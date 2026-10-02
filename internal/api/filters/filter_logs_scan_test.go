package filters

import (
	"context"
	"math"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// g41ScanChain extends filterChainStub with header/log/receipt lookups so
// Filter.Logs (and its blockLogs/checkMatches/indexedLogs/unindexedLogs
// helpers) can be exercised end to end.
type g41ScanChain struct {
	filterChainStub
	byNumber map[uint64]block.IHeader
	byHash   map[types.Hash]block.IHeader
	logs     map[types.Hash][][]*block.Log
	receipts map[types.Hash]block.Receipts
}

func (c *g41ScanChain) GetHeaderByNumber(n *uint256.Int) block.IHeader {
	if c.byNumber == nil || n == nil {
		return nil
	}
	h, ok := c.byNumber[n.Uint64()]
	if !ok {
		return nil
	}
	return h
}

func (c *g41ScanChain) GetHeaderByHash(h types.Hash) (block.IHeader, error) {
	if c.byHash == nil {
		return nil, nil
	}
	return c.byHash[h], nil
}

func (c *g41ScanChain) GetLogs(h types.Hash) ([][]*block.Log, error) {
	return c.logs[h], nil
}

func (c *g41ScanChain) GetReceipts(h types.Hash) (block.Receipts, error) {
	return c.receipts[h], nil
}

// g41ScanAPI wraps filterAPIStub, overriding Database() with a real test DB
// so indexedLogs can open a read-only transaction.
type g41ScanAPI struct {
	filterAPIStub
	db kv.RwDB
}

func (a *g41ScanAPI) Database() kv.RwDB { return a.db }

func g41MakeHeader(num uint64, bloom block.Bloom) *block.Header {
	return &block.Header{
		Number:     uint256.NewInt(num),
		Difficulty: uint256.NewInt(0),
		Bloom:      bloom,
		Extra:      []byte{byte(num)},
	}
}

func TestFilterLogsUnindexedScan(t *testing.T) {
	// No address/topic criteria: indexedLogs reports "no criteria" and the
	// filter falls back to a full unindexedLogs scan over blockLogs.
	addr := g41Addr(1)
	h1 := g41MakeHeader(1, block.Bloom{})
	h2 := g41MakeHeader(2, block.Bloom{})

	chain := &g41ScanChain{
		byNumber: map[uint64]block.IHeader{
			1: h1,
			2: h2,
		},
		logs: map[types.Hash][][]*block.Log{
			h1.Hash(): {{g41Log(addr, 1)}},
			h2.Hash(): {{g41Log(addr, 2)}},
		},
	}
	chain.current = &filterBlockStub{header: h2}

	db := memdb.NewTestDB(t)
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: db}

	f := NewRangeFilter(api, 1, 2, nil, nil)
	logs, err := f.Logs(context.Background())
	if err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 logs from the unindexed scan, got %+v", logs)
	}
}

func TestBlockLogsBloomPrefilter(t *testing.T) {
	addr := g41Addr(1)
	var bloom block.Bloom
	bloom.Add(addr.Bytes())

	hMatch := g41MakeHeader(1, bloom)
	hNoMatch := g41MakeHeader(2, block.Bloom{})

	chain := &g41ScanChain{
		logs: map[types.Hash][][]*block.Log{
			hMatch.Hash():   {{g41Log(addr, 1)}},
			hNoMatch.Hash(): {{g41Log(addr, 2)}},
		},
	}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := newFilter(api, []types.Address{addr}, nil)
	logs, err := f.blockLogs(context.Background(), hMatch)
	if err != nil {
		t.Fatalf("blockLogs error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected bloom match to yield 1 log, got %d", len(logs))
	}

	logs, err = f.blockLogs(context.Background(), hNoMatch)
	if err != nil {
		t.Fatalf("blockLogs error: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected empty bloom to skip checkMatches entirely, got %d logs", len(logs))
	}
}

func TestFilterLogsBlockHashSingleBlock(t *testing.T) {
	addr := g41Addr(2)
	var bloom block.Bloom
	bloom.Add(addr.Bytes())
	h1 := g41MakeHeader(5, bloom)

	chain := &g41ScanChain{
		byHash: map[types.Hash]block.IHeader{
			h1.Hash(): h1,
		},
		logs: map[types.Hash][][]*block.Log{
			h1.Hash(): {{g41Log(addr, 5)}},
		},
	}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := NewBlockFilter(api, h1.Hash(), []types.Address{addr}, nil)
	logs, err := f.Logs(context.Background())
	if err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
}

func TestFilterLogsBlockHashUnknownBlock(t *testing.T) {
	chain := &g41ScanChain{}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := NewBlockFilter(api, types.HexToHash("0xdead"), nil, nil)
	_, err := f.Logs(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown block hash")
	}
}

func TestFilterLogsPendingShortcut(t *testing.T) {
	chain := &g41ScanChain{}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := NewRangeFilter(api, jsonrpc.PendingBlockNumber.Int64(), jsonrpc.PendingBlockNumber.Int64(), nil, nil)
	logs, err := f.Logs(context.Background())
	if err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if logs != nil {
		t.Fatalf("expected nil pending logs, got %+v", logs)
	}

	// Mismatched begin/end around the pending sentinel is an error.
	f2 := NewRangeFilter(api, jsonrpc.PendingBlockNumber.Int64(), 5, nil, nil)
	if _, err := f2.Logs(context.Background()); err == nil {
		t.Fatal("expected invalid block range error")
	}
}

func TestFilterLogsNilCurrentBlock(t *testing.T) {
	chain := &g41ScanChain{} // current is nil
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := NewRangeFilter(api, 0, 10, nil, nil)
	logs, err := f.Logs(context.Background())
	if err != nil || logs != nil {
		t.Fatalf("expected (nil, nil) when current block is unavailable, got (%v, %v)", logs, err)
	}
}

func TestFilterLogsUnindexedScanPrunedRange(t *testing.T) {
	// Current head is at 5, but block 1 is missing: this must surface an
	// error rather than silently returning an empty/partial result.
	h5 := g41MakeHeader(5, block.Bloom{})
	chain := &g41ScanChain{
		byNumber: map[uint64]block.IHeader{
			5: h5,
		},
	}
	chain.current = &filterBlockStub{header: h5}
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: memdb.NewTestDB(t)}

	f := NewRangeFilter(api, 1, 5, nil, nil)
	_, err := f.Logs(context.Background())
	if err == nil {
		t.Fatal("expected pruned-range error")
	}
}

func TestIndexedLogsRangeOverflowFallsBack(t *testing.T) {
	chain := &g41ScanChain{}
	db := memdb.NewTestDB(t)
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: db}

	f := newFilter(api, []types.Address{g41Addr(1)}, nil)
	f.begin = 1
	logs, indexed, err := f.indexedLogs(context.Background(), uint64(math.MaxUint32)+1)
	if err != nil {
		t.Fatalf("indexedLogs unexpected error: %v", err)
	}
	if indexed {
		t.Fatal("expected fallback (indexed=false) on range overflow")
	}
	if logs != nil {
		t.Fatalf("expected nil logs on fallback, got %+v", logs)
	}
}

func TestIndexedLogsNoCriteriaFallsBack(t *testing.T) {
	chain := &g41ScanChain{}
	db := memdb.NewTestDB(t)
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: db}

	f := newFilter(api, nil, nil)
	_, indexed, err := f.indexedLogs(context.Background(), 10)
	if err != nil {
		t.Fatalf("indexedLogs unexpected error: %v", err)
	}
	if indexed {
		t.Fatal("expected fallback when no address/topic criteria set")
	}
}

func TestIndexedLogsEmptyIndexFallsBack(t *testing.T) {
	// Address criteria set, but the index tables are empty: BlocksForAddresses
	// returns an empty (non-nil) bitmap, so indexedLogs should report
	// indexed=true with zero results.
	chain := &g41ScanChain{}
	db := memdb.NewTestDB(t)
	api := &g41ScanAPI{filterAPIStub: filterAPIStub{bc: chain}, db: db}

	f := newFilter(api, []types.Address{g41Addr(9)}, nil)
	f.begin = 0
	logs, indexed, err := f.indexedLogs(context.Background(), 100)
	if err != nil {
		t.Fatalf("indexedLogs unexpected error: %v", err)
	}
	if !indexed {
		t.Fatal("expected indexed=true with an empty-but-present bitmap")
	}
	if len(logs) != 0 {
		t.Fatalf("expected no logs from an empty index, got %+v", logs)
	}
}

func TestPendingLogsReturnsNil(t *testing.T) {
	f := &Filter{}
	logs, err := f.pendingLogs()
	if logs != nil || err != nil {
		t.Fatalf("pendingLogs() = (%v, %v), want (nil, nil)", logs, err)
	}
}
