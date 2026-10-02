package graphql

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// resolverChainBlockStub is a minimal block.IBlock wrapping a real *block.Header
// so marshalBlock's type assertion (iblock.Header().(*block.Header)) succeeds.
type resolverChainBlockStub struct {
	header *block.Header
	txs    []*transaction.Transaction
}

func (b *resolverChainBlockStub) Number64() *uint256.Int { return b.header.Number }
func (b *resolverChainBlockStub) BaseFee64() *uint256.Int {
	return b.header.BaseFee
}
func (b *resolverChainBlockStub) Hash() types.Hash                                { return b.header.Hash() }
func (b *resolverChainBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *resolverChainBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *resolverChainBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *resolverChainBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *resolverChainBlockStub) StateRoot() types.Hash                           { return b.header.Root }
func (b *resolverChainBlockStub) Header() block.IHeader                           { return b.header }
func (b *resolverChainBlockStub) Body() block.IBody                               { return nil }
func (b *resolverChainBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *resolverChainBlockStub) Transactions() []*transaction.Transaction        { return b.txs }
func (b *resolverChainBlockStub) Difficulty() *uint256.Int                        { return b.header.Difficulty }
func (b *resolverChainBlockStub) Time() uint64                                    { return b.header.Time }
func (b *resolverChainBlockStub) GasLimit() uint64                                { return b.header.GasLimit }
func (b *resolverChainBlockStub) GasUsed() uint64                                 { return b.header.GasUsed }
func (b *resolverChainBlockStub) Nonce() uint64                                   { return 0 }
func (b *resolverChainBlockStub) Coinbase() types.Address                         { return b.header.Coinbase }
func (b *resolverChainBlockStub) ParentHash() types.Hash                          { return b.header.ParentHash }
func (b *resolverChainBlockStub) TxHash() types.Hash                              { return b.header.TxHash }
func (b *resolverChainBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

// resolverChainStub implements common.IBlockChain with just enough behavior for
// Resolver.Block / Resolver.Logs: lookup by hash/number, current block, GetTd and
// GetLogs. All other methods are unused stubs.
type resolverChainStub struct {
	byHash   map[types.Hash]block.IBlock
	byNumber map[uint64]block.IBlock
	current  block.IBlock
	td       *uint256.Int
	logs     map[types.Hash][][]*block.Log
}

func (m *resolverChainStub) Config() *params.ChainConfig { return nil }
func (m *resolverChainStub) CurrentBlock() block.IBlock  { return m.current }
func (m *resolverChainStub) GetHeader(types.Hash, *uint256.Int) block.IHeader {
	return nil
}
func (m *resolverChainStub) GetHeaderByNumber(*uint256.Int) block.IHeader { return nil }
func (m *resolverChainStub) GetHeaderByHash(types.Hash) (block.IHeader, error) {
	return nil, nil
}
func (m *resolverChainStub) GetTd(types.Hash, *uint256.Int) *uint256.Int { return m.td }
func (m *resolverChainStub) GetBlockByNumber(n *uint256.Int) (block.IBlock, error) {
	b, ok := m.byNumber[n.Uint64()]
	if !ok {
		return nil, nil
	}
	return b, nil
}
func (m *resolverChainStub) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}
func (m *resolverChainStub) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}
func (m *resolverChainStub) InsertHeader([]block.IHeader) (int, error) { return 0, nil }
func (m *resolverChainStub) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	b, ok := m.byHash[h]
	if !ok {
		return nil, nil
	}
	return b, nil
}
func (m *resolverChainStub) Blocks() []block.IBlock                           { return nil }
func (m *resolverChainStub) Start() error                                     { return nil }
func (m *resolverChainStub) GenesisBlock() block.IBlock                       { return nil }
func (m *resolverChainStub) NewBlockHandler([]byte, peer.ID) error            { return nil }
func (m *resolverChainStub) InsertChain([]block.IBlock) (int, error)          { return 0, nil }
func (m *resolverChainStub) InsertBlock([]block.IBlock, bool) (int, error)    { return 0, nil }
func (m *resolverChainStub) SetEngine(interface{})                            {}
func (m *resolverChainStub) GetBlocksFromHash(types.Hash, int) []block.IBlock { return nil }
func (m *resolverChainStub) SealedBlock(block.IBlock) error                   { return nil }
func (m *resolverChainStub) Engine() interface{}                              { return nil }
func (m *resolverChainStub) GetReceipts(types.Hash) (block.Receipts, error)   { return nil, nil }
func (m *resolverChainStub) GetLogs(h types.Hash) ([][]*block.Log, error)     { return m.logs[h], nil }
func (m *resolverChainStub) SetHead(uint64) error                             { return nil }
func (m *resolverChainStub) AddFutureBlock(block.IBlock) error                { return nil }
func (m *resolverChainStub) GetBlock(types.Hash, uint64) block.IBlock         { return nil }
func (m *resolverChainStub) StateAt(kv.Tx, uint64) interface{}                { return nil }
func (m *resolverChainStub) HasBlock(types.Hash, uint64) bool                 { return false }
func (m *resolverChainStub) DB() kv.RwDB                                      { return nil }
func (m *resolverChainStub) Quit() <-chan struct{}                            { return nil }
func (m *resolverChainStub) EarliestBlock() uint64                            { return 0 }
func (m *resolverChainStub) Close() error                                     { return nil }
func (m *resolverChainStub) WriteBlockWithState(block.IBlock, []*block.Receipt, interface{}, map[types.Address]*uint256.Int) error {
	return nil
}

func resolverTestHeader(number uint64) *block.Header {
	return &block.Header{
		Number:      uint256.NewInt(number),
		Difficulty:  uint256.NewInt(0),
		GasLimit:    30000000,
		GasUsed:     21000,
		Time:        1234,
		Coinbase:    types.HexToAddress("0xaa"),
		Root:        types.HexToHash("0x01"),
		TxHash:      types.HexToHash("0x02"),
		ReceiptHash: types.HexToHash("0x03"),
		Extra:       []byte{0x01},
	}
}

func TestResolverBlockByNumber(t *testing.T) {
	header := resolverTestHeader(5)
	blk := &resolverChainBlockStub{header: header}
	chain := &resolverChainStub{
		byNumber: map[uint64]block.IBlock{5: blk},
		td:       uint256.NewInt(999),
	}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	num := hexutil.Uint64(5)
	got, err := r.Block(context.Background(), BlockArgs{Number: &num})
	if err != nil {
		t.Fatalf("Block() error = %v", err)
	}
	if got.Number != 5 {
		t.Fatalf("Number = %d, want 5", got.Number)
	}
	if got.TotalDifficulty == nil || got.TotalDifficulty.ToInt().Int64() != 999 {
		t.Fatalf("TotalDifficulty = %v, want 999", got.TotalDifficulty)
	}
}

func TestResolverBlockByHash(t *testing.T) {
	header := resolverTestHeader(6)
	blk := &resolverChainBlockStub{header: header}
	hash := blk.Hash()
	chain := &resolverChainStub{byHash: map[types.Hash]block.IBlock{hash: blk}}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	got, err := r.Block(context.Background(), BlockArgs{Hash: &hash})
	if err != nil {
		t.Fatalf("Block() error = %v", err)
	}
	if got.Hash != hash {
		t.Fatalf("Hash = %s, want %s", got.Hash, hash)
	}
}

func TestResolverBlockByHashNotFound(t *testing.T) {
	chain := &resolverChainStub{byHash: map[types.Hash]block.IBlock{}}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	missing := types.HexToHash("0xdead")
	_, err := r.Block(context.Background(), BlockArgs{Hash: &missing})
	if err == nil {
		t.Fatal("expected error for missing block hash")
	}
}

func TestResolverBlockLatestWhenNoArgs(t *testing.T) {
	header := resolverTestHeader(10)
	blk := &resolverChainBlockStub{header: header}
	chain := &resolverChainStub{current: blk}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	got, err := r.Block(context.Background(), BlockArgs{})
	if err != nil {
		t.Fatalf("Block() error = %v", err)
	}
	if got.Number != 10 {
		t.Fatalf("Number = %d, want 10 (current block)", got.Number)
	}
}

func TestResolverBlockNilCurrentBlock(t *testing.T) {
	chain := &resolverChainStub{}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	_, err := r.Block(context.Background(), BlockArgs{})
	if err == nil {
		t.Fatal("expected error when current block is nil")
	}
}

func TestResolverBlockMarshalsTransactions(t *testing.T) {
	header := resolverTestHeader(1)
	to := types.HexToAddress("0x02")
	tx := transaction.NewTx(&transaction.LegacyTx{Nonce: 1, GasPrice: uint256.NewInt(1), Gas: 21000, To: &to, Value: uint256.NewInt(0)})
	blk := &resolverChainBlockStub{header: header, txs: []*transaction.Transaction{tx}}
	chain := &resolverChainStub{current: blk}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	got, err := r.Block(context.Background(), BlockArgs{})
	if err != nil {
		t.Fatalf("Block() error = %v", err)
	}
	if len(got.Transactions) != 1 || got.Transactions[0].Hash != tx.Hash() {
		t.Fatalf("Transactions = %+v, want [%s]", got.Transactions, tx.Hash())
	}
}

func TestResolverLogsChainUnavailable(t *testing.T) {
	chain := &resolverChainStub{}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)
	if _, err := r.Logs(context.Background(), LogFilter{}); err == nil {
		t.Fatal("expected error when chain has no current block")
	}
}

func TestResolverLogsFromAfterToErrors(t *testing.T) {
	header := resolverTestHeader(10)
	blk := &resolverChainBlockStub{header: header}
	chain := &resolverChainStub{current: blk}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	from := hexutil.Uint64(5)
	to := hexutil.Uint64(2)
	if _, err := r.Logs(context.Background(), LogFilter{FromBlock: &from, ToBlock: &to}); err == nil {
		t.Fatal("expected error when fromBlock > toBlock")
	}
}

func TestResolverLogsRangeTooLarge(t *testing.T) {
	header := resolverTestHeader(10_000_000)
	blk := &resolverChainBlockStub{header: header}
	chain := &resolverChainStub{current: blk}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	from := hexutil.Uint64(0)
	to := hexutil.Uint64(5_000_000)
	if _, err := r.Logs(context.Background(), LogFilter{FromBlock: &from, ToBlock: &to}); err == nil {
		t.Fatal("expected error for an over-large block range")
	}
}

func TestResolverLogsMatchesAndFilters(t *testing.T) {
	addr := types.HexToAddress("0x01")
	header1 := resolverTestHeader(1)
	blk1 := &resolverChainBlockStub{header: header1}
	hash1 := blk1.Hash()

	lg := &block.Log{Address: addr, Topics: []types.Hash{types.HexToHash("0xaa")}, TxHash: types.HexToHash("0xtx"), Index: 0}
	other := &block.Log{Address: types.HexToAddress("0x99")}

	chain := &resolverChainStub{
		current:  blk1,
		byNumber: map[uint64]block.IBlock{1: blk1},
		logs:     map[types.Hash][][]*block.Log{hash1: {{lg, other}}},
	}
	backend := api.NewAPI(chain, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)

	got, err := r.Logs(context.Background(), LogFilter{Addresses: []types.Address{addr}})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if len(got) != 1 || got[0].Address != addr {
		t.Fatalf("Logs() = %+v, want exactly the matching log", got)
	}
}

func TestResolverTransactionNotFound(t *testing.T) {
	backend := api.NewAPI(&resolverChainStub{}, memdb.NewTestDB(t), nil, nil, nil, nil)
	r := NewResolver(backend)
	_, err := r.Transaction(context.Background(), types.HexToHash("0xdead"))
	if err == nil {
		t.Fatal("expected error for a transaction absent from the index")
	}
}

var _ common.IBlockChain = (*resolverChainStub)(nil)
var _ block.IBlock = (*resolverChainBlockStub)(nil)
