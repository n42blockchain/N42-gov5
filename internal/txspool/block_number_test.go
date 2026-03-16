package txspool

import (
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/params"
)

type txpoolHeaderStub struct {
	number *uint256.Int
	hash   types.Hash
}

type txpoolBodyStub struct {
	txs []*transaction.Transaction
}

type txpoolBlockStub struct {
	header     block.IHeader
	body       block.IBody
	parentHash types.Hash
	gasLimit   uint64
}

type txpoolChainStub struct {
	common.IBlockChain
	current block.IBlock
	blocks  map[types.Hash]block.IBlock
}

func (h *txpoolHeaderStub) Number64() *uint256.Int               { return h.number }
func (h *txpoolHeaderStub) BaseFee64() *uint256.Int              { return uint256.NewInt(0) }
func (h *txpoolHeaderStub) Hash() types.Hash                     { return h.hash }
func (h *txpoolHeaderStub) ToProtoMessage() proto.Message        { return nil }
func (h *txpoolHeaderStub) FromProtoMessage(proto.Message) error { return nil }
func (h *txpoolHeaderStub) Marshal() ([]byte, error)             { return nil, nil }
func (h *txpoolHeaderStub) Unmarshal([]byte) error               { return nil }
func (h *txpoolHeaderStub) StateRoot() types.Hash                { return types.Hash{} }

func (b *txpoolBodyStub) Verifier() []*block.Verify                { return nil }
func (b *txpoolBodyStub) Reward() []*block.Reward                  { return nil }
func (b *txpoolBodyStub) Transactions() []*transaction.Transaction { return b.txs }
func (b *txpoolBodyStub) ZKProof() []byte                          { return nil }
func (b *txpoolBodyStub) ToProtoMessage() proto.Message            { return nil }
func (b *txpoolBodyStub) FromProtoMessage(proto.Message) error     { return nil }

func (b *txpoolBlockStub) Header() block.IHeader                           { return b.header }
func (b *txpoolBlockStub) Body() block.IBody                               { return b.body }
func (b *txpoolBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *txpoolBlockStub) Transactions() []*transaction.Transaction {
	if b.body == nil {
		return nil
	}
	return b.body.Transactions()
}
func (b *txpoolBlockStub) Number64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.Number64()
}
func (b *txpoolBlockStub) BaseFee64() *uint256.Int {
	if b.header == nil {
		return uint256.NewInt(0)
	}
	return b.header.BaseFee64()
}
func (b *txpoolBlockStub) Difficulty() *uint256.Int             { return uint256.NewInt(1) }
func (b *txpoolBlockStub) Time() uint64                         { return 0 }
func (b *txpoolBlockStub) GasLimit() uint64                     { return b.gasLimit }
func (b *txpoolBlockStub) GasUsed() uint64                      { return 0 }
func (b *txpoolBlockStub) Nonce() uint64                        { return 0 }
func (b *txpoolBlockStub) Coinbase() types.Address              { return types.Address{} }
func (b *txpoolBlockStub) ParentHash() types.Hash               { return b.parentHash }
func (b *txpoolBlockStub) TxHash() types.Hash                   { return types.Hash{} }
func (b *txpoolBlockStub) WithSeal(block.IHeader) *block.Block  { return nil }
func (b *txpoolBlockStub) Hash() types.Hash                     { return b.header.Hash() }
func (b *txpoolBlockStub) ToProtoMessage() proto.Message        { return nil }
func (b *txpoolBlockStub) FromProtoMessage(proto.Message) error { return nil }
func (b *txpoolBlockStub) Marshal() ([]byte, error)             { return nil, nil }
func (b *txpoolBlockStub) Unmarshal([]byte) error               { return nil }
func (b *txpoolBlockStub) StateRoot() types.Hash                { return types.Hash{} }

func (c *txpoolChainStub) CurrentBlock() block.IBlock { return c.current }
func (c *txpoolChainStub) GetBlockByHash(hash types.Hash) (block.IBlock, error) {
	if c.blocks == nil {
		return nil, nil
	}
	return c.blocks[hash], nil
}
func (c *txpoolChainStub) DB() kv.RwDB { return nil }

func TestResetHandlesNilNewBlock(t *testing.T) {
	current := &txpoolBlockStub{
		header:   &txpoolHeaderStub{number: uint256.NewInt(5), hash: types.HexToHash("0x5")},
		gasLimit: 15_000_000,
	}
	pool := &TxsPool{
		bc:          &txpoolChainStub{current: current},
		chainconfig: params.TestChainConfig,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reset() panicked: %v", r)
		}
	}()

	pool.reset(nil, nil)

	if pool.currentMaxGas != current.GasLimit() {
		t.Fatalf("currentMaxGas = %d, want %d", pool.currentMaxGas, current.GasLimit())
	}
}

func TestResetHandlesNilHeadNumbers(t *testing.T) {
	oldBlock := &txpoolBlockStub{
		header:   &txpoolHeaderStub{hash: types.HexToHash("0x1")},
		gasLimit: 10_000_000,
	}
	newBlock := &txpoolBlockStub{
		header:     &txpoolHeaderStub{hash: types.HexToHash("0x2")},
		parentHash: types.HexToHash("0x3"),
		gasLimit:   12_000_000,
	}
	pool := &TxsPool{
		bc:          &txpoolChainStub{current: newBlock},
		chainconfig: params.TestChainConfig,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reset() panicked: %v", r)
		}
	}()

	pool.reset(oldBlock, newBlock)

	if pool.currentMaxGas != newBlock.GasLimit() {
		t.Fatalf("currentMaxGas = %d, want %d", pool.currentMaxGas, newBlock.GasLimit())
	}
}

func TestResetHandlesNilAncestorBlockNumber(t *testing.T) {
	ancestorHash := types.HexToHash("0xaa")
	oldBlock := &txpoolBlockStub{
		header:     &txpoolHeaderStub{number: uint256.NewInt(2), hash: types.HexToHash("0x10")},
		parentHash: ancestorHash,
		body:       &txpoolBodyStub{},
		gasLimit:   10_000_000,
	}
	newBlock := &txpoolBlockStub{
		header:     &txpoolHeaderStub{number: uint256.NewInt(1), hash: types.HexToHash("0x20")},
		parentHash: types.HexToHash("0x30"),
		body:       &txpoolBodyStub{},
		gasLimit:   12_000_000,
	}
	ancestor := &txpoolBlockStub{
		header:   &txpoolHeaderStub{hash: ancestorHash},
		body:     &txpoolBodyStub{},
		gasLimit: 9_000_000,
	}
	pool := &TxsPool{
		bc: &txpoolChainStub{
			current: newBlock,
			blocks: map[types.Hash]block.IBlock{
				ancestorHash: ancestor,
			},
		},
		chainconfig: params.TestChainConfig,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("reset() panicked: %v", r)
		}
	}()

	pool.reset(oldBlock, newBlock)

	if pool.currentMaxGas != newBlock.GasLimit() {
		t.Fatalf("currentMaxGas = %d, want %d", pool.currentMaxGas, newBlock.GasLimit())
	}
}
