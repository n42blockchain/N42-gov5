package ethel

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func TestMemoryTxPoolOrdersTransactionsAndTracksNonce(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	for _, nonce := range []uint64{2, 0, 1} {
		tx := transaction.NewTransaction(nonce, from, nil, uint256.NewInt(0), 21_000, uint256.NewInt(1), nil)
		if err := pool.AddLocal(tx); err != nil {
			t.Fatal(err)
		}
	}
	pending := pool.Pending(false)[from]
	if len(pending) != 3 || pending[0].Nonce() != 0 || pending[1].Nonce() != 1 || pending[2].Nonce() != 2 {
		t.Fatalf("pending nonces are not ordered: %v", pending)
	}
	if nonce := pool.Nonce(from); nonce != 3 {
		t.Fatalf("Nonce = %d, want 3", nonce)
	}
}

func TestMemoryTxPoolReplacesBlobTransaction(t *testing.T) {
	pool := NewMemoryTxPool()
	from := types.HexToAddress("0x1234")
	old := newMemoryPoolBlobTransaction(from, 7, 100, 200, 300)
	underpriced := newMemoryPoolBlobTransaction(from, 7, 199, 400, 600)
	replacement := newMemoryPoolBlobTransaction(from, 7, 200, 400, 600)

	if err := pool.AddLocal(old); err != nil {
		t.Fatal(err)
	}
	if err := pool.AddLocal(underpriced); !errors.Is(err, errMemoryPoolReplaceUnderpriced) {
		t.Fatalf("underpriced replacement error = %v, want %v", err, errMemoryPoolReplaceUnderpriced)
	}
	if !pool.Has(old.Hash()) || pool.Has(underpriced.Hash()) {
		t.Fatal("underpriced replacement changed the pool")
	}
	if err := pool.AddLocal(replacement); err != nil {
		t.Fatal(err)
	}
	pending := pool.Pending(false)[from]
	if len(pending) != 1 || pending[0].Hash() != replacement.Hash() {
		t.Fatalf("pending transactions = %v, want only replacement %s", pending, replacement.Hash())
	}
	if pool.Has(old.Hash()) || !pool.Has(replacement.Hash()) {
		t.Fatal("replacement did not update the transaction hash index")
	}
}

func newMemoryPoolBlobTransaction(from types.Address, nonce, tip, feeCap, blobFeeCap uint64) *transaction.Transaction {
	tx := transaction.NewTx(&transaction.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(tip),
		GasFeeCap:  uint256.NewInt(feeCap),
		Gas:        100_000,
		To:         types.HexToAddress("0xabcd"),
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(blobFeeCap),
		BlobHashes: []types.Hash{types.HexToHash("0x01")},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(0),
		S:          uint256.NewInt(0),
	})
	tx.SetFrom(from)
	return tx
}
