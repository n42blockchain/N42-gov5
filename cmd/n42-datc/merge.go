// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// merge — fold an upper-range build (built from a mid-chain state with
// prep-state, blocks [X, end)) into the genesis build ([0, X)) so one
// output serves every height.
//
// Rows of the two ranges are disjoint except the boundary epoch: the lower
// build's final flush wrote partial-epoch node records at X-1, the upper
// build wrote the true end-of-epoch ones. Both are kept in the merged
// segments (lower first, upper second — finalize's stable merge order), and
// the reader's floor lookup (Seek then Prev) lands on the LAST record with a
// given key, i.e. the upper build's; the upper build's first record per path
// is FULL (resumed build), so no DIFF chain ever crosses the boundary. In
// MDBX (storage node records) Put simply overwrites.
//
//	n42-datc merge --into /data/datc-25m-v2 --from /data/datc-25m-v2-hi
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
	if sv, ok := metaF["start"]; ok && len(sv) == 8 {
		fromStart = binary.BigEndian.Uint64(sv)
	}
	if fromStart == 0 {
		return fmt.Errorf("upper build has no DatcMeta/start; pass --from-start")
	}
	if fromStart != headI {
		return fmt.Errorf("ranges not contiguous: into.head=%d from.start=%d", headI, fromStart)
	}
	if headF <= headI {
		return fmt.Errorf("upper build head %d not beyond %d", headF, headI)
	}
	fmt.Printf("merge [%d, %d) ← [%d, %d)\n", 0, headI, headI, headF)

	// Segments: re-spill the upper build's segment rows into the lower
	// build's spill dir, then finalize (merges with the existing segments).
	sw, err := newLeafSpillWriter(into)
	if err != nil {
		return err
	}
	cache := newFrameLRUSize(64)
	for tab := 0; tab < segTabCount; tab++ {
		set, ok, err := openLeafSegSet(from, tab, cache)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		c := set.Cursor()
		n := 0
		for k, v, e := c.Seek([]byte{0}); k != nil && e == nil; k, v, e = c.Next() {
			if err := sw.add(tab, k, v); err != nil {
				return err
			}
			n++
		}
		set.Close()
		fmt.Printf("  %-3s %d rows re-spilled\n", segTabNames[tab], n)
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
	if err := txW.Put(tDatcMeta, []byte("head"), metaF["head"]); err != nil {
		return err
	}
	if err := txW.Put(tDatcMeta, []byte("progress"), metaF["progress"]); err != nil {
		return err
	}
	if err := txW.Commit(); err != nil {
		return err
	}
	fmt.Printf("merged: head=%d\n", headF)
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
