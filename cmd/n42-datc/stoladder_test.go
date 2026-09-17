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

// TestPerContractDeepestLevelPerBlock is the point of the per-contract ladder:
// a contract whose deepest recorded level is written per block has an exact
// record at every height, so the reader neither replays a window nor folds
// there. Proof latency is made of those folds, so the test asserts the fold
// count — correctness alone would pass either way.
func TestPerContractDeepestLevelPerBlock(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// Same build twice: the hot contract recorded to depth 1 (its root node is
	// the one level a few-hundred-key test contract has as a full branch),
	// first on the shared 8-block ladder, then with that level per block.
	run := func(shift uint8) (folds, reads int, rows int) {
		out := t.TempDir()
		db, err := openDatcDB(log.New(), out, 4, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		writeFwd(t, db, sc)

		hot := sc.big[0]
		hotHash := keccak(hot[:])
		mapPath := filepath.Join(out, "depth.map")
		if err := os.WriteFile(mapPath, []byte(fmt.Sprintf("%x 1 %d\n", hotHash[:], shift)), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := loadStoDepthMap(mapPath)
		if err != nil {
			t.Fatal(err)
		}
		o := e2eOpts{sched: schedE0is4, stoSched: [maxChgDepth + 1]uint64{8, 8, 8, 8, 64, 64},
			batch: 50, stoCache: 64, accDepth: 2, stoDepth: 0}
		end := uint64(len(sc.blocks))
		b := newTestBuilder(t, db, out, sc, o, 0)
		b.stoDepth = 0
		b.stoDepthMap = m
		b.stoDepthSeen = make(map[string]stoLadder, len(m))
		b.stoShifts = []uint8{0, shift}
		if err := b.run(0, end, o.batch); err != nil {
			t.Fatalf("build(shift=%d): %v", shift, err)
		}
		q, closeQ := openTestQuerier(t, db, out, o)
		defer closeQ()
		q.stoFold = 0
		if got := q.stoLadderAt(hotHash[:], end-1); got.depth != 1 || got.shift != shift {
			t.Fatalf("reader ladder = %+v, want depth 1 shift %d", got, shift)
		}
		// Walk a slot proof across heights and count the work it costs. The
		// storage ROOT is exact either way (per-block root history), so the
		// recursion and folding only show up on the proof path.
		for n := uint64(end / 2); n < end; n++ {
			sm := sc.storageAt(hot, n)
			if len(sm) == 0 {
				continue
			}
			var slot types.Hash
			for k := range sm {
				slot = k
				break
			}
			sh := keccak(slot[:])
			q.folds, q.leafReads = 0, 0
			if _, err := q.proofPath(hotHash[:], nibblesOfBytes(sh[:]), n); err != nil {
				t.Fatal(err)
			}
			folds += q.folds
			reads += q.leafReads
		}
		_ = db.View(context.Background(), func(tx kv.Tx) error {
			c, err := tx.Cursor(tDatcStoNode)
			if err != nil {
				return err
			}
			defer c.Close()
			for k, _, e := c.First(); k != nil && e == nil; k, _, e = c.Next() {
				if int(k[0])-stoDomainLen == 0 && string(k[1:1+stoDomainLen]) == string(hotHash[:]) { // the deepest recorded level
					rows++
				}
			}
			return nil
		})
		return
	}

	windowFolds, windowReads, windowRows := run(0)
	blockFolds, blockReads, blockRows := run(3) // sto[0] = 8 >> 3 = per block
	t.Logf("windowed: folds=%d leafReads=%d deepestRows=%d", windowFolds, windowReads, windowRows)
	t.Logf("perBlock: folds=%d leafReads=%d deepestRows=%d", blockFolds, blockReads, blockRows)
	if blockFolds >= windowFolds {
		t.Errorf("per-block deepest level did not cut the folds: %d vs %d", blockFolds, windowFolds)
	}
	if blockReads >= windowReads {
		t.Errorf("per-block deepest level did not cut the leaf reads: %d vs %d", blockReads, windowReads)
	}
	if blockRows <= windowRows {
		t.Errorf("per-block deepest level should cost MORE records: %d vs %d", blockRows, windowRows)
	}
}
