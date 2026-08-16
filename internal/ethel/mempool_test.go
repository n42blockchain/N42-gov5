package ethel

import (
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
