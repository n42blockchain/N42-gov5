// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// merge — fold two contiguous range builds ([0, X) and [X, end), the upper
// one built from a mid-chain state with prep-state) into one output that
// serves every height. Either side may be the one that receives the rows
// (--into); the other is re-spilled into it.
//
// Rows of the two ranges are disjoint except the boundary epoch: the lower
// build's final flush wrote PARTIAL-epoch node records (state at X-1) for
// every level whose epoch is longer than one block, the upper build wrote
// the true end-of-epoch ones. The reader's floor lookup (Seek then Prev)
// lands on the LAST record with a given key, so the upper build's record
// must sort last. When the lower build is --into, finalize's stable merge
// (existing rows first, new rows second) already guarantees that; when the
// upper build is --into, the lower build's boundary-epoch partial records
// are DROPPED while re-spilling (they are redundant: heights inside that
// epoch step back to the previous record and roll the change rows forward).
// The upper build's first record per path is FULL (resumed build), so no
// DIFF chain ever crosses the boundary. In MDBX (storage node records) Put
// overwrites, with the same drop rule.
//
//	n42-datc merge --into /data/datc-25m-v2-hi --from /mnt/win/datc-v2-lo
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func runMerge(args []string) {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	into := fs.String("into", "", "genesis-range build dir (receives the rows)")
	from := fs.String("from", "", "upper-range build dir")
	mapGB := fs.Int("map.gb", 4096, "MDBX map size GB")
	fromStart := fs.Uint64("from-start", 0, "first block of the upper build when its DatcMeta/start is absent (older binary)")
	_ = fs.Parse(args)
	if *into == "" || *from == "" {
		die("--into and --from required")
	}
	modulesInit()
	if err := mergeBuilds(*into, *from, *mapGB, *fromStart); err != nil {
		die("merge: %v", err)
	}
}

// mergeBuilds does the work of the merge subcommand.
func mergeBuilds(into, from string, mapGB int, fromStart uint64) error {
	if _, err := os.Stat(filepath.Join(into, leafSpillDir)); err == nil {
		return fmt.Errorf("%s still has a leafspill dir (build not finalized)", into)
	}
	if _, err := os.Stat(filepath.Join(from, leafSpillDir)); err == nil {
		return fmt.Errorf("%s still has a leafspill dir (build not finalized)", from)
	}
	logger := log.New()
	dbI, err := openDatcDB(logger, into, mapGB, 4)
	if err != nil {
		return err
	}
	defer dbI.Close()
	dbF, err := openDatcDB(logger, from, mapGB, 1)
	if err != nil {
		return err
	}
	defer dbF.Close()
	txF, err := dbF.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer txF.Rollback()

	// Meta contract: same format/schedule/depths/cadence; contiguous ranges.
	txI, err := dbI.BeginRo(context.Background())
	if err != nil {
		return err
	}
	metaI, err := readMeta(txI)
	txI.Rollback()
	if err != nil {
		return err
	}
	metaF, err := readMeta(txF)
	if err != nil {
		return err
	}
	for _, k := range []string{"format", "sched", "accdepth", "stodepth", "srcad", "accroot"} {
		if !bytes.Equal(metaI[k], metaF[k]) {
			return fmt.Errorf("meta %q differs: into=%x from=%x", k, metaI[k], metaF[k])
		}
	}
	headI := binary.BigEndian.Uint64(metaI["head"])
	headF := binary.BigEndian.Uint64(metaF["head"])
	metaStart := func(m map[string][]byte, fallback uint64) uint64 {
		if sv, ok := m["start"]; ok && len(sv) == 8 {
			return binary.BigEndian.Uint64(sv)
		}
		return fallback
	}
	startF := metaStart(metaF, fromStart)
	startI := metaStart(metaI, 0)
	// Which side is the lower range? from is upper when it starts where
	// into ends; from is lower when it ends where into starts.
	var fromIsLower bool
	switch {
	case startF == headI && headF > headI:
		fromIsLower = false
	case headF == startI && startI > 0:
		fromIsLower = true
	default:
		return fmt.Errorf("ranges not contiguous: into=[%d,%d) from=[%d,%d) (pass --from-start when the upper build predates DatcMe/start)", startI, headI, startF, headF)
	}
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(metaF["sched"]); d++ {
		sched.e[d] = binary.BigEndian.Uint64(metaF["sched"][d*8:])
	}
	if v := metaF["accroot"]; len(v) == 8 {
		sched.accRoot = binary.BigEndian.Uint64(v)
	}
	// boundaryDrop reports whether a node-record key of the LOWER build must
	// be dropped: its epoch is the (partial) boundary epoch of a level whose
	// epoch spans more than one block.
	boundary := headF // lower's exclusive end when from is lower
	boundaryDrop := func(storage bool, k []byte) bool {
		if !fromIsLower || len(k) < 5 {
			return false
		}
		pathLen := int(k[0])
		d := pathLen
		if storage {
			d = pathLen - stoDomainLen
		}
		if d < 0 || d > maxChgDepth || len(k) != 1+pathLen+4 {
			return false
		}
		l := sched.lenFor(storage, d)
		if l <= 1 {
			return false
		}
		return uint64(binary.BigEndian.Uint32(k[1+pathLen:])) == (boundary-1)/l
	}
	if fromIsLower {
		fmt.Printf("merge [%d, %d) → prepend to [%d, %d)\n", startF, headF, startI, headI)
	} else {
		fmt.Printf("merge [%d, %d) ← [%d, %d)\n", startI, headI, startF, headF)
	}

	// Segments: re-spill the upper build's segment rows into the lower
	// build's spill dir, then finalize (merges with the existing segments).
	sw, err := newLeafSpillWriter(into)
	if err != nil {
		return err
	}
	// One frame cache per segment set: the cache key is (table, bucket,
	// frame) and does not name the directory, so sets from two builds must
	// never share one.
	// A lower boundary-epoch record is dropped ONLY when the upper build
	// wrote a record for the same key (the node changed again inside the
	// epoch); otherwise the lower's record IS the end-of-epoch state.
	var upperNA *leafSegSet
	if fromIsLower {
		if set, ok, err := openLeafSegSet(into, segTabNodeA, newFrameLRUSize(64)); err != nil {
			return err
		} else if ok {
			upperNA = set
			defer set.Close()
		}
	}
	upperHasNA := func(k []byte) bool {
		if upperNA == nil {
			return false
		}
		c := upperNA.Cursor()
		fk, _, _ := c.Seek(k)
		return fk != nil && bytes.Equal(fk, k)
	}
	for tab := 0; tab < segTabCount; tab++ {
		set, ok, err := openLeafSegSet(from, tab, newFrameLRUSize(64))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		c := set.Cursor()
		n, dropped := 0, 0
		for k, v, e := c.Seek([]byte{0}); k != nil && e == nil; k, v, e = c.Next() {
			if tab == segTabNodeA && boundaryDrop(false, k) && upperHasNA(k) {
				if os.Getenv("DATC_MERGE_TRACE") != "" {
					fmt.Printf("    drop %x\n", k)
				}
				dropped++
				continue
			}
			if err := sw.add(tab, k, v); err != nil {
				return err
			}
			n++
		}
		set.Close()
		fmt.Printf("  %-3s %d rows re-spilled (%d boundary-epoch partial records dropped)\n", segTabNames[tab], n, dropped)
	}
	if err := sw.close(); err != nil {
		return err
	}
	if err := finalizeLeafSegments(into); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(into, leafSpillDir)); err == nil {
		return fmt.Errorf("finalize retained the spill dir (corrupt frames?) — merge incomplete")
	}

	// MDBX: storage node records (upper wins on equal keys) + meta head.
	txW, err := dbI.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer txW.Rollback()
	for _, tab := range []string{tDatcStoNode, tDatcAccNode, tDatcStoRoot, tDatcAccChg, tDatcStoChg, tDatcLeafA, tDatcLeafS} {
		c, err := txF.Cursor(tab)
		if err != nil {
			return err
		}
		n := 0
		for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
			if (tab == tDatcStoNode && boundaryDrop(true, k)) || (tab == tDatcAccNode && boundaryDrop(false, k)) {
				if ex, _ := txW.GetOne(tab, k); ex != nil {
					continue // upper build has the true end-of-epoch record
				}
			}
			if err := txW.Put(tab, k, v); err != nil {
				c.Close()
				return err
			}
			n++
		}
		c.Close()
		if n > 0 {
			fmt.Printf("  mdbx %-13s %d rows copied\n", tab, n)
		}
	}
	if !fromIsLower {
		if err := txW.Put(tDatcMeta, []byte("head"), metaF["head"]); err != nil {
			return err
		}
		if err := txW.Put(tDatcMeta, []byte("progress"), metaF["progress"]); err != nil {
			return err
		}
	}
	var sb [8]byte
	binary.BigEndian.PutUint64(sb[:], min(startI, startF))
	if err := txW.Put(tDatcMeta, []byte("start"), sb[:]); err != nil {
		return err
	}
	if err := txW.Commit(); err != nil {
		return err
	}
	fmt.Printf("merged: [%d, %d)\n", min(startI, startF), max(headI, headF))
	return nil
}

func readMeta(tx kv.Tx) (map[string][]byte, error) {
	m := map[string][]byte{}
	c, err := tx.Cursor(tDatcMeta)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		m[string(k)] = append([]byte{}, v...)
	}
	if len(m["head"]) < 8 {
		return nil, fmt.Errorf("DatcMeta/head missing")
	}
	return m, nil
}
