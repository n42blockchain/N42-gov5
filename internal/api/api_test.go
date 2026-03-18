package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
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

type apiStateReaderStub struct{}

func (s *apiStateReaderStub) ReadAccountData(types.Address) (*account.StateAccount, error) {
	return nil, nil
}
func (s *apiStateReaderStub) ReadAccountStorage(types.Address, uint16, *types.Hash) ([]byte, error) {
	return nil, nil
}
func (s *apiStateReaderStub) ReadAccountCode(types.Address, uint16, types.Hash) ([]byte, error) {
	return nil, nil
}
func (s *apiStateReaderStub) ReadAccountCodeSize(types.Address, uint16, types.Hash) (int, error) {
	return 0, nil
}
func (s *apiStateReaderStub) ReadAccountIncarnation(types.Address) (uint16, error) {
	return 0, nil
}

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
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, nil, nil, false, false)

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
	msg := transaction.NewMessage(types.Address{}, nil, 0, uint256.NewInt(0), 21000, uint256.NewInt(0), nil, nil, nil, nil, nil, nil, false, false)

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

func TestUint256FromBigAllowsNil(t *testing.T) {
	got, err := uint256FromBig(nil)
	if err != nil {
		t.Fatalf("uint256FromBig(nil) error = %v", err)
	}
	if got != nil {
		t.Fatalf("uint256FromBig(nil) = %v, want nil", got)
	}
}

func TestUint256FromBigRejectsNegative(t *testing.T) {
	_, err := uint256FromBig(big.NewInt(-1))
	if err == nil || err.Error() != "value must be non-negative" {
		t.Fatalf("uint256FromBig(-1) error = %v", err)
	}
}

func TestUint256FromBigRejectsOverflow(t *testing.T) {
	overflow := new(big.Int).Lsh(big.NewInt(1), 256)
	_, err := uint256FromBig(overflow)
	if err == nil || err.Error() != "value overflows uint256" {
		t.Fatalf("uint256FromBig(2^256) error = %v", err)
	}
}

func TestUint256FromBigAcceptsValue(t *testing.T) {
	got, err := uint256FromBig(big.NewInt(9))
	if err != nil {
		t.Fatalf("uint256FromBig(9) error = %v", err)
	}
	if got == nil || got.Uint64() != 9 {
		t.Fatalf("uint256FromBig(9) = %v, want 9", got)
	}
}

func TestRequireIntraBlockStateRejectsUnexpectedType(t *testing.T) {
	_, err := requireIntraBlockState(struct{}{}, "unexpected state type")
	if err == nil || err.Error() != "unexpected state type" {
		t.Fatalf("requireIntraBlockState() error = %v", err)
	}
}

func TestRequirePlainStateReaderRejectsUnexpectedReaderType(t *testing.T) {
	statedb := state.New(&apiStateReaderStub{})

	_, err := requirePlainStateReader(statedb, "unexpected state reader type")
	if err == nil || err.Error() != "unexpected state reader type" {
		t.Fatalf("requirePlainStateReader() error = %v", err)
	}
}

func TestRequirePlainStateReaderAcceptsPlainState(t *testing.T) {
	statedb := state.New(&state.PlainState{})

	reader, err := requirePlainStateReader(statedb, "unexpected state reader type")
	if err != nil {
		t.Fatalf("requirePlainStateReader() error = %v", err)
	}
	if reader == nil {
		t.Fatal("requirePlainStateReader() returned nil reader")
	}
}

func TestChainIDUint64AllowsMissingChainID(t *testing.T) {
	if got := chainIDUint64(nil); got != 0 {
		t.Fatalf("chainIDUint64(nil) = %d, want 0", got)
	}

	if got := chainIDUint64(&params.ChainConfig{}); got != 0 {
		t.Fatalf("chainIDUint64(empty cfg) = %d, want 0", got)
	}

	cfg := &params.ChainConfig{ChainID: big.NewInt(9)}
	if got := chainIDUint64(cfg); got != 9 {
		t.Fatalf("chainIDUint64(cfg) = %d, want 9", got)
	}
}

func TestBlockChainAPIChainIdAllowsMissingConfig(t *testing.T) {
	if got := (&BlockChainAPI{}).ChainId(); got != nil {
		t.Fatalf("ChainId() = %v, want nil", got)
	}

	if got := (&BlockChainAPI{api: &API{}}).ChainId(); got != nil {
		t.Fatalf("ChainId() with nil config = %v, want nil", got)
	}

	api := &BlockChainAPI{api: &API{chainConfig: &params.ChainConfig{}}}
	if got := api.ChainId(); got != nil {
		t.Fatalf("ChainId() with nil ChainID = %v, want nil", got)
	}

	api.api.chainConfig.ChainID = big.NewInt(9)
	if got := api.ChainId(); got == nil || got.ToInt().Uint64() != 9 {
		t.Fatalf("ChainId() = %v, want 9", got)
	}
}

func TestENSAPIResolveNameRejectsMissingChainID(t *testing.T) {
	ensAPI := NewENSAPI(&BlockChainAPI{
		api: &API{
			chainConfig: &params.ChainConfig{},
		},
	})

	_, err := ensAPI.ResolveName(context.Background(), "example.eth")
	if err == nil || err.Error() != "chain ID unavailable" {
		t.Fatalf("ResolveName() error = %v", err)
	}
}

func TestBlockOverridesApplyRejectsOverflowNumber(t *testing.T) {
	ctx := &evmtypes.BlockContext{}
	overflow := new(big.Int).Lsh(big.NewInt(1), 256)
	overrides := &BlockOverrides{
		Number: (*hexutil.Big)(overflow),
	}

	err := overrides.Apply(ctx)
	if err == nil || err.Error() != "invalid block override number: value overflows uint256" {
		t.Fatalf("BlockOverrides.Apply() error = %v", err)
	}
}

func TestBlockOverridesApplyRejectsNegativeBaseFee(t *testing.T) {
	ctx := &evmtypes.BlockContext{}
	overrides := &BlockOverrides{
		BaseFee: (*hexutil.Big)(big.NewInt(-1)),
	}

	err := overrides.Apply(ctx)
	if err == nil || err.Error() != "invalid block override base fee: value must be non-negative" {
		t.Fatalf("BlockOverrides.Apply() error = %v", err)
	}
}
