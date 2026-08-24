// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-datc fork-state — copies the live-state tables (HashedAccounts,
// HashedStorage, TrieOfAccounts, TrieOfStorage) plus the DatcMeta stamps from
// an existing build DB into a FRESH MDBX (Pipeline B's fresh-DB step).
//
// Why: resuming the 880 GB build DB thrashes (random B+tree writes over the
// whole file under Windows WriteMap = pageswap storms). The records-only
// continuation needs just the head state + trie — a couple hundred GB copied
// SEQUENTIALLY (cursor walk + Append/AppendDup, the 3-10x bulk path) into a
// compact fresh tree whose working set is the new range only. The old DB
// stays read-only: the querier overlays node/StoRoot reads across both.
//
// Resume: each table records its last written key; a re-run seeks the source
// past it and continues (hard-kill safe — commits every --commit-rows).

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runForkState(args []string) {
	fs := flag.NewFlagSet("fork-state", flag.ExitOnError)
	src := fs.String("src", "", "existing DATC build dir (read-only source)")
	dst := fs.String("dst", "", "fresh DATC dir for the records-only continuation")
	commitRows := fs.Uint64("commit-rows", 8_000_000, "rows per dst commit")
	mapGB := fs.Int("map.gb", 2048, "MDBX map size GB")
	_ = fs.Parse(args)
	if *src == "" || *dst == "" {
		die("--src and --dst required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	sdb, err := mdbxkv.NewMDBX(logger).Path(*src).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open src: %v", err)
	}
	defer sdb.Close()
	ddb, err := mdbxkv.NewMDBX(logger).Path(*dst).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Open(context.Background())
	if err != nil {
		die("open dst: %v", err)
	}
	defer ddb.Close()

	// State + trie tables. DupSort tables copy at the LOGICAL cursor level and
	// append back the same way (Append splits AutoDupSort keys internally —
	// the proven n42-migrate-reth-hashed path); pure-DupSort TrieOfStorage
	// uses AppendDup on the physical (key, dup) pairs.
	tables := []struct {
		name string
		dup  bool // pure DupSort (physical AppendDup); false = plain/auto Append
	}{
		{modules.HashedAccounts, false},
		{modules.HashedStorage, false}, // AutoDupSort: logical Append round-trips
		{modules.TrieOfAccounts, false},
		{modules.TrieOfStorage, true},
	}
	t0 := time.Now()
	for _, tbl := range tables {
		if err := forkCopyTable(sdb, ddb, tbl.name, tbl.dup, *commitRows); err != nil {
			die("copy %s: %v", tbl.name, err)
		}
	}

	// Meta stamps: schedules + head + stoRootFrom carry over verbatim; the
	// continuation build auto-resumes from head exactly like an in-place resume.
	if err := forkCopyMeta(sdb, ddb); err != nil {
		die("meta: %v", err)
	}
	fmt.Printf("fork-state DONE in %s\n", time.Since(t0).Round(time.Second))
}

func forkCopyTable(sdb kv.RoDB, ddb kv.RwDB, table string, dup bool, commitRows uint64) error {
	ctx := context.Background()
	stx, err := sdb.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer stx.Rollback()

	dtx, err := ddb.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if dtx != nil {
			dtx.Rollback()
		}
	}()

	var rows uint64
	t0 := time.Now()
	commit := func() error {
		if err := dtx.Commit(); err != nil {
			return err
		}
		dtx, err = ddb.BeginRw(ctx)
		return err
	}

	if dup {
		sc, err := stx.CursorDupSort(table)
		if err != nil {
			return err
		}
		defer sc.Close()
		dc, err := dtx.RwCursorDupSort(table)
		if err != nil {
			return err
		}
		// Resume: skip source rows ≤ dst's last (key, dup).
		lastK, lastV, err := dc.Last()
		if err != nil {
			return err
		}
		dc.Close()
		k, v, err := sc.First()
		if lastK != nil {
			k, v, err = sc.Seek(lastK)
			for k != nil && err == nil && bytes.Equal(k, lastK) && bytes.Compare(v, lastV) <= 0 {
				k, v, err = sc.NextDup()
				if k == nil { // dup run ended: move to the next key
					k, v, err = sc.NextNoDup()
					break
				}
			}
			fmt.Printf("[fork] %s: resume past %x\n", table, lastK[:min(8, len(lastK))])
		}
		dcw, err := dtx.RwCursorDupSort(table)
		if err != nil {
			return err
		}
		for ; k != nil && err == nil; k, v, err = sc.Next() {
			if e := dcw.AppendDup(k, v); e != nil {
				return fmt.Errorf("appenddup k=%x: %w", k, e)
			}
			rows++
			if rows%commitRows == 0 {
				dcw.Close()
				if e := commit(); e != nil {
					return e
				}
				if dcw, err = dtx.RwCursorDupSort(table); err != nil {
					return err
				}
				fmt.Printf("[fork] %s: %dM rows (%.0f rows/s)\n", table, rows/1e6, float64(rows)/time.Since(t0).Seconds())
			}
		}
		if err != nil {
			return err
		}
		dcw.Close()
	} else {
		sc, err := stx.Cursor(table)
		if err != nil {
			return err
		}
		defer sc.Close()
		dc, err := dtx.RwCursor(table)
		if err != nil {
			return err
		}
		lastK, _, err := dc.Last()
		if err != nil {
			return err
		}
		dc.Close()
		k, v, err := sc.First()
		if lastK != nil {
			k, v, err = sc.Seek(lastK)
			for k != nil && err == nil && bytes.Compare(k, lastK) <= 0 {
				k, v, err = sc.Next()
			}
			fmt.Printf("[fork] %s: resume past %x\n", table, lastK[:min(8, len(lastK))])
		}
		dcw, err := dtx.RwCursor(table)
		if err != nil {
			return err
		}
		for ; k != nil && err == nil; k, v, err = sc.Next() {
			if e := dcw.Append(k, v); e != nil {
				return fmt.Errorf("append k=%x: %w", k, e)
			}
			rows++
			if rows%commitRows == 0 {
				dcw.Close()
				if e := commit(); e != nil {
					return e
				}
				if dcw, err = dtx.RwCursor(table); err != nil {
					return err
				}
				fmt.Printf("[fork] %s: %dM rows (%.0f rows/s)\n", table, rows/1e6, float64(rows)/time.Since(t0).Seconds())
			}
		}
		if err != nil {
			return err
		}
		dcw.Close()
	}
	if err := dtx.Commit(); err != nil {
		return err
	}
	dtx = nil
	fmt.Printf("[fork] %s: DONE %d rows in %s\n", table, rows, time.Since(t0).Round(time.Second))
	return nil
}

func forkCopyMeta(sdb kv.RoDB, ddb kv.RwDB) error {
	ctx := context.Background()
	stx, err := sdb.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer stx.Rollback()
	dtx, err := ddb.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer dtx.Rollback()
	for _, key := range []string{"head", "sched", "stoSched", "stoRootFrom", "leafprog"} {
		v, err := stx.GetOne(tDatcMeta, []byte(key))
		if err != nil {
			return err
		}
		if len(v) == 0 {
			fmt.Printf("[fork] meta %q absent in src (skipped)\n", key)
			continue
		}
		if err := dtx.Put(tDatcMeta, []byte(key), v); err != nil {
			return err
		}
	}
	// Record the fork provenance: the querier overlay knows records < forkHead
	// live in the src DB.
	if hv, _ := stx.GetOne(tDatcMeta, []byte("head")); len(hv) >= 8 {
		if err := dtx.Put(tDatcMeta, []byte("forkedAt"), hv[:8]); err != nil {
			return err
		}
	}
	return dtx.Commit()
}
