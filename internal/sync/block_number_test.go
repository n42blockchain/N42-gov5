package sync

import (
	"testing"
	"time"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type syncBlockStub struct {
	number *uint256.Int
}

type syncChainStub struct {
	common.IBlockChain
	current block.IBlock
}

func (b *syncBlockStub) Header() block.IHeader                           { return b }
func (b *syncBlockStub) Body() block.IBody                               { return nil }
func (b *syncBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *syncBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *syncBlockStub) Number64() *uint256.Int                          { return b.number }
func (b *syncBlockStub) BaseFee64() *uint256.Int                         { return uint256.NewInt(0) }
func (b *syncBlockStub) Difficulty() *uint256.Int                        { return uint256.NewInt(1) }
func (b *syncBlockStub) Time() uint64                                    { return uint64(time.Now().Unix()) }
func (b *syncBlockStub) GasLimit() uint64                                { return 0 }
func (b *syncBlockStub) GasUsed() uint64                                 { return 0 }
func (b *syncBlockStub) Nonce() uint64                                   { return 0 }
func (b *syncBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *syncBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *syncBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *syncBlockStub) Hash() types.Hash                                { return types.Hash{} }
func (b *syncBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *syncBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *syncBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *syncBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *syncBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *syncBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

func (s *syncChainStub) CurrentBlock() block.IBlock {
	return s.current
}

func TestRequireBlockNumberRejectsNilNumber(t *testing.T) {
	_, err := requireBlockNumber(&syncBlockStub{}, "block number unavailable")
	if err == nil || err.Error() != "block number unavailable" {
		t.Fatalf("requireBlockNumber() error = %v", err)
	}
}

func TestRequireHeaderNumberRejectsNilNumber(t *testing.T) {
	_, err := requireHeaderNumber(&block.Header{}, "header number unavailable")
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("requireHeaderNumber() error = %v", err)
	}
}

func TestBlockNumberOrZeroHandlesNilNumber(t *testing.T) {
	if got := blockNumberOrZero(&syncBlockStub{}); got != 0 {
		t.Fatalf("blockNumberOrZero() = %d, want 0", got)
	}
}

func TestBlockNumberOrZeroReturnsBlockNumber(t *testing.T) {
	if got := blockNumberOrZero(&syncBlockStub{number: uint256.NewInt(7)}); got != 7 {
		t.Fatalf("blockNumberOrZero() = %d, want 7", got)
	}
}

func TestCurrentBlockNumberOrZeroHandlesNilCurrentBlockNumber(t *testing.T) {
	chain := &syncChainStub{current: &syncBlockStub{}}
	if got := currentBlockNumberOrZero(chain); got != 0 {
		t.Fatalf("currentBlockNumberOrZero() = %d, want 0", got)
	}
}

func TestUpdateMetricsHandlesNilHeaderNumber(t *testing.T) {
	svc := &Service{
		cfg: &config{
			chain: &syncChainStub{current: &syncBlockStub{}},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("updateMetrics() panicked: %v", r)
		}
	}()

	svc.updateMetrics()
}
