package deferred

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
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
