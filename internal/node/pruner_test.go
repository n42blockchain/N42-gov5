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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

	pruner := NewPruner(db, config, hp)
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

func testCtx() context.Context {
	return context.Background()
}
