// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// slotReader serves persisted storage per slot, and is deliberately NOT a
// StorageEnumerator — that is the shape of the witness-backed replay reader,
// which is where HasNonEmptyStorage has to fall back on its in-memory record.
type slotReader struct {
	slots map[types.Hash][]byte
}

func (slotReader) ReadAccountData(types.Address) (*account.StateAccount, error) {
	a := account.NewAccount()
	return &a, nil
}

func (r slotReader) ReadAccountStorage(_ types.Address, key *types.Hash) ([]byte, error) {
	return r.slots[*key], nil
}

func (slotReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (slotReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

// HasNonEmptyStorage decides the EIP-7610 CREATE collision check
// (internal/vm/evm.go: IsParis && HasNonEmptyStorage). It must give the same
// answer whether or not the caller asked for a block write set — that is a
// bookkeeping choice, not a consensus parameter.
//
// The case that separates the two: a slot that held a non-zero value at the
// start of the block and was zeroed by an earlier transaction in the same
// block. updateTrie overwrites originStorage with the new (zero) value, so the
// block-start fact survives only in the object's record of committed reads.
// The slot deliberately is not 0 or 1, because the non-enumerating fallback
// probes exactly those two and would otherwise mask the difference — real
// contract storage (mappings, arrays) lives at hashed slots.
func TestHasNonEmptyStorageIgnoresDiscardBlockChanges(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	slot := types.HexToHash("0x42")

	run := func(discard bool) bool {
		sdb := New(slotReader{slots: map[types.Hash][]byte{slot: {7}}})
		sdb.SetDiscardBlockChanges(discard)

		// Transaction 1 reads the slot, then zeroes it and finalizes.
		var got uint256.Int
		sdb.GetState(addr, &slot, &got)
		if got.Uint64() != 7 {
			t.Fatalf("discard=%v: committed read = %d, want 7", discard, got.Uint64())
		}
		sdb.SetState(addr, &slot, *uint256.NewInt(0))
		if err := sdb.FinalizeTx(&params.Rules{}, NewNoopWriter()); err != nil {
			t.Fatalf("discard=%v: FinalizeTx: %v", discard, err)
		}

		// Transaction 2 asks the question CREATE asks.
		return sdb.HasNonEmptyStorage(addr)
	}

	withWriteSet := run(false)
	validationOnly := run(true)
	if !withWriteSet {
		t.Fatal("baseline: slot was non-zero at block start, want HasNonEmptyStorage=true")
	}
	if validationOnly != withWriteSet {
		t.Fatalf("discardBlockChanges changed a consensus answer: validation-only=%v, with-write-set=%v",
			validationOnly, withWriteSet)
	}
}
