package internal

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type nonConcreteHeaderStub struct{}

func (h *nonConcreteHeaderStub) Number64() *uint256.Int               { return uint256.NewInt(1) }
func (h *nonConcreteHeaderStub) BaseFee64() *uint256.Int              { return uint256.NewInt(0) }
func (h *nonConcreteHeaderStub) Hash() types.Hash                     { return types.Hash{} }
func (h *nonConcreteHeaderStub) ToProtoMessage() proto.Message        { return nil }
func (h *nonConcreteHeaderStub) FromProtoMessage(proto.Message) error { return nil }
func (h *nonConcreteHeaderStub) Marshal() ([]byte, error)             { return nil, nil }
func (h *nonConcreteHeaderStub) Unmarshal([]byte) error               { return nil }
func (h *nonConcreteHeaderStub) StateRoot() types.Hash                { return types.Hash{} }

type nonConcreteBlockStub struct {
	header block.IHeader
	body   block.IBody
}

func (b *nonConcreteBlockStub) Number64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.Number64()
}
func (b *nonConcreteBlockStub) BaseFee64() *uint256.Int {
	if b.header == nil {
		return nil
	}
	return b.header.BaseFee64()
}
func (b *nonConcreteBlockStub) Hash() types.Hash {
	if b.header == nil {
		return types.Hash{}
	}
	return b.header.Hash()
}
func (b *nonConcreteBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *nonConcreteBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *nonConcreteBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *nonConcreteBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *nonConcreteBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *nonConcreteBlockStub) Header() block.IHeader                           { return b.header }
func (b *nonConcreteBlockStub) Body() block.IBody                               { return b.body }
func (b *nonConcreteBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *nonConcreteBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *nonConcreteBlockStub) Difficulty() *uint256.Int                        { return uint256.NewInt(1) }
func (b *nonConcreteBlockStub) Time() uint64                                    { return uint64(time.Now().Unix()) }
func (b *nonConcreteBlockStub) GasLimit() uint64                                { return 0 }
func (b *nonConcreteBlockStub) GasUsed() uint64                                 { return 0 }
func (b *nonConcreteBlockStub) Nonce() uint64                                   { return 0 }
func (b *nonConcreteBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *nonConcreteBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *nonConcreteBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *nonConcreteBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

func TestRequireConcreteBlockRejectsUnexpectedType(t *testing.T) {
	_, err := requireConcreteBlock(&nonConcreteBlockStub{}, "unexpected block type")
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("requireConcreteBlock() error = %v", err)
	}
}

func TestRequireConcreteHeaderRejectsUnexpectedType(t *testing.T) {
	_, err := requireConcreteHeader(&nonConcreteHeaderStub{}, "unexpected header type")
	if err == nil || err.Error() != "unexpected header type" {
		t.Fatalf("requireConcreteHeader() error = %v", err)
	}
}

func TestRequireBlockNumberRejectsNilBlockNumber(t *testing.T) {
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	_, err := requireBlockNumber(blk, "block number unavailable")
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("requireBlockNumber() error = %v", err)
	}
}

func TestRequireHeaderNumberRejectsNilHeaderNumber(t *testing.T) {
	header := &block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}

	_, err := requireHeaderNumber(header, "header number unavailable")
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("requireHeaderNumber() error = %v", err)
	}
}

func TestAddFutureBlockRejectsUnexpectedType(t *testing.T) {
	cache, _ := lru.New[types.Hash, *block.Block](1)
	bc := &BlockChain{futureBlocks: cache}
	blk := &nonConcreteBlockStub{
		header: &block.Header{
			Number:     uint256.NewInt(1),
			Difficulty: uint256.NewInt(1),
			BaseFee:    uint256.NewInt(0),
		},
		body: &block.Body{},
	}

	err := bc.AddFutureBlock(blk)
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("AddFutureBlock() error = %v", err)
	}
}

func TestPersistBlockRejectsUnexpectedTypeWithoutPanicking(t *testing.T) {
	bc := &BlockChain{ctx: context.Background()}
	blk := &nonConcreteBlockStub{
		header: &block.Header{
			Number:     uint256.NewInt(1),
			Difficulty: uint256.NewInt(1),
			BaseFee:    uint256.NewInt(0),
		},
		body: &block.Body{},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("persistBlock panicked: %v", r)
		}
	}()
	bc.persistBlock(nil, blk, "test")
}

func TestWriteBlockWithoutStateRejectsUnexpectedType(t *testing.T) {
	bc := &BlockChain{}

	err := bc.WriteBlockWithoutState(&nonConcreteBlockStub{})
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("WriteBlockWithoutState() error = %v", err)
	}
}

func TestWriteBlockWithTdRejectsUnexpectedType(t *testing.T) {
	bc := &BlockChain{}

	err := bc.writeBlockWithTd(&nonConcreteBlockStub{}, uint256.NewInt(1))
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("writeBlockWithTd() error = %v", err)
	}
}

func TestWriteBlockWithStateRejectsUnexpectedType(t *testing.T) {
	bc := &BlockChain{}

	status, err := bc.writeBlockWithState(&nonConcreteBlockStub{}, nil, nil, nil)
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("writeBlockWithState() error = %v", err)
	}
	if status != NonStatTy {
		t.Fatalf("writeBlockWithState() status = %v, want %v", status, NonStatTy)
	}
}

func TestWriteHeadBlockRejectsUnexpectedType(t *testing.T) {
	bc := &BlockChain{}

	err := bc.writeHeadBlock(nil, &nonConcreteBlockStub{})
	if err == nil || err.Error() != "unexpected block type" {
		t.Fatalf("writeHeadBlock() error = %v", err)
	}
}

func TestWriteHeadBlockCommitsAfterContextCancellation(t *testing.T) {
	modules.N42Init()
	previousCfg := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = previousCfg
	})

	db := memdb.NewTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	blk := testConcreteBlock(&block.Header{
		Number:     uint256.NewInt(7),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})
	bc := &BlockChain{
		ctx:     ctx,
		ChainDB: db,
	}

	if err := bc.writeHeadBlock(nil, blk); err != nil {
		t.Fatalf("writeHeadBlock() error = %v", err)
	}

	tx := memdb.BeginRo(t, db)
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash != blk.Hash() {
		t.Fatalf("ReadHeadBlockHash() = %s, want %s", headHash, blk.Hash())
	}
	canonicalHash, err := rawdb.ReadCanonicalHash(tx, 7)
	if err != nil {
		t.Fatalf("ReadCanonicalHash() error = %v", err)
	}
	if canonicalHash != blk.Hash() {
		t.Fatalf("ReadCanonicalHash() = %s, want %s", canonicalHash, blk.Hash())
	}
	current := bc.CurrentBlock()
	if current == nil || current.Hash() != blk.Hash() {
		t.Fatalf("CurrentBlock() = %v, want hash %s", current, blk.Hash())
	}
}

func TestNewBlockChainRejectsUnexpectedGenesisType(t *testing.T) {
	_, err := NewBlockChain(context.Background(), &nonConcreteBlockStub{}, nil, nil, nil, nil)
	if err == nil || err.Error() != "unexpected genesis block type" {
		t.Fatalf("NewBlockChain() error = %v", err)
	}
}

func TestAddFutureBlockRejectsNilBlockNumber(t *testing.T) {
	cache, _ := lru.New[types.Hash, *block.Block](1)
	bc := &BlockChain{futureBlocks: cache}
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	err := bc.AddFutureBlock(blk)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("AddFutureBlock() error = %v", err)
	}
}

func TestWriteBlockWithoutStateRejectsNilBlockNumber(t *testing.T) {
	bc := &BlockChain{}
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	err := bc.WriteBlockWithoutState(blk)
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("WriteBlockWithoutState() error = %v", err)
	}
}

func TestNewBlockChainRejectsNilGenesisBlockNumber(t *testing.T) {
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	_, err := NewBlockChain(context.Background(), blk, nil, nil, nil, nil)
	if err == nil || err.Error() != "genesis block number unavailable" {
		t.Fatalf("NewBlockChain() error = %v", err)
	}
}

func TestProcessFutureBlocksDropsNilNumberBlockWithoutPanicking(t *testing.T) {
	cache, _ := lru.New[types.Hash, *block.Block](1)
	current := testConcreteBlock(&block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})
	future := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})
	cache.Add(types.Hash{1}, future)

	bc := &BlockChain{futureBlocks: cache}
	bc.currentBlock.Store(current)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("processFutureBlocks panicked: %v", r)
		}
	}()
	bc.processFutureBlocks()

	if bc.futureBlocks.Len() != 0 {
		t.Fatalf("futureBlocks len = %d, want 0", bc.futureBlocks.Len())
	}
}

func TestRecoverAncestorsRejectsNilBlockNumber(t *testing.T) {
	bc := &BlockChain{}
	blk := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	_, err := bc.recoverAncestors(blk)
	if err == nil || err.Error() != "ancestor block number unavailable" {
		t.Fatalf("recoverAncestors() error = %v", err)
	}
}

func TestReorgRejectsNilOldBlockNumber(t *testing.T) {
	bc := &BlockChain{}
	oldBlock := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})
	newBlock := testConcreteBlock(&block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	err := bc.reorg(nil, oldBlock, newBlock)
	if err == nil || err.Error() != "old block number unavailable" {
		t.Fatalf("reorg() error = %v", err)
	}
}

func TestReorgRejectsNilNewBlockNumber(t *testing.T) {
	bc := &BlockChain{}
	oldBlock := testConcreteBlock(&block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})
	newBlock := testConcreteBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, &block.Body{})

	err := bc.reorg(nil, oldBlock, newBlock)
	if err == nil || err.Error() != "new block number unavailable" {
		t.Fatalf("reorg() error = %v", err)
	}
}

func testConcreteBlock(header *block.Header, body *block.Body) *block.Block {
	blk := &block.Block{}
	setBlockField(blk, "header", header)
	setBlockField(blk, "body", body)
	return blk
}

func setBlockField(target interface{}, name string, value interface{}) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
