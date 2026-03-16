package initialsync

import (
	"testing"
	"time"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
)

type initialSyncBlockStub struct {
	number *uint256.Int
}

type initialSyncChainStub struct {
	common.IBlockChain
	current block.IBlock
}

type initialSyncP2PStub struct {
	p2p.P2P
	cfg *conf.P2PConfig
}

func (b *initialSyncBlockStub) Header() block.IHeader                           { return b }
func (b *initialSyncBlockStub) Body() block.IBody                               { return nil }
func (b *initialSyncBlockStub) Transaction(types.Hash) *transaction.Transaction { return nil }
func (b *initialSyncBlockStub) Transactions() []*transaction.Transaction        { return nil }
func (b *initialSyncBlockStub) Number64() *uint256.Int                          { return b.number }
func (b *initialSyncBlockStub) BaseFee64() *uint256.Int                         { return uint256.NewInt(0) }
func (b *initialSyncBlockStub) Difficulty() *uint256.Int                        { return uint256.NewInt(1) }
func (b *initialSyncBlockStub) Time() uint64                                    { return uint64(time.Now().Unix()) }
func (b *initialSyncBlockStub) GasLimit() uint64                                { return 0 }
func (b *initialSyncBlockStub) GasUsed() uint64                                 { return 0 }
func (b *initialSyncBlockStub) Nonce() uint64                                   { return 0 }
func (b *initialSyncBlockStub) Coinbase() types.Address                         { return types.Address{} }
func (b *initialSyncBlockStub) ParentHash() types.Hash                          { return types.Hash{} }
func (b *initialSyncBlockStub) TxHash() types.Hash                              { return types.Hash{} }
func (b *initialSyncBlockStub) Hash() types.Hash                                { return types.Hash{} }
func (b *initialSyncBlockStub) ToProtoMessage() proto.Message                   { return nil }
func (b *initialSyncBlockStub) FromProtoMessage(proto.Message) error            { return nil }
func (b *initialSyncBlockStub) Marshal() ([]byte, error)                        { return nil, nil }
func (b *initialSyncBlockStub) Unmarshal([]byte) error                          { return nil }
func (b *initialSyncBlockStub) StateRoot() types.Hash                           { return types.Hash{} }
func (b *initialSyncBlockStub) WithSeal(block.IHeader) *block.Block             { return nil }

func (s *initialSyncChainStub) CurrentBlock() block.IBlock {
	return s.current
}

func (s *initialSyncP2PStub) GetConfig() *conf.P2PConfig {
	return s.cfg
}

func TestCurrentBlockNumberHandlesNilCurrentBlockNumber(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{}}
	if got := currentBlockNumber(chain).Uint64(); got != 0 {
		t.Fatalf("currentBlockNumber() = %d, want 0", got)
	}
}

func TestServiceShouldSkipPeerWaitHandlesNilCurrentBlockNumber(t *testing.T) {
	svc := &Service{
		cfg: &Config{
			Chain: &initialSyncChainStub{current: &initialSyncBlockStub{}},
			P2P: &initialSyncP2PStub{
				cfg: &conf.P2PConfig{MinSyncPeers: 1},
			},
		},
	}

	if !svc.shouldSkipPeerWait() {
		t.Fatal("shouldSkipPeerWait() = false, want true for nil current block number without bootstrap peers")
	}
}

func TestBlocksFetcherShouldSkipPeerWaitHandlesNilCurrentBlockNumber(t *testing.T) {
	fetcher := &blocksFetcher{
		chain: &initialSyncChainStub{current: &initialSyncBlockStub{}},
		p2p: &initialSyncP2PStub{
			cfg: &conf.P2PConfig{MinSyncPeers: 1},
		},
	}

	if !fetcher.shouldSkipPeerWait() {
		t.Fatal("shouldSkipPeerWait() = false, want true for nil current block number without bootstrap peers")
	}
}

func TestRequireCurrentBlockNumberRejectsNilCurrentBlockNumber(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{}}
	_, err := requireCurrentBlockNumber(chain, "current block number unavailable")
	if err == nil || err.Error() != "current block number unavailable" {
		t.Fatalf("requireCurrentBlockNumber() error = %v", err)
	}
}
