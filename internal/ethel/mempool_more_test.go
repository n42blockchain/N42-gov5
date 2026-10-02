// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestMemoryTxPoolStopIsNoop(t *testing.T) {
	pool := NewMemoryTxPool()
	if err := pool.Stop(); err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}
}

func TestMemoryTxPoolAddLocalRejectsNilAndSenderless(t *testing.T) {
	pool := NewMemoryTxPool()
	if err := pool.AddLocal(nil); err == nil {
		t.Fatal("expected error for nil transaction")
	}
	// A round-tripped transaction has its explicit sender dropped and no
	// signature to recover one from, so From() is nil.
	built := transaction.NewTransaction(0, types.HexToAddress("0xabcd"), nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	raw, err := transaction.EncodeEthereumTransaction(built)
	if err != nil {
		t.Fatal(err)
	}
	senderless, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.AddLocal(senderless); err == nil {
		t.Fatal("expected error for transaction without recoverable sender")
	}
}

func TestMemoryTxPoolAddLocalDuplicateHashIsIgnored(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	if err := pool.AddLocal(tx); err != nil {
		t.Fatal(err)
	}
	if err := pool.AddLocal(tx); err != nil {
		t.Fatalf("re-adding the same transaction should be a no-op, got %v", err)
	}
	if _, count, _, _ := pool.Stats(); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestMemoryTxPoolGetTransactionAndGetTx(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx0 := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	tx1 := transaction.NewTransaction(1, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	if err := pool.AddLocal(tx0); err != nil {
		t.Fatal(err)
	}
	if err := pool.AddLocal(tx1); err != nil {
		t.Fatal(err)
	}

	flat, err := pool.GetTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != 2 {
		t.Fatalf("GetTransaction() returned %d txs, want 2", len(flat))
	}

	if got := pool.GetTx(tx0.Hash()); got == nil || got.Hash() != tx0.Hash() {
		t.Fatalf("GetTx(tx0) = %v, want tx0", got)
	}
	if got := pool.GetTx(types.HexToHash("0xdeadbeef")); got != nil {
		t.Fatalf("GetTx(unknown) = %v, want nil", got)
	}
}

func TestMemoryTxPoolAddRemotesDelegatesToAddLocals(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	txs := []*transaction.Transaction{
		transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil),
		nil, // exercises the nil-tx error path through AddLocals
	}
	errs := pool.AddRemotes(txs)
	if len(errs) != 2 {
		t.Fatalf("errs len = %d, want 2", len(errs))
	}
	if errs[0] != nil {
		t.Fatalf("errs[0] = %v, want nil", errs[0])
	}
	if errs[1] == nil {
		t.Fatal("errs[1] = nil, want error for nil transaction")
	}
}

func TestMemoryTxPoolAddLocalsReturnsPerTxErrors(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	errs := pool.AddLocals([]*transaction.Transaction{tx, tx})
	if errs[0] != nil {
		t.Fatalf("first add errs[0] = %v, want nil", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("duplicate add errs[1] = %v, want nil (dedup, not error)", errs[1])
	}
}

func TestMemoryTxPoolContentReturnsPendingAndEmptyQueued(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	if err := pool.AddLocal(tx); err != nil {
		t.Fatal(err)
	}
	pending, queued := pool.Content()
	if len(pending[from]) != 1 {
		t.Fatalf("pending[from] = %v, want 1 entry", pending[from])
	}
	if len(queued) != 0 {
		t.Fatalf("queued = %v, want empty map", queued)
	}
}

// TestMemoryTxPoolRemoveCanonicalFallsBackToHashRemoval exercises
// removeHashLocked: RemoveCanonical is given a canonical tx whose sender is
// nil and which was never pooled (so the pooled-sender lookup also misses),
// landing in the direct removeHashLocked(hash) path, which is then a no-op
// since byHash[hash] is nil.
func TestMemoryTxPoolRemoveCanonicalFallsBackToHashRemoval(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	if err := pool.AddLocal(tx); err != nil {
		t.Fatal(err)
	}

	raw, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the hash so it's not found in byHash either (simulate an
	// unrelated canonical tx with no sender and no pooled match).
	other := types.HexToAddress("0x9999")
	otherTx := transaction.NewTransaction(5, other, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	raw2, err := transaction.EncodeEthereumTransaction(otherTx)
	if err != nil {
		t.Fatal(err)
	}
	unknownOther, err := transaction.DecodeEthereumTransaction(raw2)
	if err != nil {
		t.Fatal(err)
	}

	pool.RemoveCanonical([]*transaction.Transaction{unknown, unknownOther})

	if pool.Has(tx.Hash()) {
		t.Fatal("tx should have been removed via the pooled-sender lookup")
	}
	if _, count, _, _ := pool.Stats(); count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

// TestMemoryTxPoolRemoveHashLockedDirect exercises removeHashLocked's full
// body directly. RemoveCanonical can only reach removeHashLocked when the
// hash is NOT already pooled (any pooled tx has a non-nil sender, so the
// pooled-sender lookup always succeeds first) — so the delete loop is
// unreachable from RemoveCanonical and is covered here as a unit test of
// the unexported helper instead.
func TestMemoryTxPoolRemoveHashLockedDirect(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	tx0 := transaction.NewTransaction(0, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	tx1 := transaction.NewTransaction(1, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
	if err := pool.AddLocal(tx0); err != nil {
		t.Fatal(err)
	}
	if err := pool.AddLocal(tx1); err != nil {
		t.Fatal(err)
	}

	pool.mu.Lock()
	pool.removeHashLocked(tx0.Hash())
	pool.mu.Unlock()

	if pool.Has(tx0.Hash()) {
		t.Fatal("tx0 should have been removed")
	}
	if !pool.Has(tx1.Hash()) {
		t.Fatal("tx1 should remain pooled")
	}
	pending := pool.Pending(false)[from]
	if len(pending) != 1 || pending[0].Hash() != tx1.Hash() {
		t.Fatalf("pending = %v, want only tx1", pending)
	}

	// Removing the last tx for an address drops the address entirely.
	pool.mu.Lock()
	pool.removeHashLocked(tx1.Hash())
	pool.mu.Unlock()
	if _, ok := pool.Pending(false)[from]; ok {
		t.Fatal("expected address entry to be removed once its last tx is gone")
	}

	// Unknown hash is a no-op.
	pool.mu.Lock()
	pool.removeHashLocked(types.HexToHash("0xfeed"))
	pool.mu.Unlock()
}
