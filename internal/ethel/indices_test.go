package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestWriteBlockIndices(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Use an empty tx list — WriteBlockIndices should handle zero txs.
	if err := WriteBlockIndices(tx, 42, []*transaction.Transaction{}); err != nil {
		t.Fatal(err)
	}

	// Test with a known hash directly.
	txHash := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	// Non-existent tx.
	_, found, _ := LookupTransaction(tx, txHash)
	if found {
		t.Error("should not find non-existent hash")
	}

	_ = kv.TxLookup
}
