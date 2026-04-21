// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func mkAddr(b byte) types.Address {
	var a types.Address
	for i := range a {
		a[i] = b
	}
	return a
}

func mkHash(b byte) types.Hash {
	var h types.Hash
	for i := range h {
		h[i] = b
	}
	return h
}

func TestFinalizeBlock_Empty(t *testing.T) {
	mv := NewMVHashMap(16)
	coinbase := mkAddr(0xff)
	bc, err := FinalizeBlock(mv, nil, coinbase)
	if err != nil {
		t.Fatal(err)
	}
	if len(bc.Writes) != 0 {
		t.Errorf("writes: %+v", bc.Writes)
	}
	if bc.GasUsed != 0 {
		t.Errorf("gasUsed: %d", bc.GasUsed)
	}
	if !bc.CoinbaseDelta.IsZero() {
		t.Errorf("coinbaseDelta: %s", bc.CoinbaseDelta.String())
	}
}

func TestFinalizeBlock_GasAggregation(t *testing.T) {
	mv := NewMVHashMap(16)
	outs := []TxOutput{
		{TxIdx: 0, GasUsed: 21000, Status: 1},
		{TxIdx: 1, GasUsed: 50000, Status: 1},
		{TxIdx: 2, GasUsed: 30000, Status: 0, Err: nil}, // reverted still charges
	}
	bc, err := FinalizeBlock(mv, outs, types.Address{})
	if err != nil {
		t.Fatal(err)
	}
	if bc.GasUsed != 101000 {
		t.Errorf("GasUsed=%d want 101000", bc.GasUsed)
	}
	if len(bc.Receipts) != 3 {
		t.Fatalf("receipts: %d", len(bc.Receipts))
	}
	// CumulativeGasUsed monotonic.
	if bc.Receipts[0].CumulativeGasUsed != 21000 {
		t.Errorf("r0 cum: %d", bc.Receipts[0].CumulativeGasUsed)
	}
	if bc.Receipts[1].CumulativeGasUsed != 71000 {
		t.Errorf("r1 cum: %d", bc.Receipts[1].CumulativeGasUsed)
	}
	if bc.Receipts[2].CumulativeGasUsed != 101000 {
		t.Errorf("r2 cum: %d", bc.Receipts[2].CumulativeGasUsed)
	}
}

func TestFinalizeBlock_CoinbaseAggregation(t *testing.T) {
	mv := NewMVHashMap(16)
	coinbase := mkAddr(0xcb)
	outs := []TxOutput{
		{TxIdx: 0, GasUsed: 21000, CoinbaseTip: uint256.NewInt(100), Status: 1},
		{TxIdx: 1, GasUsed: 21000, CoinbaseTip: uint256.NewInt(250), Status: 1},
		{TxIdx: 2, GasUsed: 21000, CoinbaseTip: uint256.NewInt(50), Status: 1},
	}
	bc, err := FinalizeBlock(mv, outs, coinbase)
	if err != nil {
		t.Fatal(err)
	}
	if bc.CoinbaseDelta.Uint64() != 400 {
		t.Errorf("coinbase delta: %d want 400", bc.CoinbaseDelta.Uint64())
	}
	if bc.CoinbaseAddress != coinbase {
		t.Errorf("coinbase addr mismatch")
	}
}

func TestFinalizeBlock_LogIndices(t *testing.T) {
	mv := NewMVHashMap(16)
	addrA := mkAddr(0xa)
	addrB := mkAddr(0xb)
	outs := []TxOutput{
		{TxIdx: 0, GasUsed: 21000, Logs: []Log{
			{Address: addrA, Data: []byte("tx0-log0")},
			{Address: addrA, Data: []byte("tx0-log1")},
		}, Status: 1},
		{TxIdx: 1, GasUsed: 21000, Logs: nil, Status: 1}, // no logs
		{TxIdx: 2, GasUsed: 21000, Logs: []Log{
			{Address: addrB, Data: []byte("tx2-log0")},
		}, Status: 1},
	}
	bc, err := FinalizeBlock(mv, outs, types.Address{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bc.Logs) != 3 {
		t.Fatalf("logs: %d", len(bc.Logs))
	}
	// Global indices 0, 1, 2.
	wantAddrs := []types.Address{addrA, addrA, addrB}
	for i, l := range bc.Logs {
		if l.Index != uint(i) {
			t.Errorf("log[%d].Index=%d want %d", i, l.Index, i)
		}
		if l.Address != wantAddrs[i] {
			t.Errorf("log[%d].Address mismatch", i)
		}
	}
	// tx 0's logs have TxIndex=0.
	if bc.Logs[0].TxIndex != 0 || bc.Logs[1].TxIndex != 0 {
		t.Errorf("tx 0 log TxIndex: %d,%d", bc.Logs[0].TxIndex, bc.Logs[1].TxIndex)
	}
	if bc.Logs[2].TxIndex != 2 {
		t.Errorf("tx 2 log TxIndex: %d", bc.Logs[2].TxIndex)
	}
	// Receipt log ranges.
	if bc.Receipts[0].FirstLogIndex != 0 || bc.Receipts[0].LastLogIndex != 2 {
		t.Errorf("r0 log range: [%d, %d)", bc.Receipts[0].FirstLogIndex, bc.Receipts[0].LastLogIndex)
	}
	if bc.Receipts[1].FirstLogIndex != 2 || bc.Receipts[1].LastLogIndex != 2 {
		t.Errorf("r1 (no logs): [%d, %d)", bc.Receipts[1].FirstLogIndex, bc.Receipts[1].LastLogIndex)
	}
	if bc.Receipts[2].FirstLogIndex != 2 || bc.Receipts[2].LastLogIndex != 3 {
		t.Errorf("r2: [%d, %d)", bc.Receipts[2].FirstLogIndex, bc.Receipts[2].LastLogIndex)
	}
}

func TestFinalizeBlock_TxIdxMismatchError(t *testing.T) {
	outs := []TxOutput{
		{TxIdx: 0, GasUsed: 1},
		{TxIdx: 2, GasUsed: 1}, // wrong: should be 1
	}
	_, err := FinalizeBlock(NewMVHashMap(16), outs, types.Address{})
	if err == nil {
		t.Error("expected TxIdx mismatch error")
	}
}

func TestBlockCommit_ApplyAccountAndStorage(t *testing.T) {
	mv := NewMVHashMap(16)
	addr := mkAddr(0x1)
	slot := mkHash(0x2)

	// Tx 0 writes an account and a storage slot via EVMStateView,
	// then flushes into MVHashMap.
	base := NewMapBaseReader(nil)
	view := NewEVMStateView(NewMVStateView(mv, base, 0, 0))
	view.WriteAccount(addr, &account.StateAccount{
		Nonce:       5,
		Balance:     *uint256.NewInt(1000),
		Initialised: true,
	})
	view.WriteStorage(addr, slot, uint256.NewInt(42))
	view.Inner().FlushWrites()

	bc, err := FinalizeBlock(mv, []TxOutput{{TxIdx: 0, GasUsed: 21000, Status: 1}},
		types.Address{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bc.Writes) != 2 {
		t.Fatalf("writes: %d want 2", len(bc.Writes))
	}

	target := NewMapApplyTarget()
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// Verify account.
	enc, ok := target.Accounts[addr]
	if !ok {
		t.Fatal("account not present")
	}
	a, err := DecodeAccountValue(enc)
	if err != nil {
		t.Fatal(err)
	}
	if a.Nonce != 5 || a.Balance.Uint64() != 1000 {
		t.Errorf("applied account: nonce=%d balance=%d", a.Nonce, a.Balance.Uint64())
	}

	// Verify storage.
	got := target.Storage[addr][slot]
	want := uint256.NewInt(42).Bytes()
	if !bytes.Equal(got, want) {
		t.Errorf("storage: %x want %x", got, want)
	}
}

func TestBlockCommit_ApplyCoinbaseTip(t *testing.T) {
	mv := NewMVHashMap(16)
	coinbase := mkAddr(0xcb)
	outs := []TxOutput{
		{TxIdx: 0, GasUsed: 21000, CoinbaseTip: uint256.NewInt(500)},
	}
	bc, _ := FinalizeBlock(mv, outs, coinbase)

	target := NewMapApplyTarget()
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	bal := target.Balance[coinbase]
	if bal == nil || bal.Uint64() != 500 {
		t.Errorf("coinbase balance: %v", bal)
	}
}

func TestBlockCommit_WritesSortedByKey(t *testing.T) {
	// Deterministic write order matters for MDBX page locality.
	mv := NewMVHashMap(16)
	// Write in reverse-ish order; FinalizeBlock should sort.
	for _, b := range []byte{0x09, 0x01, 0x07, 0x03, 0x05} {
		mv.Write([]byte{b}, Version{TxIdx: 0}, []byte{b * 10})
	}
	bc, _ := FinalizeBlock(mv, []TxOutput{{TxIdx: 0, GasUsed: 21000}},
		types.Address{})
	if len(bc.Writes) != 5 {
		t.Fatalf("writes: %d", len(bc.Writes))
	}
	for i := 1; i < len(bc.Writes); i++ {
		if bytes.Compare(bc.Writes[i-1].Key, bc.Writes[i].Key) >= 0 {
			t.Errorf("writes[%d]=%x NOT sorted before writes[%d]=%x",
				i-1, bc.Writes[i-1].Key, i, bc.Writes[i].Key)
		}
	}
}

// TestEndToEnd wires the full parallel path:
//   ExecuteBlockParallel -> FinalizeBlock -> Apply
// on a small workload, verifying the applied state matches the
// sequential expectation.
func TestEndToEnd_ParallelCommit(t *testing.T) {
	const numTxs = 10
	base := NewMapBaseReader(nil)

	// Each tx writes its own account + a shared counter slot to
	// force some rewinds.
	counterAddr := mkAddr(0xcc)
	counterSlot := mkHash(0x01)

	executor := func(txIdx int) TxExecutor {
		return func(v *MVStateView) error {
			// Disjoint write: per-tx account.
			selfKey := EncodeAccountKey(mkAddr(byte(txIdx + 1)))
			v.Set(selfKey, []byte{byte(txIdx)})

			// Contention: read+write counter slot.
			ck := EncodeStorageKey(counterAddr, counterSlot)
			cur, err := v.Get(ck)
			if err != nil {
				return err
			}
			if v.AbortPending() {
				return nil
			}
			n := byte(0)
			if len(cur) > 0 {
				n = cur[0]
			}
			v.Set(ck, []byte{n + 1})
			return nil
		}
	}

	_, mv, err := ExecuteBlockParallel(numTxs, 4, base, executor)
	if err != nil {
		t.Fatal(err)
	}

	// Fake TxOutputs: all successful, 21000 gas each, no tips/logs.
	outs := make([]TxOutput, numTxs)
	for i := range outs {
		outs[i] = TxOutput{TxIdx: i, GasUsed: 21000, Status: 1}
	}

	bc, err := FinalizeBlock(mv, outs, mkAddr(0xff))
	if err != nil {
		t.Fatal(err)
	}
	if bc.GasUsed != 21000*numTxs {
		t.Errorf("gasUsed: %d", bc.GasUsed)
	}

	target := NewMapApplyTarget()
	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	// Counter slot should be == numTxs (Block-STM preserves sequential semantics).
	slots := target.Storage[counterAddr]
	if slots == nil {
		t.Fatal("counter storage missing")
	}
	val := slots[counterSlot]
	if len(val) != 1 || val[0] != numTxs {
		t.Errorf("counter: got %v want [%d]", val, numTxs)
	}

	// Each tx's own account should be present.
	for i := 0; i < numTxs; i++ {
		addr := mkAddr(byte(i + 1))
		v, ok := target.Accounts[addr]
		if !ok {
			t.Errorf("tx %d account missing", i)
			continue
		}
		if len(v) != 1 || v[0] != byte(i) {
			t.Errorf("tx %d account: got %v want [%d]", i, v, i)
		}
	}
}
