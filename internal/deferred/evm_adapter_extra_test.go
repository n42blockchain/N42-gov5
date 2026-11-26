package deferred

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestNewEVMExecuteFunc_BlockNotFound covers the "block not found" error
// branch of NewEVMExecuteFunc's returned ExecuteFunc using an empty test
// database, without needing a full block/receipt fixture (bc is unused by
// the function body, so nil is safe here).
func TestNewEVMExecuteFunc_BlockNotFound(t *testing.T) {
	db := memdb.NewTestDB(t)

	fn := NewEVMExecuteFunc(db, nil)
	_, err := fn(context.Background(), 1, types.HexToHash("0xdead"))
	if err == nil {
		t.Fatal("ExecuteFunc() error = nil, want error for missing block")
	}
}

// TestExecutedBlockGas: the stored executed result wins over the receipts'
// cumulative gas (they differ from Amsterdam), then the header figure.
func TestExecutedBlockGas(t *testing.T) {
	db := memdb.NewTestDB(t)
	h := types.HexToHash("0x01")
	rcpts := []*block.Receipt{{CumulativeGasUsed: 900}}

	got, err := executedBlockGas(readerOf(t, db), h, 700, rcpts)
	if err != nil || got != 700 {
		t.Fatalf("no stored result: got %d, %v; want header 700", got, err)
	}
	got, _ = executedBlockGas(readerOf(t, db), h, 0, rcpts)
	if got != 900 {
		t.Fatalf("zero header: got %d; want receipts 900", got)
	}
	rw, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rawdb.WriteExecutedResult(rw, h, rawdb.ExecutedResult{GasUsed: 500}); err != nil {
		t.Fatal(err)
	}
	got, err = executedBlockGas(rw, h, 700, rcpts)
	rw.Rollback()
	if err != nil || got != 500 {
		t.Fatalf("stored result: got %d, %v; want 500", got, err)
	}
}

func readerOf(t *testing.T, db kv.RwDB) kv.Tx {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	return tx
}
