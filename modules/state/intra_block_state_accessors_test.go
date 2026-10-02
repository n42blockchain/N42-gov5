// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// TestIntraBlockStateSimpleAccessors sweeps the small getter/setter surface
// of IntraBlockState that pure logic tests otherwise never touch: root
// computer plumbing, snapshot bookkeeping, tracer hooks, refund counter,
// logs, code queries, HasSuicided/Suicide aliases, and SetStorage.
func TestIntraBlockStateSimpleAccessors(t *testing.T) {
	base := &countingReader{}
	sdb := New(base)

	// Root computer plumbing: nil until set.
	if sdb.HasRootComputer() {
		t.Fatal("HasRootComputer true with no computer set")
	}
	if sdb.GetRootComputer() != nil {
		t.Fatal("GetRootComputer non-nil with no computer set")
	}
	if got := sdb.LtHashRoot(); got != (types.Hash{}) {
		t.Fatalf("LtHashRoot = %x, want zero", got)
	}

	// Height.
	sdb.SetHeight(42)

	// Snapshot bookkeeping: before BeginWriteSnapshot, Snap()/SetOutHash/
	// WrittenSnapshot/SetGetOneFun are all safe no-ops.
	if sdb.Snap() != nil {
		t.Fatal("Snap() non-nil before BeginWriteSnapshot")
	}
	sdb.SetOutHash(types.Hash{0x01}) // no-op, snap is nil
	if got := sdb.WrittenSnapshot(types.Hash{0x01}); got != nil {
		t.Fatal("WrittenSnapshot non-nil with no active snapshot")
	}
	sdb.SetGetOneFun(nil) // no-op, snap is nil

	sdb.BeginWriteSnapshot()
	if sdb.Snap() == nil {
		t.Fatal("Snap() nil after BeginWriteSnapshot")
	}
	sdb.SetOutHash(types.Hash{0x02})
	encoded := sdb.WrittenSnapshot(types.Hash{0x03})
	if len(encoded) == 0 {
		t.Fatal("WrittenSnapshot returned no bytes for an active snapshot")
	}
	if sdb.Snap() != nil {
		t.Fatal("Snap() should be nil after WrittenSnapshot consumes it")
	}

	// BeginWriteCodes + CodeHashes (empty, no dirty code yet).
	sdb.BeginWriteCodes()
	if got := sdb.CodeHashes(); len(got) != 0 {
		t.Fatalf("CodeHashes = %v, want empty", got)
	}

	// Tracer hooks: no tracer installed is a safe no-op through public API.
	sdb.SetTracer(nil)
	sdb.SetTrace(true)
	sdb.SetTrace(false)

	// Error plumbing.
	if sdb.Error() != nil {
		t.Fatal("Error() non-nil on fresh state")
	}

	// StateReader plumbing.
	sdb.SetStateReader(base)
	if sdb.GetStateReader() != StateReader(base) {
		t.Fatal("GetStateReader did not return the installed reader")
	}

	// TxIndex reflects Prepare.
	sdb.Prepare(types.Hash{0x10}, types.Hash{0x11}, 3)
	if sdb.TxIndex() != 3 {
		t.Fatalf("TxIndex = %d, want 3", sdb.TxIndex())
	}

	// Logs: AddLog stamps tx/block hash, index, and size; GetLogs/Logs read
	// them back.
	sdb.AddLog(&block.Log{Address: types.Address{0x20}})
	sdb.AddLog(&block.Log{Address: types.Address{0x21}})
	logs := sdb.GetLogs(types.Hash{0x10})
	if len(logs) != 2 {
		t.Fatalf("GetLogs = %d, want 2", len(logs))
	}
	if logs[0].TxIndex != 3 || logs[1].Index != 1 {
		t.Fatalf("log metadata mismatch: %+v / %+v", logs[0], logs[1])
	}
	if got := sdb.Logs(); len(got) != 2 {
		t.Fatalf("Logs() = %d, want 2", len(got))
	}

	// Refund counter.
	sdb.AddRefund(100)
	sdb.AddRefund(50)
	if got := sdb.GetRefund(); got != 150 {
		t.Fatalf("GetRefund = %d, want 150", got)
	}
	sdb.SubRefund(30)
	if got := sdb.GetRefund(); got != 120 {
		t.Fatalf("GetRefund after SubRefund = %d, want 120", got)
	}
	sdb.SubRefund(1000) // underflow sets an error, does not panic
	if sdb.Error() == nil {
		t.Fatal("expected an error after SubRefund underflow")
	}

	// Code / suicide / storage on a created account.
	var addr types.Address
	addr[0] = 0x30
	sdb.CreateAccount(addr, false)
	sdb.SetCode(addr, []byte{0x60, 0x01})
	if size := sdb.GetCodeSize(addr); size != 2 {
		t.Fatalf("GetCodeSize = %d, want 2", size)
	}
	if h := sdb.GetCodeHash(addr); h == (types.Hash{}) {
		t.Fatal("GetCodeHash = zero for an account with code")
	}

	if sdb.HasSuicided(addr) {
		t.Fatal("HasSuicided true before Suicide/Selfdestruct")
	}
	storage := Storage{types.Hash{0x40}: *uint256.NewInt(7)}
	sdb.SetStorage(addr, storage)
	var got uint256.Int
	h := types.Hash{0x40}
	sdb.GetState(addr, &h, &got)
	if got.Uint64() != 7 {
		t.Fatalf("GetState after SetStorage = %d, want 7", got.Uint64())
	}

	if !sdb.Suicide(addr) {
		t.Fatal("Suicide() returned false for an existing account")
	}
	if !sdb.HasSuicided(addr) {
		t.Fatal("HasSuicided false after Suicide")
	}

	// DirtyAddresses is exercised for coverage; dirtiness is only recorded
	// as of FinalizeTx/IntermediateRoot, so an empty result mid-transaction
	// is valid.
	_ = sdb.DirtyAddresses()

	// SetDiscardBlockChanges is a plain flag flip, exercised for coverage.
	sdb.SetDiscardBlockChanges(true)
	sdb.SetDiscardBlockChanges(false)
}
