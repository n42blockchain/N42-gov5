// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the BALCapture code-change path, DeleteAccount/CreateContract
// delegation, and the blockTxHashes/TxHashes helpers that are not exercised
// by bal_capture_test.go's hash-agreement suite.

package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// recordingWriter wraps state.NewNoopWriter's behavior (all ops succeed) but
// records which delegated calls it saw, so tests can assert BALCapture always
// delegates to the inner writer unchanged, per its "pure observer" contract.
type recordingWriter struct {
	state.StateWriter
	codeCalls   int
	deleteCalls int
	createCalls int
	failDelete  error
	failCreate  error
}

func (w *recordingWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	w.codeCalls++
	return nil
}

func (w *recordingWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	w.deleteCalls++
	return w.failDelete
}

func (w *recordingWriter) CreateContract(address types.Address) error {
	w.createCalls++
	return w.failCreate
}

func TestBALCaptureUpdateAccountCodeRecordsNonEmptyCode(t *testing.T) {
	inner := &recordingWriter{}
	c := NewBALCapture(inner)
	addr := balAddr(0x01)

	// No current tx bucket: delegate only, nothing recorded.
	if err := c.UpdateAccountCode(addr, types.Hash{}, []byte{0x60, 0x60}); err != nil {
		t.Fatal(err)
	}
	if inner.codeCalls != 1 {
		t.Fatalf("inner.UpdateAccountCode calls = %d, want 1", inner.codeCalls)
	}

	c.BeginTx(balSlot(0xa1))
	if err := c.UpdateAccountCode(addr, types.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.cur.CodeChanges) != 0 {
		t.Fatalf("empty code was recorded as a code change: %+v", c.cur.CodeChanges)
	}
	code := []byte{0x60, 0x80, 0x60, 0x40}
	if err := c.UpdateAccountCode(addr, types.Hash{}, code); err != nil {
		t.Fatal(err)
	}
	if len(c.cur.CodeChanges) != 1 || c.cur.CodeChanges[0].Address != addr {
		t.Fatalf("UpdateAccountCode did not record the code change: %+v", c.cur.CodeChanges)
	}
	if inner.codeCalls != 3 {
		t.Fatalf("inner.UpdateAccountCode calls = %d, want 3 (always delegated)", inner.codeCalls)
	}
}

func TestBALCaptureDeleteAccountDelegatesWithoutRecording(t *testing.T) {
	inner := &recordingWriter{}
	c := NewBALCapture(inner)
	addr := balAddr(0x02)
	c.BeginTx(balSlot(0xb2))
	if err := c.DeleteAccount(addr, nil); err != nil {
		t.Fatal(err)
	}
	if inner.deleteCalls != 1 {
		t.Fatalf("inner.DeleteAccount calls = %d, want 1", inner.deleteCalls)
	}
	// DeleteAccount deliberately emits no balance/nonce change (phase-1 BAL
	// model represents deletion by absence).
	if len(c.cur.BalanceChanges) != 0 || len(c.cur.NonceChanges) != 0 {
		t.Fatalf("DeleteAccount unexpectedly recorded a balance/nonce change")
	}

	inner.failDelete = errors.New("boom")
	if err := c.DeleteAccount(addr, nil); err == nil {
		t.Fatal("DeleteAccount swallowed the inner writer's error")
	}
}

func TestBALCaptureCreateContractDelegates(t *testing.T) {
	inner := &recordingWriter{}
	c := NewBALCapture(inner)
	addr := balAddr(0x03)
	if err := c.CreateContract(addr); err != nil {
		t.Fatal(err)
	}
	if inner.createCalls != 1 {
		t.Fatalf("inner.CreateContract calls = %d, want 1", inner.createCalls)
	}
	inner.failCreate = errors.New("boom")
	if err := c.CreateContract(addr); err == nil {
		t.Fatal("CreateContract swallowed the inner writer's error")
	}
}

func TestBlockTxHashesMatchesBlockOrder(t *testing.T) {
	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	tx1 := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	tx2 := transaction.NewTransaction(1, from, &to, uint256.NewInt(2), 21000, uint256.NewInt(1), nil)
	b := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
	}, []*transaction.Transaction{tx1, tx2}).(*block.Block)

	got := blockTxHashes(b)
	if len(got) != 2 || got[0] != tx1.Hash() || got[1] != tx2.Hash() {
		t.Fatalf("blockTxHashes() = %v, want [%s %s]", got, tx1.Hash().Hex(), tx2.Hash().Hex())
	}

	empty := block.NewBlock(&block.Header{Number: uint256.NewInt(2), Difficulty: uint256.NewInt(1)}, nil).(*block.Block)
	if got := blockTxHashes(empty); len(got) != 0 {
		t.Fatalf("blockTxHashes() on an empty block = %v, want empty", got)
	}
}

func TestTxHashesMatchesInputOrder(t *testing.T) {
	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	tx1 := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	tx2 := transaction.NewTransaction(1, from, &to, uint256.NewInt(2), 21000, uint256.NewInt(1), nil)

	got := TxHashes([]*transaction.Transaction{tx1, tx2})
	if len(got) != 2 || got[0] != tx1.Hash() || got[1] != tx2.Hash() {
		t.Fatalf("TxHashes() = %v, want [%s %s]", got, tx1.Hash().Hex(), tx2.Hash().Hex())
	}
	if got := TxHashes(nil); len(got) != 0 {
		t.Fatalf("TxHashes(nil) = %v, want empty", got)
	}
}
