// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestPerContractStorageDepth builds with a depth map that gives one contract
// three recorded levels and leaves every other contract at zero, then checks
// the three properties the dynamic-depth format rests on: the named contract
// gets records at levels 0..2, the unnamed ones get none, and the reader picks
// each contract's depth up from DatcStoDepth. Proof correctness is covered by
// the record-vs-fold comparison, which must hold at every recorded level.
func TestPerContractStorageDepth(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	logger := log.New()
	db, err := openDatcDB(logger, out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)

	deep := sc.big[0]
	deepHash := keccak(deep[:])
	mapPath := filepath.Join(out, "depth.map")
	if err := os.WriteFile(mapPath, []byte(fmt.Sprintf("%x 3\n", deepHash[:])), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := loadStoDepthMap(mapPath)
	if err != nil {
		t.Fatal(err)
	}

	o := e2eOpts{sched: schedE0is4, batch: 50, stoCache: 64}
	end := uint64(len(sc.blocks))
	b := newTestBuilder(t, db, out, sc, o, 0)
	b.stoDepth = 0 // unnamed contracts: no records, the reader folds them whole
	b.stoDepthMap = m
	b.stoDepthSeen = make(map[string]uint8, len(m))
	if err := b.run(0, end, o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}

	byDepth := map[int]int{}
	nibbles := map[int]map[byte]bool{}
	others := 0
	var depthRows int
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(tDatcStoNode)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, _, e := c.First(); k != nil && e == nil; k, _, e = c.Next() {
			d := int(k[0]) - stoDomainLen
			if string(k[1:1+stoDomainLen]) != string(deepHash[:]) {
				others++
				continue
			}
			byDepth[d]++
			if d >= 1 {
				if nibbles[d] == nil {
					nibbles[d] = map[byte]bool{}
				}
				nibbles[d][k[1+stoDomainLen+d-1]] = true
			}
		}
		dc, err := tx.Cursor(tDatcStoDepth)
		if err != nil {
			return err
		}
		defer dc.Close()
		for k, v, e := dc.First(); k != nil && e == nil; k, v, e = dc.Next() {
			depthRows++
			if len(v) != 1 || v[0] != 3 || string(k[:stoDomainLen]) != string(deepHash[:]) {
				return fmt.Errorf("unexpected DatcStoDepth row %x=%x", k, v)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if others != 0 {
		t.Errorf("contracts without a depth entry got %d storage node records; they should be folded whole", others)
	}
	for d := 0; d <= 2; d++ {
		if byDepth[d] == 0 {
			t.Errorf("no storage node records at depth %d for the mapped contract (%v)", d, byDepth)
		}
	}
	if byDepth[3] != 0 {
		t.Errorf("records written at depth 3, below the mapped fold depth")
	}
	if len(nibbles[1]) < 2 || len(nibbles[2]) < 2 {
		t.Errorf("records do not spread over child nibbles: d1=%d d2=%d distinct", len(nibbles[1]), len(nibbles[2]))
	}
	if depthRows != 1 {
		t.Errorf("DatcStoDepth has %d rows, want exactly one (depth never changes here)", depthRows)
	}

	// The reader must take the depth from the archive, per contract.
	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()
	q.stoFold = 0
	if got := q.stoFoldAt(deepHash[:], end-1); got != 3 {
		t.Errorf("reader fold depth for the mapped contract = %d, want 3", got)
	}
	var plain types.Address
	copy(plain[:], sc.small[0][:])
	plainHash := keccak(plain[:])
	if got := q.stoFoldAt(plainHash[:], end-1); got != 0 {
		t.Errorf("reader fold depth for an unmapped contract = %d, want the meta default 0", got)
	}

	// Records and leaf history must agree at every recorded level.
	for n := uint64(0); n < end; n += 7 {
		for nib := byte(0); nib < 16; nib++ {
			path := []byte{nib}
			rec, recOK, err := q.nodeHashAt(deepHash[:], path, n)
			if err != nil {
				t.Fatal(err)
			}
			fold, foldOK, err := q.foldAt(deepHash[:], path, n)
			if err != nil {
				t.Fatal(err)
			}
			if recOK != foldOK || (recOK && rec != fold) {
				t.Fatalf("n=%d path=%x: record=%x(ok=%v) fold=%x(ok=%v)", n, path, rec[:8], recOK, fold[:8], foldOK)
			}
		}
	}
}
