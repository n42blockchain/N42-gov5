package api

import (
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func TestEngineAPIsExposeAuthenticatedNamespace(t *testing.T) {
	t.Parallel()

	apis := EngineAPIs(&API{})
	if len(apis) != 3 {
		t.Fatalf("len(EngineAPIs()) = %d, want 3", len(apis))
	}
	for i, api := range apis {
		if api.Namespace != "engine" {
			t.Fatalf("api[%d].Namespace = %q, want engine", i, api.Namespace)
		}
		if !api.Authenticated {
			t.Fatalf("api[%d].Authenticated = false, want true", i)
		}
	}
}

func TestSupportedEngineMethodsContainLegacyAndExchangeEndpoints(t *testing.T) {
	t.Parallel()

	got := supportedEngineMethods()
	want := []string{
		"engine_newPayloadV1",
		"engine_newPayloadV2",
		"engine_newPayloadV3",
		"engine_newPayloadV4",
		"engine_getPayloadV1",
		"engine_getPayloadV2",
		"engine_getPayloadV3",
		"engine_getPayloadV4",
		"engine_forkchoiceUpdatedV1",
		"engine_forkchoiceUpdatedV2",
		"engine_forkchoiceUpdatedV3",
		"engine_forkchoiceUpdatedV4",
		"engine_getBlobsBundleV1",
		"engine_getBlobsV1",
		"engine_getBlobScheduleV1",
		"engine_getClientCapabilitiesV1",
		"engine_getForkCandidatesV1",
		"engine_exchangeCapabilities",
		"engine_exchangeTransitionConfigurationV1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supportedEngineMethods() = %v, want %v", got, want)
	}
}

func TestEngineAPIV1InputValidationAndExchangeCapabilities(t *testing.T) {
	t.Parallel()

	engine := NewEngineAPIV1(nil)

	resp, err := engine.NewPayloadV1(context.Background(), nil)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if resp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", resp.Status, PayloadStatusInvalid)
	}

	v2Resp, err := engine.NewPayloadV2(context.Background(), &ExecutionPayloadV2{})
	if err != nil {
		t.Fatalf("NewPayloadV2() error = %v", err)
	}
	if v2Resp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV2().Status = %q, want %q", v2Resp.Status, PayloadStatusInvalid)
	}

	forkchoiceResp, err := engine.ForkchoiceUpdatedV2(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV2() error = %v", err)
	}
	if forkchoiceResp.PayloadStatus.Status != PayloadStatusInvalid {
		t.Fatalf("ForkchoiceUpdatedV2().Status = %q, want %q", forkchoiceResp.PayloadStatus.Status, PayloadStatusInvalid)
	}

	methods, err := engine.ExchangeCapabilities(context.Background(), []string{"engine_newPayloadV1"})
	if err != nil {
		t.Fatalf("ExchangeCapabilities() error = %v", err)
	}
	if !reflect.DeepEqual(methods, supportedEngineMethods()) {
		t.Fatalf("ExchangeCapabilities() = %v, want %v", methods, supportedEngineMethods())
	}
}

func TestLocalTransitionConfigurationUsesChainConfigTTD(t *testing.T) {
	t.Parallel()

	engine := &EngineAPIV1{
		api: &BlockChainAPI{
			api: &API{
				chainConfig: &params.ChainConfig{
					TerminalTotalDifficulty: big.NewInt(42),
				},
			},
		},
	}

	got := localTransitionConfiguration(engine)
	if got.TerminalTotalDifficulty == nil || got.TerminalTotalDifficulty.ToInt().Uint64() != 42 {
		t.Fatalf("localTransitionConfiguration() = %#v, want terminalTotalDifficulty=42", got)
	}
}

func TestEngineAPIV1BuildsAndImportsMinimalPayload(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       1,
		BaseFee:    uint256.NewInt(1),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	api := &API{
		bc:            chain,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	headHash := ethCompatibleBlockHash(genesisBlock, nil)
	attrs := &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	}
	forkchoiceResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, attrs)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if forkchoiceResp.PayloadStatus.Status != PayloadStatusValid {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", forkchoiceResp.PayloadStatus.Status, PayloadStatusValid)
	}
	if forkchoiceResp.PayloadID == nil {
		t.Fatal("ForkchoiceUpdatedV1() returned nil payload ID")
	}

	payload, err := engine.GetPayloadV1(context.Background(), *forkchoiceResp.PayloadID)
	if err != nil {
		t.Fatalf("GetPayloadV1() error = %v", err)
	}
	if payload.ParentHash != headHash {
		t.Fatalf("GetPayloadV1().ParentHash = %s, want %s", payload.ParentHash, headHash)
	}
	if payload.BlockHash == (types.Hash{}) {
		t.Fatal("GetPayloadV1().BlockHash is empty")
	}

	newPayloadResp, err := engine.NewPayloadV1(context.Background(), payload)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if newPayloadResp.Status != PayloadStatusValid {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", newPayloadResp.Status, PayloadStatusValid)
	}
	if newPayloadResp.LatestValidHash == nil || *newPayloadResp.LatestValidHash != payload.BlockHash {
		t.Fatalf("NewPayloadV1().LatestValidHash = %v, want %s", newPayloadResp.LatestValidHash, payload.BlockHash)
	}

	finalResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload.BlockHash,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1(finalize) error = %v", err)
	}
	if finalResp.PayloadStatus.Status != PayloadStatusValid {
		t.Fatalf("ForkchoiceUpdatedV1(finalize).Status = %q, want %q", finalResp.PayloadStatus.Status, PayloadStatusValid)
	}
	if finalResp.PayloadID != nil {
		t.Fatalf("ForkchoiceUpdatedV1(finalize).PayloadID = %v, want nil", finalResp.PayloadID)
	}

	imported, err := engine.api.getBlockByNumber(jsonrpc.BlockNumber(1))
	if err != nil {
		t.Fatalf("getBlockByNumber(1) error = %v", err)
	}
	if imported == nil {
		t.Fatal("getBlockByNumber(1) returned nil")
	}
	if got := ethCompatibleBlockHash(imported, nil); got != payload.BlockHash {
		t.Fatalf("imported block hash = %s, want %s", got, payload.BlockHash)
	}
	if got := engine.api.BlockNumber(); got != hexutil.Uint64(1) {
		t.Fatalf("BlockNumber() = %d, want 1", got)
	}
}

func typesHashFromHexByte(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}
