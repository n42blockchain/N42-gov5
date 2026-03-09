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
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

// writeTestBlock writes minimal block data (header, body, receipts, senders,
// TD, canonical hash) for a single block number so that expiry has something
// to delete.
func writeTestBlock(t *testing.T, tx kv.RwTx, num uint64) types.Hash {
	t.Helper()
	hash := types.Hash{}
	binary.BigEndian.PutUint64(hash[:8], num) // deterministic fake hash

	blockKey := modules.HeaderKey(num, hash)
	numKey := modules.EncodeBlockNumber(num)

	// Canonical hash
	if err := tx.Put(modules.HeaderCanonical, numKey, hash.Bytes()); err != nil {
		t.Fatal(err)
	}
	// Header
	if err := tx.Put(modules.Headers, blockKey, []byte("header-data")); err != nil {
		t.Fatal(err)
	}
	// HeaderTD
	if err := tx.Put(modules.HeaderTD, blockKey, []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	// BlockBody
	if err := tx.Put(modules.BlockBody, blockKey, []byte("body-data-xx")); err != nil {
		t.Fatal(err)
	}
	// Receipts
	if err := tx.Put(modules.Receipts, numKey, []byte("receipt-data")); err != nil {
		t.Fatal(err)
	}
	// Log (block_num + tx_id)
	logKey := modules.LogKey(num, 0)
	if err := tx.Put(modules.Log, logKey, []byte("log-data")); err != nil {
		t.Fatal(err)
	}
	// Senders
	if err := tx.Put(modules.Senders, blockKey, []byte("sender-data")); err != nil {
		t.Fatal(err)
	}

	return hash
}

// blockDataExists checks whether the core block data tables have entries for
// the given block number and hash.
func blockDataExists(t *testing.T, tx kv.Tx, num uint64, hash types.Hash) bool {
	t.Helper()
	blockKey := modules.HeaderKey(num, hash)
	data, err := tx.GetOne(modules.Headers, blockKey)
	if err != nil {
		t.Fatal(err)
	}
	return len(data) > 0
}

// canonicalHashExists checks whether HeaderCanonical still has an entry for
// the given block number.
func canonicalHashExists(t *testing.T, tx kv.Tx, num uint64) bool {
	t.Helper()
	data, err := tx.GetOne(modules.HeaderCanonical, modules.EncodeBlockNumber(num))
	if err != nil {
		t.Fatal(err)
	}
	return len(data) > 0
}

func TestHistoryExpiry_Basic(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write blocks 1-100.
	hashes := make(map[uint64]types.Hash)
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 100; i++ {
			hashes[i] = writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  50,
		Interval:   10,
		BatchLimit: 10000,
	}
	hp := &staticBlockProvider{block: 100}
	expirer := NewHistoryExpirer(db, config, hp)

	// Run expiry: should expire blocks 1-49 (expireTo = 100-50 = 50).
	expirer.maybeExpire()

	if expirer.EarliestBlock() != 50 {
		t.Fatalf("expected earliestBlock=50, got %d", expirer.EarliestBlock())
	}

	// Verify blocks 1-49 are deleted, 50-100 exist.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		for i := uint64(1); i < 50; i++ {
			if blockDataExists(t, tx, i, hashes[i]) {
				t.Errorf("block %d should have been deleted", i)
			}
		}
		for i := uint64(50); i <= 100; i++ {
			if !blockDataExists(t, tx, i, hashes[i]) {
				t.Errorf("block %d should still exist", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExpiry_EarliestBlock_Persistence(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write blocks 1-50.
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 50; i++ {
			writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  20,
		Interval:   5,
		BatchLimit: 10000,
	}
	hp := &staticBlockProvider{block: 50}

	// First expirer run.
	expirer1 := NewHistoryExpirer(db, config, hp)
	expirer1.maybeExpire()
	expectedEarliest := expirer1.EarliestBlock()
	if expectedEarliest == 0 {
		t.Fatal("expected earliestBlock > 0 after expiry")
	}

	// Simulate restart: create new expirer from same DB.
	expirer2 := NewHistoryExpirer(db, config, hp)
	if expirer2.EarliestBlock() != expectedEarliest {
		t.Errorf("expected earliest=%d after restart, got %d", expectedEarliest, expirer2.EarliestBlock())
	}
}

func TestHistoryExpiry_BatchLimit(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write blocks 1-200.
	hashes := make(map[uint64]types.Hash)
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 200; i++ {
			hashes[i] = writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  50,
		Interval:   10,
		BatchLimit: 30, // only delete 30 blocks per cycle
	}
	hp := &staticBlockProvider{block: 200}
	expirer := NewHistoryExpirer(db, config, hp)

	// First expiry cycle: should delete blocks 1-30 (batch limit).
	expirer.maybeExpire()

	if expirer.EarliestBlock() != 31 {
		t.Fatalf("expected earliestBlock=31, got %d", expirer.EarliestBlock())
	}

	// Blocks 31-200 should still exist.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		if blockDataExists(t, tx, 1, hashes[1]) {
			t.Error("block 1 should have been deleted")
		}
		if !blockDataExists(t, tx, 31, hashes[31]) {
			t.Error("block 31 should still exist")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExpiry_NoOpWhenDisabled(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write blocks 1-50.
	hashes := make(map[uint64]types.Hash)
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 50; i++ {
			hashes[i] = writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     false, // disabled
		Retention:  10,
		Interval:   5,
		BatchLimit: 10000,
	}

	if config.IsEnabled() {
		t.Fatal("config should report disabled")
	}

	// Verify nothing would be expired if we call maybeExpire directly.
	config.Enable = true
	hp := &staticBlockProvider{block: 0} // block 0 = no chain progress
	expirer := NewHistoryExpirer(db, config, hp)
	expirer.maybeExpire()

	if expirer.EarliestBlock() != 0 {
		t.Fatalf("expected earliestBlock=0 (no-op), got %d", expirer.EarliestBlock())
	}

	// All blocks should still exist.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		for i := uint64(1); i <= 50; i++ {
			if !blockDataExists(t, tx, i, hashes[i]) {
				t.Errorf("block %d should still exist", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExpiry_KeepsCanonicalHash(t *testing.T) {
	db := memdb.NewTestDB(t)

	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 100; i++ {
			writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  50,
		Interval:   10,
		BatchLimit: 10000,
	}
	hp := &staticBlockProvider{block: 100}
	expirer := NewHistoryExpirer(db, config, hp)
	expirer.maybeExpire()

	// Verify canonical hash entries are preserved for expired blocks.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		for i := uint64(1); i < 50; i++ {
			if !canonicalHashExists(t, tx, i) {
				t.Errorf("HeaderCanonical for block %d should be preserved", i)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExpiry_MultipleCycles(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Write blocks 1-300.
	hashes := make(map[uint64]types.Hash)
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		for i := uint64(1); i <= 300; i++ {
			hashes[i] = writeTestBlock(t, tx, i)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  50,
		Interval:   10,
		BatchLimit: 50, // small batch to force multiple cycles
	}
	hp := &staticBlockProvider{block: 200}
	expirer := NewHistoryExpirer(db, config, hp)

	// Cycle 1: expire blocks 1-50 (batch limited).
	expirer.maybeExpire()
	if expirer.EarliestBlock() != 51 {
		t.Fatalf("cycle 1: expected earliestBlock=51, got %d", expirer.EarliestBlock())
	}

	// Advance chain and run again.
	hp.block = 250
	expirer.maybeExpire()
	// Cycle 2: from=51, expireTo=200, batchEnd=101
	if expirer.EarliestBlock() != 101 {
		t.Fatalf("cycle 2: expected earliestBlock=101, got %d", expirer.EarliestBlock())
	}

	// Advance more.
	hp.block = 300
	expirer.maybeExpire()
	// Cycle 3: from=101, expireTo=250, batchEnd=151
	if expirer.EarliestBlock() != 151 {
		t.Fatalf("cycle 3: expected earliestBlock=151, got %d", expirer.EarliestBlock())
	}

	// Verify data integrity.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		// Block 100 should be deleted.
		if blockDataExists(t, tx, 100, hashes[100]) {
			t.Error("block 100 should be deleted after cycle 2")
		}
		// Block 200 should still exist.
		if !blockDataExists(t, tx, 200, hashes[200]) {
			t.Error("block 200 should still exist")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryExpiry_StartStop(t *testing.T) {
	db := memdb.NewTestDB(t)
	config := conf.HistoryExpiryConfig{
		Enable:     true,
		Retention:  100,
		Interval:   10,
		BatchLimit: 1000,
	}
	hp := &staticBlockProvider{block: 0}

	expirer := NewHistoryExpirer(db, config, hp)
	expirer.Start()
	expirer.Stop() // should not hang
}

func TestReadWriteEarliestBlock(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Initially should return 0.
	if err := db.View(testCtx(), func(tx kv.Tx) error {
		val, err := rawdb.ReadEarliestBlock(tx)
		if err != nil {
			return err
		}
		if val != 0 {
			t.Errorf("expected 0, got %d", val)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Write and read back.
	if err := db.Update(testCtx(), func(tx kv.RwTx) error {
		return rawdb.WriteEarliestBlock(tx, 12345)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(testCtx(), func(tx kv.Tx) error {
		val, err := rawdb.ReadEarliestBlock(tx)
		if err != nil {
			return err
		}
		if val != 12345 {
			t.Errorf("expected 12345, got %d", val)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
