// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// End-to-end integration test for the parallel commit pipeline.
// Builds a synthetic block of txs that exercise account writes,
// storage writes, SELFDESTRUCT wipes, and post-wipe writes; runs
// the full ExecuteBlockParallel → FinalizeBlock → BlockCommit.Apply
// path against a PlainStateBuffer; verifies the resulting buffer
// matches what the sequential equivalent would produce.
//
// This test is the state-layer analogue of the executor's forward-
// replay equivalence check — it catches regressions in the commit
// pipeline (Bug #1's pre-wipe filter, BlockCommit.Apply ordering,
// PlainStateBufferApplyTarget) without needing a real EVM or MDBX.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestParallelPipeline_DisjointTxsMatchBuffer: N txs write distinct
// storage slots. Parallel commit produces a buffer with every slot
// set, matching what sequential per-tx CommitBlock would yield.
func TestParallelPipeline_DisjointTxsMatchBuffer(t *testing.T) {
	const numTxs = 20
	base := NewMapBaseReader(nil)
	addr := mkAddr(0xaa)

	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			// Write a uniquely-tagged storage slot.
			slot := types.Hash{}
			slot[31] = byte(txIdx)
			ev := NewEVMStateView(v)
			ev.WriteStorage(addr, slot, uint256.NewInt(uint64(txIdx+100)))
			return nil
		}
	}

	outputs := make([]TxOutput, numTxs)
	results, mv, err := ExecuteBlockParallel(numTxs, 4, base, func(txIdx int) TxExecutor {
		inner := executor(txIdx)
		return func(v *MVStateView) error {
			err := inner(v)
			outputs[txIdx] = TxOutput{TxIdx: txIdx, GasUsed: 21000, Status: 1}
			return err
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d err: %v", r.TxIdx, r.Err)
		}
	}

	bc, err := FinalizeBlock(mv, outputs, types.Address{})
	if err != nil {
		t.Fatal(err)
	}

	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// Verify all 20 slots are in the buffer with expected values.
	slots := buf.storage[addr]
	if len(slots) != numTxs {
		t.Errorf("slot count: got %d want %d", len(slots), numTxs)
	}
	for i := 0; i < numTxs; i++ {
		slot := types.Hash{}
		slot[31] = byte(i)
		entry, ok := slots[slot]
		if !ok {
			t.Errorf("tx %d slot missing", i)
			continue
		}
		got := new(uint256.Int).SetBytes(entry.Bytes())
		if got.Uint64() != uint64(i+100) {
			t.Errorf("tx %d val: got %s want %d", i, got, i+100)
		}
	}

	// Gas aggregate matches.
	if bc.GasUsed != numTxs*21000 {
		t.Errorf("gas: got %d want %d", bc.GasUsed, numTxs*21000)
	}
}

// TestParallelPipeline_SelfdestructThenRewrite: tx 3 writes slot_a;
// tx 5 SELFDESTRUCT addr; tx 7 re-creates and writes slot_b.
// Expected buffer state: slot_a gone (wiped by tx 5 after Bug #1 fix),
// slot_b present (post-wipe write by tx 7), addr wiped-marked.
func TestParallelPipeline_SelfdestructThenRewrite(t *testing.T) {
	base := NewMapBaseReader(nil)
	addr := mkAddr(0x5d)
	slotA := mkHash(0xaa)
	slotB := mkHash(0xbb)

	outputs := make([]TxOutput, 10)
	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			ev := NewEVMStateView(v)
			switch txIdx {
			case 3:
				ev.WriteStorage(addr, slotA, uint256.NewInt(111))
			case 5:
				ev.WipeAddress(addr)
			case 7:
				ev.WriteStorage(addr, slotB, uint256.NewInt(999))
			}
			outputs[txIdx] = TxOutput{TxIdx: txIdx, GasUsed: 21000, Status: 1}
			return nil
		}
	}

	_, mv, err := ExecuteBlockParallel(10, 4, base, executor)
	if err != nil {
		t.Fatal(err)
	}

	bc, err := FinalizeBlock(mv, outputs, types.Address{})
	if err != nil {
		t.Fatal(err)
	}

	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	// Pre-populate the buffer as if slotA had an older base value.
	// The wipe should clear it from buf.storage via CreateContract.
	buf.storage[addr] = map[types.Hash]storageEntry{
		slotA: storageEntryFromBytes([]byte{0x42}),
	}

	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// slot_a must NOT be present (tx 5's wipe applied, tx 3 filtered).
	if slots, ok := buf.storage[addr]; ok {
		if _, present := slots[slotA]; present {
			t.Errorf("slot_a leaked through wipe (Bug #1 regression)")
		}
		// slot_b must be present.
		entry, ok := slots[slotB]
		if !ok {
			t.Errorf("slot_b missing")
		} else {
			got := new(uint256.Int).SetBytes(entry.Bytes())
			if got.Uint64() != 999 {
				t.Errorf("slot_b: got %s want 999", got.String())
			}
		}
	}

	// addr must be in wipedStorage (CreateContract ran).
	if _, ok := buf.wipedStorage[addr]; !ok {
		t.Errorf("addr not marked wiped")
	}
}

// TestParallelPipeline_MultipleCoinbaseWritesNotDoubled: simulate
// multiple txs updating the coinbase account; after commit the
// buffer must contain the LATEST coinbase balance (MVHashMap's highest-
// txIdx write) — NOT a doubled sum. Regression for Bug #2's side
// effect: if some future refactor re-introduced TxOutput.CoinbaseTip,
// this test would fail by seeing doubled balance.
func TestParallelPipeline_MultipleCoinbaseWritesNotDoubled(t *testing.T) {
	base := NewMapBaseReader(nil)
	coinbase := mkAddr(0xcb)

	outputs := make([]TxOutput, 5)
	// Each tx writes an account for coinbase with Balance=i*100.
	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			ev := NewEVMStateView(v)
			var a account.StateAccount
			a.Initialised = true
			a.Nonce = uint64(txIdx)
			a.Balance.SetUint64(uint64(txIdx * 100))
			ev.WriteAccount(coinbase, &a)
			// DO NOT set CoinbaseTip — Bug #2 fix contract.
			outputs[txIdx] = TxOutput{TxIdx: txIdx, GasUsed: 21000, Status: 1}
			return nil
		}
	}

	_, mv, err := ExecuteBlockParallel(5, 4, base, executor)
	if err != nil {
		t.Fatal(err)
	}

	bc, err := FinalizeBlock(mv, outputs, coinbase)
	if err != nil {
		t.Fatal(err)
	}
	// BlockCommit.Apply must NOT receive a non-zero CoinbaseDelta.
	if bc.CoinbaseDelta != nil && !bc.CoinbaseDelta.IsZero() {
		t.Fatalf("CoinbaseDelta=%s (non-zero; Bug #2 regression)", bc.CoinbaseDelta)
	}

	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// Final balance should be tx 4's value (highest-txIdx), which is
	// 400 — NOT 0+100+200+300+400=1000 (doubled sum).
	raw, ok := buf.accounts[coinbase]
	if !ok {
		t.Fatal("coinbase account missing")
	}
	var acct account.StateAccount
	if err := acct.DecodeForStorage(raw); err != nil {
		t.Fatal(err)
	}
	if acct.Balance.Uint64() != 400 {
		t.Errorf("coinbase balance: got %s want 400", acct.Balance.String())
	}
	if acct.Nonce != 4 {
		t.Errorf("coinbase nonce: got %d want 4", acct.Nonce)
	}
}
