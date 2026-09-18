// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// schedule.go — the DATC epoch schedule, a build/verify shared contract.
//
// Per-level epoch length E_d = clamp(α·16^d / C̄, 1, 2^22): every node sees ~α
// changes per its own epoch, equalizing the change rate across depths. build
// writes records keyed by epochOf(d, block); verify resolves them with the
// same schedule loaded from DatcMeta. Keep this the single definition so the
// two sides never drift.

package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// epochSchedule holds per-depth epoch lengths. e[d] applies to storage level
// d and account levels d >= 1; the account-trie ROOT (level 0, which has no
// TrieOfAccounts row) is recorded every accRoot blocks from the loader's
// dense slots (0 = not recorded: the reader synthesizes it from the 16
// depth-1 children). A per-block root record (accRoot = 1) costs ~16 hashes
// per block and removes the whole depth-1..3 fan-out from every proof.
type epochSchedule struct {
	e [maxChgDepth + 1]uint64
	// sto is the STORAGE tries' ladder. A zero entry means "same as e" (format
	// 2 archives and builds without --sto-sched). Deep storage levels want
	// short epochs: a proof re-folds every child that changed inside the
	// window, so a long epoch at the deepest recorded level fans out into
	// sixteen folds however fine the record depth is.
	sto     [maxChgDepth + 1]uint64
	accRoot uint64
}

// lenFor is the epoch length of one level in one trie (0 = level not
// recorded).
func (s epochSchedule) lenFor(storage bool, d int) uint64 {
	if !storage && d == 0 {
		return s.accRoot
	}
	if storage && s.sto[d] != 0 {
		return s.sto[d]
	}
	return s.e[d]
}

// stoLenFor is the STORAGE epoch length of level d for one contract. The
// contract's shift applies to one chosen level: recorded per block, that
// level's record is the exact state at any height, so the reader's recursion
// into changed children stops there and only the proof path continues below
// it. The shallowest level that leaves few enough folds is the cheapest place
// to cut, since per-block records cost min(16^level, writes) per block.
func (s epochSchedule) stoLenFor(d int, lad stoLadder) uint64 {
	l := s.lenFor(true, d)
	if sh := lad.shiftAt(d); sh > 0 {
		l >>= sh
		if l == 0 {
			l = 1
		}
	}
	return l
}

// stoEpochOf is stoLenFor's matching epoch number.
func (s epochSchedule) stoEpochOf(d int, lad stoLadder, block uint64) uint64 {
	l := s.stoLenFor(d, lad)
	if l == 0 {
		return 0
	}
	return block / l
}

// epochOfFor is epochOf for a level that may be the account root.
func (s epochSchedule) epochOfFor(storage bool, d int, block uint64) uint64 {
	l := s.lenFor(storage, d)
	if l == 0 {
		return 0
	}
	return block / l
}

func newSchedule(alpha, cbar float64) epochSchedule {
	var s epochSchedule
	for d := 0; d <= maxChgDepth; d++ {
		e := alpha * pow16(d) / cbar
		if e < 1 {
			e = 1
		}
		if e > 1<<22 {
			e = 1 << 22
		}
		s.e[d] = uint64(e)
	}
	return s
}

func pow16(d int) float64 {
	v := 1.0
	for i := 0; i < d; i++ {
		v *= 16
	}
	return v
}

func (s epochSchedule) epochOf(d int, block uint64) uint64 { return block / s.e[d] }

// parseSchedule parses "e0,e1,...,e5" (exactly maxChgDepth+1 values ≥ 1).
func parseSchedule(str string) (epochSchedule, error) {
	var s epochSchedule
	parts := strings.Split(str, ",")
	if len(parts) != maxChgDepth+1 {
		return s, fmt.Errorf("need %d comma-separated epoch lengths, got %d", maxChgDepth+1, len(parts))
	}
	for i, p := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err != nil || v == 0 {
			return s, fmt.Errorf("epoch %d: %q is not a positive integer", i, p)
		}
		s.e[i] = v
	}
	return s, nil
}

// resolveSchedule turns the --alpha/--cbar/--sched/--sto-sched/--acc-root-epoch
// flags into the effective schedule.
//
// Order matters: --sched replaces the whole ladder, so the storage ladder has
// to be applied AFTER it. Applying --sto-sched first dropped it on the floor
// whenever both flags were given -- the storage tries then silently inherited
// the account ladder, which is the very thing --sto-sched exists to prevent.
func resolveSchedule(alpha, cbar float64, schedStr, stoSchedStr string, accRoot uint64) (epochSchedule, error) {
	sched := newSchedule(alpha, cbar)
	if schedStr != "" {
		s, err := parseSchedule(schedStr)
		if err != nil {
			return sched, fmt.Errorf("--sched: %w", err)
		}
		sched = s
	}
	if stoSchedStr != "" {
		s, err := parseSchedule(stoSchedStr)
		if err != nil {
			return sched, fmt.Errorf("--sto-sched: %w", err)
		}
		sched.sto = s.e
	}
	sched.accRoot = accRoot
	return sched, nil
}

// writeQuerierMeta records in DatcMeta what a reader needs to interpret the
// records: head (exclusive), the account and storage ladders, the on-disk
// format, the storage-root row cadence (srcad) and the record depths. The
// build writes it with its final flush; stamp-meta writes it into a snapshot
// of a build that has not finished.
func writeQuerierMeta(tx kv.RwTx, hi uint64, sched epochSchedule, accDepth, stoDepth int, srcad uint64) error {
	meta := make([]byte, 8+8+8)
	binary.BigEndian.PutUint64(meta[0:], hi)
	binary.BigEndian.PutUint64(meta[8:], sched.e[0])
	binary.BigEndian.PutUint64(meta[16:], uint64(maxChgDepth))
	if err := tx.Put(tDatcMeta, []byte("head"), meta); err != nil {
		return err
	}
	var sb, ssb []byte
	for d := 0; d <= maxChgDepth; d++ {
		sb = binary.BigEndian.AppendUint64(sb, sched.e[d])
		ssb = binary.BigEndian.AppendUint64(ssb, sched.lenFor(true, d))
	}
	if err := tx.Put(tDatcMeta, []byte("sched"), sb); err != nil {
		return err
	}
	if err := tx.Put(tDatcMeta, []byte("stosched"), ssb); err != nil {
		return err
	}
	if err := tx.Put(tDatcMeta, []byte("format"), []byte{datcFormat}); err != nil {
		return err
	}
	for k, v := range map[string]uint64{"srcad": srcad, "accdepth": uint64(accDepth), "stodepth": uint64(stoDepth), "accroot": sched.accRoot} {
		if err := tx.Put(tDatcMeta, []byte(k), binary.BigEndian.AppendUint64(nil, v)); err != nil {
			return err
		}
	}
	return nil
}

// runStampMeta writes the querier meta into a SNAPSHOT of an unfinished
// build (the build only writes it with its final flush), so verify/proof/
// bench can read a mid-build checkpoint. The ladders and depths must be the
// build's own flags; head defaults to the committed resume point
// (DatcMeta/progress), the first block the snapshot has not built.
func runStampMeta(args []string) {
	fs := flag.NewFlagSet("stamp-meta", flag.ExitOnError)
	out := fs.String("out", "", "snapshot DATC dir (no build running on it)")
	head := fs.Uint64("head", 0, "head (exclusive); 0 = DatcMeta/progress")
	schedStr := fs.String("sched", "", "the build's --sched")
	stoSchedStr := fs.String("sto-sched", "", "the build's --sto-sched")
	accRoot := fs.Uint64("acc-root-epoch", 0, "the build's --acc-root-epoch")
	accDepth := fs.Int("acc-depth", 0, "the build's --acc-depth")
	stoDepth := fs.Int("sto-depth", 0, "the build's --sto-depth (0 with a depth map)")
	window := fs.Bool("window", false, "the build's --window")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" || *schedStr == "" || *accDepth == 0 {
		die("stamp-meta needs --out, --sched and --acc-depth")
	}
	sched, err := resolveSchedule(0, 0, *schedStr, *stoSchedStr, *accRoot)
	if err != nil {
		die("sched: %v", err)
	}
	srcad := uint64(1)
	if *window {
		srcad = sched.e[1]
	}
	modulesInit()
	db, err := openDatcDB(log.New(), *out, *mapGB, 1)
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		hi := *head
		if hi == 0 {
			pv, err := tx.GetOne(tDatcMeta, []byte("progress"))
			if err != nil || len(pv) != 8 {
				return fmt.Errorf("DatcMeta/progress missing (%v): pass --head", err)
			}
			hi = binary.BigEndian.Uint64(pv)
		}
		if err := writeQuerierMeta(tx, hi, sched, *accDepth, *stoDepth, srcad); err != nil {
			return err
		}
		fmt.Printf("stamped head=%d sched=%v stosched=%v accdepth=%d stodepth=%d accroot=%d srcad=%d format=%d\n",
			hi, sched.e, sched.sto, *accDepth, *stoDepth, sched.accRoot, srcad, datcFormat)
		return nil
	})
	if err != nil {
		die("stamp-meta: %v", err)
	}
}
