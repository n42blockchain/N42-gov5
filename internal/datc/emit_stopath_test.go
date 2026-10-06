// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestStorageNodeRecordsCoverEveryChild guards the shared-scratch overwrite in
// recordChangeStorage: the aggregation key's epoch bytes used to land on the
// path nibble that the NEXT level reads, so every storage level below 0 keyed
// its dirty path with a zero nibble. Only child 0 then got a node record and a
// proof had to fold the other 15 subtrees from the leaf history — on the 25M
// mainnet archive that was 94% of a slow query's reads.
//
// The overwrite only happened when level 0's epoch is longer than one block
// (e[0] == 1 skips the aggregation row), so this schedule must keep e[0] > 1.
func TestStorageNodeRecordsCoverEveryChild(t *testing.T) {
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

	o := e2eOpts{
		sched:    epochSchedule{e: [maxChgDepth + 1]uint64{4, 64, 4, 1, 4096, 4096}, accRoot: 1},
		batch:    50,
		stoCache: 64,
		accDepth: 4,
		stoDepth: 2,
	}
	end := uint64(len(sc.blocks))
	b := newTestBuilder(t, db, out, sc, o, 0)
	if err := b.run(0, end, o.batch); err != nil {
		t.Fatalf("build: %v", err)
	}

	recNib, slotNib := map[byte]int{}, map[byte]int{}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		c, err := tx.Cursor(tDatcStoNode)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, _, e := c.First(); k != nil && e == nil; k, _, e = c.Next() {
			if int(k[0]) == stoDomainLen+1 && len(k) > 1+stoDomainLen {
				recNib[k[1+stoDomainLen]]++
			}
		}
		lc, err := tx.Cursor(tDatcLeafS)
		if err != nil {
			return err
		}
		defer lc.Close()
		for k, _, e := lc.First(); k != nil && e == nil; k, _, e = lc.Next() {
			if len(k) > stoDomainLen {
				slotNib[k[stoDomainLen]>>4]++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(slotNib) < 8 {
		t.Fatalf("scenario touches only %d distinct slot-hash nibbles — too narrow to test", len(slotNib))
	}
	if len(recNib) < len(slotNib) {
		t.Fatalf("depth-1 storage node records cover %d nibbles (%v) but the slot hashes cover %d: "+
			"children without a record force the reader to fold their whole subtree",
			len(recNib), recNib, len(slotNib))
	}
}
