package filters

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

type filterBlockStub struct {
	header block.IHeader
}

func (b *filterBlockStub) Number64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.Number64()
}
func (b *filterBlockStub) BaseFee64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.BaseFee64()
}
func (b *filterBlockStub) Hash() types.Hash {
	if b.header == nil {
		return types.Hash{}
	}
	return b.header.Hash()
}
func (b *filterBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *filterBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *filterBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *filterBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *filterBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *filterBlockStub) Header() block.IHeader                           { return b.header }
func (b *filterBlockStub) Body() block.IBody                               { return nil }
func (b *filterBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *filterBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *filterBlockStub) Difficulty() *uint256.Int                        { return nil }
func (b *filterBlockStub) Time() uint64                                    { return 0 }
func (b *filterBlockStub) GasLimit() uint64                                { return 0 }
func (b *filterBlockStub) GasUsed() uint64                                 { return 0 }
func (b *filterBlockStub) Nonce() uint64                                   { return 0 }
func (b *filterBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *filterBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *filterBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *filterBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

type filterChainStub struct {
	current block.IBlock
}

func (m *filterChainStub) Config() *params.ChainConfig { return nil }
func (m *filterChainStub) CurrentBlock() block.IBlock  { return m.current }
func (m *filterChainStub) GetHeader(types.Hash, *uint256.Int) block.IHeader {
	return nil
}
func (m *filterChainStub) GetHeaderByNumber(*uint256.Int) block.IHeader { return nil }
func (m *filterChainStub) GetHeaderByHash(types.Hash) (block.IHeader, error) {
	return nil, nil
}
func (m *filterChainStub) GetTd(types.Hash, *uint256.Int) *uint256.Int         { return nil }
func (m *filterChainStub) GetBlockByNumber(*uint256.Int) (block.IBlock, error) { return nil, nil }
func (m *filterChainStub) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}
func (m *filterChainStub) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}
func (m *filterChainStub) InsertHeader([]block.IHeader) (int, error)        { return 0, nil }
func (m *filterChainStub) GetBlockByHash(types.Hash) (block.IBlock, error)  { return nil, nil }
func (m *filterChainStub) Blocks() []block.IBlock                           { return nil }
func (m *filterChainStub) Start() error                                     { return nil }
func (m *filterChainStub) GenesisBlock() block.IBlock                       { return nil }
func (m *filterChainStub) NewBlockHandler([]byte, peer.ID) error            { return nil }
func (m *filterChainStub) InsertChain([]block.IBlock) (int, error)          { return 0, nil }
func (m *filterChainStub) InsertBlock([]block.IBlock, bool) (int, error)    { return 0, nil }
func (m *filterChainStub) SetEngine(interface{})                            {}
func (m *filterChainStub) GetBlocksFromHash(types.Hash, int) []block.IBlock { return nil }
func (m *filterChainStub) SealedBlock(block.IBlock) error                   { return nil }
func (m *filterChainStub) Engine() interface{}                              { return nil }
func (m *filterChainStub) GetReceipts(types.Hash) (block.Receipts, error)   { return nil, nil }
func (m *filterChainStub) GetLogs(types.Hash) ([][]*block.Log, error)       { return nil, nil }
func (m *filterChainStub) SetHead(uint64) error                             { return nil }
func (m *filterChainStub) AddFutureBlock(block.IBlock) error                { return nil }
func (m *filterChainStub) GetBlock(types.Hash, uint64) block.IBlock         { return nil }
func (m *filterChainStub) StateAt(kv.Tx, uint64) interface{}                { return nil }
func (m *filterChainStub) HasBlock(types.Hash, uint64) bool                 { return false }
func (m *filterChainStub) DB() kv.RwDB                                      { return nil }
func (m *filterChainStub) Quit() <-chan struct{}                            { return nil }
func (m *filterChainStub) EarliestBlock() uint64                            { return 0 }
func (m *filterChainStub) Close() error                                     { return nil }
func (m *filterChainStub) WriteBlockWithState(block.IBlock, []*block.Receipt, interface{}, map[types.Address]*uint256.Int) error {
	return nil
}

type filterAPIStub struct {
	bc common.IBlockChain
}

func (a *filterAPIStub) TxsPool() common.ITxsPool { return nil }
func (a *filterAPIStub) Database() kv.RwDB        { return nil }
func (a *filterAPIStub) Engine() consensus.Engine { return nil }
func (a *filterAPIStub) BlockChain() common.IBlockChain {
	return a.bc
}
func (a *filterAPIStub) GetEvm(context.Context, internal.Message, evmtypes.IntraBlockState, block.IHeader, *vm2.Config) (*vm2.EVM, func() error, error) {
	return nil, func() error { return nil }, nil
}

var _ common.IBlockChain = (*filterChainStub)(nil)
var _ Api = (*filterAPIStub)(nil)

func TestFilterLogsRejectsNilCurrentHeaderNumber(t *testing.T) {
	filter := NewRangeFilter(&filterAPIStub{
		bc: &filterChainStub{
			current: &filterBlockStub{header: &headerStub{}},
		},
	}, jsonrpc.LatestBlockNumber.Int64(), jsonrpc.LatestBlockNumber.Int64(), nil, nil)

	_, err := filter.Logs(context.Background())
	if err == nil || err.Error() != "current block number unavailable" {
		t.Fatalf("Logs() error = %v", err)
	}
}

func TestLightFilterNewHeadRejectsNilOldHeaderNumber(t *testing.T) {
	es := &EventSystem{
		lastHead: &headerStub{
			hash: types.HexToHash("0x01"),
		},
	}
	newHeader := &headerStub{
		hash:   types.HexToHash("0x02"),
		number: uint256.NewInt(1),
	}

	called := false
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lightFilterNewHead panicked: %v", r)
		}
	}()
	es.lightFilterNewHead(newHeader, func(block.IHeader, bool) {
		called = true
	})

	if called {
		t.Fatal("callback should not be called for missing old header number")
	}
}

func TestLightFilterNewHeadRejectsNilNewHeaderNumber(t *testing.T) {
	es := &EventSystem{
		lastHead: &headerStub{
			hash:   types.HexToHash("0x01"),
			number: uint256.NewInt(1),
		},
	}
	newHeader := &headerStub{
		hash: types.HexToHash("0x02"),
	}

	called := false
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("lightFilterNewHead panicked: %v", r)
		}
	}()
	es.lightFilterNewHead(newHeader, func(block.IHeader, bool) {
		called = true
	})

	if called {
		t.Fatal("callback should not be called for missing new header number")
	}
}
