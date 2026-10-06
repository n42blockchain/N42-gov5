package publicrpc

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func mkHeader(num uint64, parent types.Hash) *block.Header {
	return &block.Header{
		Number:      uint256.NewInt(num),
		ParentHash:  parent,
		Difficulty:  uint256.NewInt(0),
		BaseFee:     uint256.NewInt(0),
		ReceiptHash: types.Hash{},
	}
}

// chainFixture builds a 3-block chain (0,1,2) in a fresh memdb, writes the
// canonical hashes, head block hash, and a tiny TD at block 2, and returns the
// *ethelChain plus the block hashes for assertions.
func chainFixture(t *testing.T) (*ethelChain, []*block.Header, func()) {
	t.Helper()
	db := memdb.New(t.TempDir())

	hdrs := []*block.Header{mkHeader(0, types.Hash{})}
	for i := uint64(1); i <= 2; i++ {
		hdrs = append(hdrs, mkHeader(i, hdrs[i-1].Hash()))
	}

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hdrs {
		b := block.NewBlock(h, nil)
		if err := rawdb.WriteBlock(tx, b.(*block.Block)); err != nil {
			t.Fatalf("WriteBlock: %v", err)
		}
		if err := rawdb.WriteCanonicalHash(tx, h.Hash(), h.Number.Uint64()); err != nil {
			t.Fatalf("WriteCanonicalHash: %v", err)
		}
	}
	rawdb.WriteHeadBlockHash(tx, hdrs[2].Hash())
	if err := rawdb.WriteTd(tx, hdrs[2].Hash(), 2, uint256.NewInt(42)); err != nil {
		t.Fatalf("WriteTd: %v", err)
	}
	if err := rawdb.WriteForkchoiceSafeHash(tx, hdrs[1].Hash()); err != nil {
		t.Fatalf("WriteForkchoiceSafeHash: %v", err)
	}
	if err := rawdb.WriteForkchoiceFinalizedHash(tx, hdrs[0].Hash()); err != nil {
		t.Fatalf("WriteForkchoiceFinalizedHash: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	c := newEthelChain(db, nil, &params.ChainConfig{}, nil)
	return c, hdrs, func() { db.Close() }
}

func TestEthelChain_CurrentBlock(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	cur := c.CurrentBlock()
	if cur == nil {
		t.Fatal("CurrentBlock() = nil")
	}
	if cur.Hash() != hdrs[2].Hash() {
		t.Errorf("CurrentBlock().Hash() = %x, want %x", cur.Hash(), hdrs[2].Hash())
	}
}

func TestEthelChain_CurrentBlock_HeadMarkerFallback(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()

	hdrs := []*block.Header{mkHeader(0, types.Hash{})}
	hdrs = append(hdrs, mkHeader(1, hdrs[0].Hash()))

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hdrs {
		b := block.NewBlock(h, nil)
		if err := rawdb.WriteBlock(tx, b.(*block.Block)); err != nil {
			t.Fatal(err)
		}
		if err := rawdb.WriteCanonicalHash(tx, h.Hash(), h.Number.Uint64()); err != nil {
			t.Fatal(err)
		}
	}
	// No WriteHeadBlockHash -> falls back to the eth-el head marker.
	if err := ethel.WriteHeadMarker(tx, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	c := newEthelChain(db, nil, &params.ChainConfig{}, nil)
	cur := c.CurrentBlock()
	if cur == nil {
		t.Fatal("CurrentBlock() = nil")
	}
	if cur.Hash() != hdrs[1].Hash() {
		t.Errorf("CurrentBlock().Hash() = %x, want %x", cur.Hash(), hdrs[1].Hash())
	}
}

func TestEthelChain_CurrentBlock_Empty(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	c := newEthelChain(db, nil, &params.ChainConfig{}, nil)
	if cur := c.CurrentBlock(); cur != nil {
		t.Errorf("CurrentBlock() on empty db = %v, want nil", cur)
	}
}

func TestEthelChain_GetHeader(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	if h := c.GetHeader(hdrs[1].Hash(), uint256.NewInt(1)); h == nil {
		t.Error("GetHeader: expected non-nil")
	}
	if h := c.GetHeader(hdrs[1].Hash(), nil); h != nil {
		t.Error("GetHeader(nil number): expected nil")
	}
	if h := c.GetHeader(types.Hash{0xff}, uint256.NewInt(99)); h != nil {
		t.Error("GetHeader(missing): expected nil")
	}
}

func TestEthelChain_GetHeaderByNumber(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	if h := c.GetHeaderByNumber(uint256.NewInt(1)); h == nil || h.Hash() != hdrs[1].Hash() {
		t.Errorf("GetHeaderByNumber(1) = %v", h)
	}
	if h := c.GetHeaderByNumber(nil); h != nil {
		t.Error("GetHeaderByNumber(nil): expected nil")
	}
	if h := c.GetHeaderByNumber(uint256.NewInt(999)); h != nil {
		t.Error("GetHeaderByNumber(missing): expected nil")
	}
}

func TestEthelChain_GetHeaderByHash(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	h, err := c.GetHeaderByHash(hdrs[1].Hash())
	if err != nil || h == nil {
		t.Fatalf("GetHeaderByHash: %v, %v", h, err)
	}
	h2, err := c.GetHeaderByHash(types.Hash{0xff})
	if err != nil || h2 != nil {
		t.Errorf("GetHeaderByHash(missing) = %v, %v", h2, err)
	}
}

func TestEthelChain_GetTd(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	if td := c.GetTd(hdrs[2].Hash(), uint256.NewInt(2)); td == nil || td.Uint64() != 42 {
		t.Errorf("GetTd = %v, want 42", td)
	}
	if td := c.GetTd(hdrs[2].Hash(), nil); td != nil {
		t.Error("GetTd(nil number): expected nil")
	}
}

func TestEthelChain_GetBlockByNumber(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	b, err := c.GetBlockByNumber(uint256.NewInt(1))
	if err != nil || b == nil || b.Hash() != hdrs[1].Hash() {
		t.Fatalf("GetBlockByNumber(1) = %v, %v", b, err)
	}
	b2, err := c.GetBlockByNumber(nil)
	if err != nil || b2 != nil {
		t.Errorf("GetBlockByNumber(nil) = %v, %v", b2, err)
	}
	b3, err := c.GetBlockByNumber(uint256.NewInt(999))
	if err != nil || b3 != nil {
		t.Errorf("GetBlockByNumber(missing) = %v, %v", b3, err)
	}
}

func TestEthelChain_GetBlockByHash(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	b, err := c.GetBlockByHash(hdrs[1].Hash())
	if err != nil || b == nil {
		t.Fatalf("GetBlockByHash: %v, %v", b, err)
	}
	b2, err := c.GetBlockByHash(types.Hash{0xff})
	if err != nil || b2 != nil {
		t.Errorf("GetBlockByHash(missing) = %v, %v", b2, err)
	}
}

func TestEthelChain_ForkchoiceTaggedBlock(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	if b := c.ForkchoiceTaggedBlock(jsonrpc.LatestBlockNumber); b == nil || b.Hash() != hdrs[2].Hash() {
		t.Errorf("ForkchoiceTaggedBlock(latest) = %v", b)
	}
	if b := c.ForkchoiceTaggedBlock(jsonrpc.PendingBlockNumber); b == nil || b.Hash() != hdrs[2].Hash() {
		t.Errorf("ForkchoiceTaggedBlock(pending) = %v", b)
	}
	if b := c.ForkchoiceTaggedBlock(jsonrpc.SafeBlockNumber); b == nil || b.Hash() != hdrs[1].Hash() {
		t.Errorf("ForkchoiceTaggedBlock(safe) = %v", b)
	}
	if b := c.ForkchoiceTaggedBlock(jsonrpc.FinalizedBlockNumber); b == nil || b.Hash() != hdrs[0].Hash() {
		t.Errorf("ForkchoiceTaggedBlock(finalized) = %v", b)
	}
	if b := c.ForkchoiceTaggedBlock(jsonrpc.EarliestBlockNumber); b != nil {
		t.Errorf("ForkchoiceTaggedBlock(unhandled tag) = %v, want nil", b)
	}
}

func TestEthelChain_ForkchoiceTaggedBlock_NoHashSet(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	c := newEthelChain(db, nil, &params.ChainConfig{}, nil)
	if b := c.ForkchoiceTaggedBlock(jsonrpc.SafeBlockNumber); b != nil {
		t.Errorf("ForkchoiceTaggedBlock(safe, unset) = %v, want nil", b)
	}
}

func TestEthelChain_ReadOnlyWriteMethods(t *testing.T) {
	c, _, cleanup := chainFixture(t)
	defer cleanup()

	if _, err := c.InsertHeader(nil); err != errReadOnly {
		t.Errorf("InsertHeader err = %v, want errReadOnly", err)
	}
	if _, err := c.InsertChain(nil); err != errReadOnly {
		t.Errorf("InsertChain err = %v, want errReadOnly", err)
	}
	if _, err := c.InsertBlock(nil, false); err != errReadOnly {
		t.Errorf("InsertBlock err = %v, want errReadOnly", err)
	}
	if err := c.SealedBlock(nil); err != errReadOnly {
		t.Errorf("SealedBlock err = %v, want errReadOnly", err)
	}
	if err := c.SetHead(0); err != errReadOnly {
		t.Errorf("SetHead err = %v, want errReadOnly", err)
	}
	if err := c.AddFutureBlock(nil); err != errReadOnly {
		t.Errorf("AddFutureBlock err = %v, want errReadOnly", err)
	}
	if err := c.WriteBlockWithState(nil, nil, nil, nil); err != errReadOnly {
		t.Errorf("WriteBlockWithState err = %v, want errReadOnly", err)
	}
}

func TestEthelChain_MiscAccessors(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	if c.Blocks() != nil {
		t.Error("Blocks() should be nil")
	}
	if err := c.Start(); err != nil {
		t.Errorf("Start() = %v", err)
	}
	if g := c.GenesisBlock(); g == nil || g.Hash() != hdrs[0].Hash() {
		t.Errorf("GenesisBlock() = %v", g)
	}
	c.SetEngine("fake-engine")
	if c.Engine() != "fake-engine" {
		t.Errorf("Engine() = %v, want fake-engine", c.Engine())
	}
	if blocks := c.GetBlocksFromHash(hdrs[2].Hash(), 3); len(blocks) != 3 {
		t.Errorf("GetBlocksFromHash returned %d blocks, want 3", len(blocks))
	}
	if blocks := c.GetBlocksFromHash(types.Hash{0xff}, 3); len(blocks) != 0 {
		t.Errorf("GetBlocksFromHash(missing) returned %d blocks, want 0", len(blocks))
	}
	if b := c.GetBlock(hdrs[1].Hash(), 1); b == nil {
		t.Error("GetBlock: expected non-nil")
	}
	if !c.HasBlock(hdrs[1].Hash(), 1) {
		t.Error("HasBlock: expected true")
	}
	if c.HasBlock(types.Hash{0xff}, 99) {
		t.Error("HasBlock(missing): expected false")
	}
	if c.DB() == nil {
		t.Error("DB() = nil")
	}
	if c.Quit() == nil {
		t.Error("Quit() = nil")
	}
	if c.EarliestBlock() != 0 {
		t.Errorf("EarliestBlock() = %d, want 0", c.EarliestBlock())
	}
	if c.Config() == nil {
		t.Error("Config() = nil")
	}
}

func TestEthelChain_Close(t *testing.T) {
	c, _, cleanup := chainFixture(t)
	defer cleanup()

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-c.Quit():
	default:
		t.Error("expected quit channel to be closed")
	}
	// Close is idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestEthelChain_GetReceiptsAndLogs(t *testing.T) {
	c, hdrs, cleanup := chainFixture(t)
	defer cleanup()

	receipts, err := c.GetReceipts(hdrs[1].Hash())
	if err != nil {
		t.Fatalf("GetReceipts: %v", err)
	}
	if len(receipts) != 0 {
		t.Errorf("GetReceipts = %d receipts, want 0 (no txs)", len(receipts))
	}
	logs, err := c.GetLogs(hdrs[1].Hash())
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("GetLogs = %d entries, want 0", len(logs))
	}

	// Missing block -> GetReceipts returns nil, nil (no header number).
	receipts2, err := c.GetReceipts(types.Hash{0xff})
	if err != nil || receipts2 != nil {
		t.Errorf("GetReceipts(missing) = %v, %v", receipts2, err)
	}
}

func TestEthelChain_StateAt(t *testing.T) {
	c, _, cleanup := chainFixture(t)
	defer cleanup()

	// No stateReader wired -> nil.
	if s := c.StateAt(nil, 0); s != nil {
		t.Errorf("StateAt with no reader = %v, want nil", s)
	}
}
