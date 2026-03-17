package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/params"
)

type gasPriceBlockStub struct {
	header block.IHeader
}

func (b *gasPriceBlockStub) Number64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.Number64()
}
func (b *gasPriceBlockStub) BaseFee64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.BaseFee64()
}
func (b *gasPriceBlockStub) Hash() types.Hash {
	if b.header == nil {
		return types.Hash{}
	}
	return b.header.Hash()
}
func (b *gasPriceBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *gasPriceBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *gasPriceBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *gasPriceBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *gasPriceBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *gasPriceBlockStub) Header() block.IHeader                           { return b.header }
func (b *gasPriceBlockStub) Body() block.IBody                               { return nil }
func (b *gasPriceBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *gasPriceBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *gasPriceBlockStub) Difficulty() *uint256.Int                        { return nil }
func (b *gasPriceBlockStub) Time() uint64                                    { return 0 }
func (b *gasPriceBlockStub) GasLimit() uint64                                { return 0 }
func (b *gasPriceBlockStub) GasUsed() uint64                                 { return 0 }
func (b *gasPriceBlockStub) Nonce() uint64                                   { return 0 }
func (b *gasPriceBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *gasPriceBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *gasPriceBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *gasPriceBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

type gasPriceChainStub struct {
	current  block.IBlock
	headers  map[uint64]block.IHeader
	earliest uint64
}

func (m *gasPriceChainStub) Config() *params.ChainConfig { return nil }
func (m *gasPriceChainStub) CurrentBlock() block.IBlock  { return m.current }
func (m *gasPriceChainStub) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	return nil
}
func (m *gasPriceChainStub) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	if number == nil {
		return nil
	}
	if m.headers == nil {
		return nil
	}
	return m.headers[number.Uint64()]
}
func (m *gasPriceChainStub) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	return nil, nil
}
func (m *gasPriceChainStub) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (m *gasPriceChainStub) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	return nil, nil
}
func (m *gasPriceChainStub) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}
func (m *gasPriceChainStub) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}
func (m *gasPriceChainStub) InsertHeader(headers []block.IHeader) (int, error) { return 0, nil }
func (m *gasPriceChainStub) GetBlockByHash(types.Hash) (block.IBlock, error)   { return nil, nil }
func (m *gasPriceChainStub) Blocks() []block.IBlock                            { return nil }
func (m *gasPriceChainStub) Start() error                                      { return nil }
func (m *gasPriceChainStub) GenesisBlock() block.IBlock                        { return nil }
func (m *gasPriceChainStub) NewBlockHandler([]byte, peer.ID) error             { return nil }
func (m *gasPriceChainStub) InsertChain([]block.IBlock) (int, error)           { return 0, nil }
func (m *gasPriceChainStub) InsertBlock([]block.IBlock, bool) (int, error)     { return 0, nil }
func (m *gasPriceChainStub) SetEngine(interface{})                             {}
func (m *gasPriceChainStub) GetBlocksFromHash(types.Hash, int) []block.IBlock  { return nil }
func (m *gasPriceChainStub) SealedBlock(block.IBlock) error                    { return nil }
func (m *gasPriceChainStub) Engine() interface{}                               { return nil }
func (m *gasPriceChainStub) GetReceipts(types.Hash) (block.Receipts, error)    { return nil, nil }
func (m *gasPriceChainStub) GetLogs(types.Hash) ([][]*block.Log, error)        { return nil, nil }
func (m *gasPriceChainStub) SetHead(uint64) error                              { return nil }
func (m *gasPriceChainStub) AddFutureBlock(block.IBlock) error                 { return nil }
func (m *gasPriceChainStub) GetBlock(types.Hash, uint64) block.IBlock          { return nil }
func (m *gasPriceChainStub) StateAt(kv.Tx, uint64) interface{}                 { return nil }
func (m *gasPriceChainStub) HasBlock(types.Hash, uint64) bool                  { return false }
func (m *gasPriceChainStub) DB() kv.RwDB                                       { return nil }
func (m *gasPriceChainStub) Quit() <-chan struct{}                             { return nil }
func (m *gasPriceChainStub) EarliestBlock() uint64                             { return m.earliest }
func (m *gasPriceChainStub) Close() error                                      { return nil }
func (m *gasPriceChainStub) WriteBlockWithState(block.IBlock, []*block.Receipt, interface{}, map[types.Address]*uint256.Int) error {
	return nil
}

var _ common.IBlockChain = (*gasPriceChainStub)(nil)

func TestSuggestTipCapAllowsNilCurrentHeader(t *testing.T) {
	oracle := &Oracle{
		backend:   &gasPriceChainStub{current: &gasPriceBlockStub{}},
		lastPrice: big.NewInt(77),
	}

	tip, err := oracle.SuggestTipCap(context.Background(), &params.ChainConfig{})
	if err != nil {
		t.Fatalf("SuggestTipCap() error = %v", err)
	}
	if tip.Cmp(big.NewInt(77)) != 0 {
		t.Fatalf("SuggestTipCap() = %v, want 77", tip)
	}
}

func TestSuggestTipCapAllowsNilCurrentHeaderNumber(t *testing.T) {
	oracle := &Oracle{
		backend:   &gasPriceChainStub{current: &gasPriceBlockStub{header: &block.Header{}}},
		lastPrice: big.NewInt(88),
	}

	tip, err := oracle.SuggestTipCap(context.Background(), &params.ChainConfig{})
	if err != nil {
		t.Fatalf("SuggestTipCap() error = %v", err)
	}
	if tip.Cmp(big.NewInt(88)) != 0 {
		t.Fatalf("SuggestTipCap() = %v, want 88", tip)
	}
}
