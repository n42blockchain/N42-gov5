package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type apiTestEngine struct{}

func (e *apiTestEngine) Author(header block.IHeader) (types.Address, error) {
	return types.Address{}, nil
}
func (e *apiTestEngine) IsServiceTransaction(sender types.Address, syscall consensus.SystemCall) bool {
	return false
}
func (e *apiTestEngine) Type() params.ConsensusType { return params.AposConsensu }
func (e *apiTestEngine) VerifyHeader(chain consensus.ChainHeaderReader, header block.IHeader, seal bool) error {
	return nil
}
func (e *apiTestEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	quit := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	close(results)
	return quit, results
}
func (e *apiTestEngine) VerifyUncles(chain consensus.ConsensusChainReader, block block.IBlock) error {
	return nil
}
func (e *apiTestEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	return nil
}
func (e *apiTestEngine) Finalize(chain consensus.ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil
}
func (e *apiTestEngine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil, nil
}
func (e *apiTestEngine) Seal(chain consensus.ChainHeaderReader, block block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	return nil
}
func (e *apiTestEngine) SealHash(header block.IHeader) types.Hash { return types.Hash{} }
func (e *apiTestEngine) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(0)
}
func (e *apiTestEngine) APIs(chain consensus.ConsensusChainReader) []jsonrpc.API { return nil }
func (e *apiTestEngine) Close() error                                            { return nil }

var _ consensus.Engine = (*apiTestEngine)(nil)

type apiHeaderStub struct{}

func (h *apiHeaderStub) Number64() *uint256.Int               { return uint256.NewInt(0) }
func (h *apiHeaderStub) BaseFee64() *uint256.Int              { return uint256.NewInt(0) }
func (h *apiHeaderStub) Hash() types.Hash                     { return types.Hash{} }
func (h *apiHeaderStub) ToProtoMessage() proto.Message        { return nil }
func (h *apiHeaderStub) FromProtoMessage(proto.Message) error { return nil }
func (h *apiHeaderStub) Marshal() ([]byte, error)             { return nil, nil }
func (h *apiHeaderStub) Unmarshal([]byte) error               { return nil }
func (h *apiHeaderStub) StateRoot() types.Hash                { return types.Hash{} }

func TestGetEvmAcceptsNilConfig(t *testing.T) {
	api := &API{
		engine:      &apiTestEngine{},
		chainConfig: &params.ChainConfig{},
	}
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, false, false)

	evm, vmError, err := api.GetEvm(context.Background(), msg, nil, header, nil)
	if err != nil {
		t.Fatalf("GetEvm() error = %v", err)
	}
	if evm == nil {
		t.Fatal("GetEvm() returned nil EVM")
	}
	if vmError == nil {
		t.Fatal("GetEvm() returned nil vmError callback")
	}
}

func TestGetEvmRejectsUnexpectedHeaderType(t *testing.T) {
	api := &API{
		engine:      &apiTestEngine{},
		chainConfig: &params.ChainConfig{},
	}
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, false, false)

	_, _, err := api.GetEvm(context.Background(), msg, nil, &apiHeaderStub{}, nil)
	if err == nil || err.Error() != "GetEvm: invalid header type assertion" {
		t.Fatalf("GetEvm() error = %v", err)
	}
}

func TestHeaderBaseFeeBigAllowsNilBaseFee(t *testing.T) {
	if got := headerBaseFeeBig(nil); got != nil {
		t.Fatalf("headerBaseFeeBig(nil) = %v, want nil", got)
	}

	if got := headerBaseFeeBig(&block.Header{}); got != nil {
		t.Fatalf("headerBaseFeeBig(empty header) = %v, want nil", got)
	}

	header := &block.Header{BaseFee: uint256.NewInt(7)}
	if got := headerBaseFeeBig(header); got == nil || got.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("headerBaseFeeBig(header) = %v, want 7", got)
	}
}

func TestUint256ToBigOrZeroAllowsNil(t *testing.T) {
	if got := uint256ToBigOrZero(nil); got == nil || got.Sign() != 0 {
		t.Fatalf("uint256ToBigOrZero(nil) = %v, want 0", got)
	}

	if got := uint256ToBigOrZero(uint256.NewInt(9)); got == nil || got.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("uint256ToBigOrZero(9) = %v, want 9", got)
	}
}

func TestUint256ToUint64OrZeroAllowsNil(t *testing.T) {
	if got := uint256ToUint64OrZero(nil); got != 0 {
		t.Fatalf("uint256ToUint64OrZero(nil) = %d, want 0", got)
	}

	if got := uint256ToUint64OrZero(uint256.NewInt(9)); got != 9 {
		t.Fatalf("uint256ToUint64OrZero(9) = %d, want 9", got)
	}
}
