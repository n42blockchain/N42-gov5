// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestG52InstrumentedReaderLogStatsAndReset drives LogStats (a thin
// log.Debug wrapper over Stats) and Reset (zeroing every counter) on
// InstrumentedReader, neither reached by any existing test.
func TestG52InstrumentedReaderLogStatsAndReset(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		r := NewInstrumentedReader(NewPlainStateReader(tx), true)
		_, _ = r.ReadAccountData(types.Address{})

		if stats := r.Stats(); stats.ReadAccountCount == 0 {
			t.Fatal("expected at least one recorded account read before LogStats/Reset")
		}
		r.LogStats() // must not panic; output isn't asserted

		r.Reset()
		if stats := r.Stats(); stats.ReadAccountCount != 0 {
			t.Fatalf("Reset should zero counters, got ReadAccountCount=%d", stats.ReadAccountCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52InstrumentedWriterLogStatsAndReset mirrors the reader test for
// InstrumentedWriter.
func TestG52InstrumentedWriterLogStatsAndReset(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		inner := NewPlainStateWriter(tx, tx, 1)
		w := NewInstrumentedWriter(inner, true)

		orig := account.NewAccount()
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Nonce = 1
		if err := w.UpdateAccountData(types.HexToAddress("0x00000000000000000000000000000000000000eb"), &orig, &acc); err != nil {
			return err
		}

		if stats := w.Stats(); stats.UpdateAccountCount == 0 {
			t.Fatal("expected at least one recorded account update before LogStats/Reset")
		}
		w.LogStats() // must not panic

		w.Reset()
		if stats := w.Stats(); stats.UpdateAccountCount != 0 {
			t.Fatalf("Reset should zero counters, got UpdateAccountCount=%d", stats.UpdateAccountCount)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52StateObjectReturnGas is a smoke test for the documented no-op:
// it must accept any *big.Int, including nil, without side effects.
func TestG52StateObjectReturnGas(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		ibs := New(NewPlainStateReader(tx))
		addr := types.HexToAddress("0x00000000000000000000000000000000000000ec")
		data := account.NewAccount()
		orig := account.NewAccount()
		so := newObject(ibs, addr, &data, &orig)
		so.ReturnGas(big.NewInt(21000))
		so.ReturnGas(nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestG52BufferSnapshotStatsAndBufferStats covers both Stats methods: the
// flushed BufferSnapshot's read-only view and the live PlainStateBuffer's
// write-buffer cardinalities.
func TestG52BufferSnapshotStatsAndBufferStats(t *testing.T) {
	buf := NewPlainStateBuffer()
	w := NewBufferedPlainStateWriterNoHistory(buf)

	addr := types.HexToAddress("0x00000000000000000000000000000000000000ed")
	orig := account.NewAccount()
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 1
	if err := w.UpdateAccountData(addr, &orig, &acc); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(addr, types.HexToHash("0x01"), uint256.Int{}, *uint256.NewInt(1)); err != nil {
		t.Fatal(err)
	}

	if accounts, storage := buf.Stats(); accounts != 1 || storage != 1 {
		t.Fatalf("PlainStateBuffer.Stats() = (%d,%d), want (1,1)", accounts, storage)
	}

	snap := buf.SnapshotForFlush()
	if snap == nil {
		t.Fatal("SnapshotForFlush returned nil")
	}
	if accounts, storage := snap.Stats(); accounts != 1 || storage != 1 {
		t.Fatalf("BufferSnapshot.Stats() = (%d,%d), want (1,1)", accounts, storage)
	}
}

// TestG52BufferedPlainStateReaderAuditStorageLRU exercises auditStorageLRU's
// agree/disagree paths directly. It is unconditional (unlike auditNilReturn)
// so no env var is needed to drive it; only the log side effect is skipped
// because we never call ensureAuditLog's file open path is exercised but not
// asserted (auditWrite is safe to call repeatedly in tests).
func TestG52BufferedPlainStateReaderAuditStorageLRU(t *testing.T) {
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		addr := types.HexToAddress("0x00000000000000000000000000000000000000ee")
		slot := types.HexToHash("0x02")
		compositeKey := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes())
		if err := tx.Put(modules.Storage, compositeKey, []byte{0x07}); err != nil {
			return err
		}

		buf := NewPlainStateBuffer()
		r := NewBufferedPlainStateReader(buf, tx)

		var ck [storageCompositeKeyLen]byte
		copy(ck[:], compositeKey)

		// Agreeing values: no divergence, must not panic.
		r.auditStorageLRU(addr, slot, ck, []byte{0x07})
		// Disagreeing values: logs a divergence, must not panic or error.
		r.auditStorageLRU(addr, slot, ck, []byte{0x08})
		// Both "zero": nil vs MDBX-zero equivalents, must not panic.
		if err := tx.Delete(modules.Storage, compositeKey); err != nil {
			return err
		}
		r.auditStorageLRU(addr, slot, ck, nil)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
