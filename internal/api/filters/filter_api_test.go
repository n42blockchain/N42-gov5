package filters

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// g41NewAPI builds a minimal filters.Api backed by filterChainStub/filterAPIStub
// (declared in header_number_test.go), with a nil current block by default.
func g41NewAPI() Api {
	return &filterAPIStub{bc: &filterChainStub{}}
}

func TestNewFilterAPILifecycle(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, 50*time.Millisecond)
	defer fa.Close()

	if fa == nil {
		t.Fatal("NewFilterAPI returned nil")
	}
}

func TestFilterAPITimeoutLoopExpiresFilter(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, 10*time.Millisecond)
	defer fa.Close()

	id := fa.NewPendingTransactionFilter()

	// Wait for the timeout loop to expire and uninstall the filter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fa.filtersMu.Lock()
		_, found := fa.filters[id]
		fa.filtersMu.Unlock()
		if !found {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected pending tx filter to expire via timeoutLoop")
}

func TestNewPendingTransactionFilterAndChanges(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	id := fa.NewPendingTransactionFilter()
	if id == "" {
		t.Fatal("expected non-empty filter id")
	}

	// GetFilterChanges before any event should return an empty hash list.
	changes, err := fa.GetFilterChanges(id)
	if err != nil {
		t.Fatalf("GetFilterChanges error: %v", err)
	}
	hashes, ok := changes.([]types.Hash)
	if !ok {
		t.Fatalf("expected []types.Hash, got %T", changes)
	}
	if len(hashes) != 0 {
		t.Fatalf("expected no hashes yet, got %d", len(hashes))
	}

	if !fa.UninstallFilter(id) {
		t.Fatal("expected UninstallFilter to find the filter")
	}
	if fa.UninstallFilter(id) {
		t.Fatal("expected second UninstallFilter to report not found")
	}
}

func TestNewBlockFilterLifecycle(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	id := fa.NewBlockFilter()
	if id == "" {
		t.Fatal("expected non-empty filter id")
	}

	changes, err := fa.GetFilterChanges(id)
	if err != nil {
		t.Fatalf("GetFilterChanges error: %v", err)
	}
	if _, ok := changes.([]types.Hash); !ok {
		t.Fatalf("expected []types.Hash, got %T", changes)
	}

	if !fa.UninstallFilter(id) {
		t.Fatal("expected UninstallFilter to find the block filter")
	}
}

func TestGetFilterChangesUnknownID(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	_, err := fa.GetFilterChanges(jsonrpc.ID("0xdoesnotexist"))
	if err == nil {
		t.Fatal("expected error for unknown filter id")
	}
}

func TestGetFilterLogsUnknownID(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	_, err := fa.GetFilterLogs(context.Background(), jsonrpc.ID("0xdoesnotexist"))
	if err == nil {
		t.Fatal("expected error for unknown filter id")
	}
}

func TestCheckBlockRange(t *testing.T) {
	chain := &filterChainStub{current: &filterBlockStub{header: &headerStub{number: mustUint256(100)}}}
	fa := &FilterAPI{api: &filterAPIStub{bc: chain}}

	if err := fa.checkBlockRange(0, 100); err != nil {
		t.Fatalf("expected small range to pass, got %v", err)
	}
	if err := fa.checkBlockRange(0, maxFilterBlockRange+1); err == nil {
		t.Fatal("expected range exceeding maxFilterBlockRange to fail")
	}
	// Negative sentinels resolve against CurrentBlock().
	if err := fa.checkBlockRange(-1, -1); err != nil {
		t.Fatalf("expected negative sentinels resolved via head to pass: %v", err)
	}
}

func TestCheckBlockRangeNilCurrentBlock(t *testing.T) {
	chain := &filterChainStub{} // current == nil
	fa := &FilterAPI{api: &filterAPIStub{bc: chain}}

	// Negative sentinels with no current block: rb/re stay negative, so the
	// range check (re >= rb) still holds and no error is produced.
	if err := fa.checkBlockRange(-1, -1); err != nil {
		t.Fatalf("expected no error when current block is nil: %v", err)
	}
}

func TestNewFilterAndGetFilterLogs(t *testing.T) {
	addr := g41Addr(3)
	h1 := g41MakeHeader(1, block.Bloom{})

	chain := &g41ScanChain{
		byNumber: map[uint64]block.IHeader{1: h1},
		logs: map[types.Hash][][]*block.Log{
			h1.Hash(): {{g41Log(addr, 1)}},
		},
	}
	chain.current = &filterBlockStub{header: h1}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	crit := FilterCriteria{FromBlock: big.NewInt(1), ToBlock: big.NewInt(1)}
	id, err := fa.NewFilter(crit)
	if err != nil {
		t.Fatalf("NewFilter error: %v", err)
	}

	logs, err := fa.GetFilterLogs(context.Background(), id)
	if err != nil {
		t.Fatalf("GetFilterLogs error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	// GetLogs should produce the same result through its own range filter.
	logs2, err := fa.GetLogs(context.Background(), crit)
	if err != nil {
		t.Fatalf("GetLogs error: %v", err)
	}
	if len(logs2) != 1 {
		t.Fatalf("expected 1 log from GetLogs, got %d", len(logs2))
	}

	if !fa.UninstallFilter(id) {
		t.Fatal("expected UninstallFilter to find the log filter")
	}
}

func TestGetLogsBlockRangeTooLarge(t *testing.T) {
	chain := &g41ScanChain{}
	chain.current = &filterBlockStub{header: g41MakeHeader(0, block.Bloom{})}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	crit := FilterCriteria{FromBlock: big.NewInt(0), ToBlock: big.NewInt(maxFilterBlockRange + 1)}
	if _, err := fa.GetLogs(context.Background(), crit); err == nil {
		t.Fatal("expected error for oversized block range")
	}
}

func TestNotificationMethodsWithoutNotifier(t *testing.T) {
	api := g41NewAPI()
	fa := NewFilterAPI(api, time.Hour)
	defer fa.Close()

	if _, err := fa.NewPendingTransactions(context.Background()); err != jsonrpc.ErrNotificationsUnsupported {
		t.Fatalf("NewPendingTransactions: expected ErrNotificationsUnsupported, got %v", err)
	}
	if _, err := fa.NewHeads(context.Background()); err != jsonrpc.ErrNotificationsUnsupported {
		t.Fatalf("NewHeads: expected ErrNotificationsUnsupported, got %v", err)
	}
	if _, err := fa.Logs(context.Background(), FilterCriteria{}); err != jsonrpc.ErrNotificationsUnsupported {
		t.Fatalf("Logs: expected ErrNotificationsUnsupported, got %v", err)
	}
}

func mustUint256(n uint64) *uint256.Int {
	return uint256.NewInt(n)
}
