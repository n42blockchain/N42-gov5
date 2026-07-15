// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package node

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// staticBlockProvider implements HealthProvider with a fixed block number.
type staticBlockProvider struct {
	block uint64
}

func (s *staticBlockProvider) CurrentBlock() uint64 { return s.block }
func (s *staticBlockProvider) HighestBlock() uint64 { return s.block }
func (s *staticBlockProvider) IsSyncing() bool      { return false }
func (s *staticBlockProvider) PeerCount() int       { return 1 }

func TestPruner_MaybePrune_SkipsWhenNotEnoughHistory(t *testing.T) {
	db := memdb.NewTestDB(t)
	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  100,
		PruneInterval:   10,
		PruneBatchLimit: 1000,
	}
	hp := &staticBlockProvider{block: 50} // less than retention

	pruner := NewPruner(db, config, hp, nil)
	pruner.maybePrune()

	// Should not have pruned (currentBlock < retention)
	if pruner.lastPrunedBlock != 0 {
		t.Errorf("expected lastPrunedBlock=0, got %d", pruner.lastPrunedBlock)
	}
}

func TestPruner_MaybePrune_SkipsWhenIntervalNotReached(t *testing.T) {
	db := memdb.NewTestDB(t)
	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  100,
		PruneInterval:   1000,
		PruneBatchLimit: 1000,
	}
	hp := &staticBlockProvider{block: 200}

	pruner := NewPruner(db, config, hp, nil)
	pruner.lastPrunedBlock = 195 // only 5 blocks since last prune

	pruner.maybePrune()

	if pruner.lastPrunedBlock != 195 {
		t.Errorf("should not have pruned, lastPrunedBlock changed to %d", pruner.lastPrunedBlock)
	}
}

func TestPruner_PruneChangeSets(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write test data: AccountChangeSet entries for blocks 1-200
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 200; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, i)
			val := []byte("account-data")
			if err := tx.Put(modules.AccountChangeSet, key, val); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  50,
		PruneInterval:   10,
		PruneBatchLimit: 10000,
	}
	hp := &staticBlockProvider{block: 200}

	pruner := NewPruner(db, config, hp, nil)
	pruner.maybePrune()

	if pruner.lastPrunedBlock != 200 {
		t.Fatalf("expected lastPrunedBlock=200, got %d", pruner.lastPrunedBlock)
	}

	// Verify: blocks 1-149 should be pruned, blocks 150-200 should remain
	// pruneTo = 200 - 50 = 150
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.AccountChangeSet)
		if err != nil {
			return err
		}
		defer c.Close()

		k, _, err := c.First()
		if err != nil {
			return err
		}
		if k == nil {
			t.Fatal("all entries pruned, expected some to remain")
		}
		firstBlock := binary.BigEndian.Uint64(k)
		if firstBlock < 150 {
			t.Errorf("expected first remaining block >= 150, got %d", firstBlock)
		}

		// Count remaining entries
		count := 0
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				return err
			}
			count++
		}
		// blocks 150-200 = 51 entries
		if count != 51 {
			t.Errorf("expected 51 remaining entries, got %d", count)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPruner_PruneReceipts(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write test receipts for blocks 1-100
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 100; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, i)
			if err := tx.Put(modules.Receipts, key, []byte("receipt")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  20,
		PruneInterval:   10,
		PruneBatchLimit: 10000,
		PruneReceipts:   true,
	}
	hp := &staticBlockProvider{block: 100}

	pruner := NewPruner(db, config, hp, nil)
	pruner.maybePrune()

	// pruneTo = 100 - 20 = 80, so blocks 1-79 pruned, 80-100 remain
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Receipts)
		if err != nil {
			return err
		}
		defer c.Close()

		k, _, err := c.First()
		if err != nil {
			return err
		}
		if k == nil {
			t.Fatal("all receipts pruned")
		}
		firstBlock := binary.BigEndian.Uint64(k)
		if firstBlock < 80 {
			t.Errorf("expected first remaining receipt >= 80, got %d", firstBlock)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPruner_NoReceiptPruneByDefault(t *testing.T) {
	db := memdb.NewTestDB(t)

	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 50; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, i)
			if err := tx.Put(modules.Receipts, key, []byte("receipt")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  10,
		PruneInterval:   5,
		PruneBatchLimit: 10000,
		PruneReceipts:   false, // default: don't prune receipts
	}
	hp := &staticBlockProvider{block: 50}

	pruner := NewPruner(db, config, hp, nil)
	pruner.maybePrune()

	// Receipts should all remain
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Receipts)
		if err != nil {
			return err
		}
		defer c.Close()

		count := 0
		for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
			if err != nil {
				return err
			}
			count++
		}
		if count != 50 {
			t.Errorf("expected all 50 receipts to remain, got %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPruner_MaybePrune_SkipsAfterReorg(t *testing.T) {
	db := memdb.NewTestDB(t)
	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  100,
		PruneInterval:   10,
		PruneBatchLimit: 1000,
	}
	// Simulate reorg: current block went backward
	hp := &staticBlockProvider{block: 150}

	pruner := NewPruner(db, config, hp, nil)
	pruner.lastPrunedBlock = 200 // was higher before reorg

	pruner.maybePrune()

	// Should NOT have pruned (currentBlock < lastPrunedBlock)
	if pruner.lastPrunedBlock != 200 {
		t.Errorf("expected lastPrunedBlock=200 (unchanged), got %d", pruner.lastPrunedBlock)
	}
}

func TestPruner_StartStop(t *testing.T) {
	db := memdb.NewTestDB(t)
	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  100,
		PruneInterval:   10,
		PruneBatchLimit: 1000,
	}
	hp := &staticBlockProvider{block: 0}

	pruner := NewPruner(db, config, hp, nil)
	pruner.Start()
	pruner.Stop() // should not hang
}

func TestPruneConfig_IsEnabled(t *testing.T) {
	tests := []struct {
		mode    conf.PruneMode
		enabled bool
	}{
		{conf.PruneModeArchive, false},
		{conf.PruneModeFull, true},
		{"", false},
	}
	for _, tt := range tests {
		c := conf.PruneConfig{Mode: tt.mode}
		if c.IsEnabled() != tt.enabled {
			t.Errorf("PruneConfig{Mode: %q}.IsEnabled() = %v, want %v", tt.mode, c.IsEnabled(), tt.enabled)
		}
	}
}

// TestPruner_DupSortBatchedDrainsBacklog verifies that when the changeset
// backlog far exceeds PruneBatchLimit, the row-budgeted DupSort prune still
// drains everything below pruneTo across multiple committed batches (rather
// than deleting it all in one unbounded transaction). This locks the Windows
// WriteMap OOM fix: each batch is bounded, but the loop finishes the job.
func TestPruner_DupSortBatchedDrainsBacklog(t *testing.T) {
	db := memdb.NewTestDB(t)

	// 500 block groups, each with several duplicate slots in both DupSort tables.
	const blocks = 500
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= blocks; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, i)
			for d := 0; d < 4; d++ {
				val := append([]byte{byte(d)}, []byte("changeset")...)
				if err := tx.Put(modules.AccountChangeSet, key, val); err != nil {
					return err
				}
				if err := tx.Put(modules.StorageChangeSet, key, val); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Small batch limit forces pruneDupSortBatched to loop many times.
	config := conf.PruneConfig{
		Mode:            conf.PruneModeFull,
		BlockRetention:  50,
		PruneInterval:   10,
		PruneBatchLimit: 30,
	}
	hp := &staticBlockProvider{block: blocks}

	pruner := NewPruner(db, config, hp, nil)
	pruner.maybePrune()

	if pruner.lastPrunedBlock != blocks {
		t.Fatalf("expected lastPrunedBlock=%d, got %d", blocks, pruner.lastPrunedBlock)
	}

	// pruneTo = 500 - 50 = 450. Everything below 450 must be gone in BOTH tables,
	// everything >= 450 must remain — proving full drainage, not just one batch.
	pruneTo := uint64(blocks - config.BlockRetention)
	for _, table := range []string{modules.AccountChangeSet, modules.StorageChangeSet} {
		if err := db.View(testCtx(), func(tx kv.Tx) error {
			c, err := tx.CursorDupSort(table)
			if err != nil {
				return err
			}
			defer c.Close()
			k, _, err := c.First()
			if err != nil {
				return err
			}
			if k == nil {
				t.Fatalf("%s: everything pruned, expected blocks >= %d to remain", table, pruneTo)
			}
			if first := binary.BigEndian.Uint64(k); first != pruneTo {
				t.Errorf("%s: first remaining block = %d, want %d (below-pruneTo backlog not fully drained)", table, first, pruneTo)
			}
			groups := 0
			for k, _, err := c.First(); k != nil; k, _, err = c.NextNoDup() {
				if err != nil {
					return err
				}
				groups++
			}
			// blocks 450..500 inclusive = 51 groups
			if groups != int(blocks-pruneTo+1) {
				t.Errorf("%s: %d remaining groups, want %d", table, groups, blocks-pruneTo+1)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruner_ZeroBatchLimitUsesDefault(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, 1)
		return tx.Put(modules.AccountChangeSet, key, []byte("change"))
	}); err != nil {
		t.Fatal(err)
	}
	pruner := NewPruner(db, conf.PruneConfig{
		Mode:           conf.PruneModeFull,
		BlockRetention: 1,
		PruneInterval:  1,
		// Zero is possible for programmatic construction before defaults are
		// applied and must not make the batched loop spin forever.
		PruneBatchLimit: 0,
	}, &staticBlockProvider{block: 3}, nil)
	pruner.maybePrune()

	if err := db.View(testCtx(), func(tx kv.Tx) error {
		c, err := tx.CursorDupSort(modules.AccountChangeSet)
		if err != nil {
			return err
		}
		defer c.Close()
		k, _, err := c.First()
		if err != nil {
			return err
		}
		if k != nil {
			t.Fatalf("zero-limit fallback left prunable key %d", binary.BigEndian.Uint64(k))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pruner.lastPrunedBlock != 3 {
		t.Fatalf("pruner did not finish with zero configured limit: last=%d", pruner.lastPrunedBlock)
	}
}

func testCtx() context.Context {
	return context.Background()
}
