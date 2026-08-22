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
	if resp.LatestValidHash != nil {
		t.Fatalf("NewPayloadV1().LatestValidHash = %v, want nil", *resp.LatestValidHash)
	}

	v2Resp, err := engine.NewPayloadV2(context.Background(), &ExecutionPayloadV2{})
	if err != nil {
		t.Fatalf("NewPayloadV2() error = %v", err)
	}
	if v2Resp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV2().Status = %q, want %q", v2Resp.Status, PayloadStatusInvalid)
	}
	if v2Resp.LatestValidHash != nil {
		t.Fatalf("NewPayloadV2().LatestValidHash = %v, want nil", *v2Resp.LatestValidHash)
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

func TestEngineAPIV2RejectsPreCancunBlobFieldsAsInvalidParams(t *testing.T) {
	t.Parallel()

	engine := NewEngineAPIV1(nil)
	blobGasUsed := hexutil.Uint64(1)
	excessBlobGas := hexutil.Uint64(1)

	resp, err := engine.NewPayloadV2(context.Background(), &ExecutionPayloadV2{
		ExecutionPayloadV1: ExecutionPayloadV1{},
		Withdrawals:        []*Withdrawal{},
		BlobGasUsed:        &blobGasUsed,
		ExcessBlobGas:      &excessBlobGas,
	})
	if resp != nil {
		t.Fatalf("NewPayloadV2() response = %#v, want nil", resp)
	}
	if err == nil {
		t.Fatal("NewPayloadV2() error = nil, want invalid params")
	}
	coded, ok := err.(interface{ ErrorCode() int })
	if !ok {
		t.Fatalf("NewPayloadV2() error = %T, want error with code", err)
	}
	if coded.ErrorCode() != -32602 {
		t.Fatalf("NewPayloadV2() error code = %d, want -32602", coded.ErrorCode())
	}
}

func TestEngineAPIV2ValidatesWithdrawalsAgainstShanghaiFork(t *testing.T) {
	t.Parallel()

	engine := &EngineAPIV1{api: &BlockChainAPI{api: &API{chainConfig: &params.ChainConfig{
		ShanghaiTime: big.NewInt(10),
	}}}}
	tests := []struct {
		name        string
		timestamp   uint64
		withdrawals []*Withdrawal
		wantError   bool
	}{
		{name: "missing at Shanghai", timestamp: 10, withdrawals: nil, wantError: true},
		{name: "present before Shanghai", timestamp: 9, withdrawals: []*Withdrawal{}, wantError: true},
		{name: "missing before Shanghai", timestamp: 9, withdrawals: nil, wantError: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp, err := engine.NewPayloadV2(context.Background(), &ExecutionPayloadV2{
				ExecutionPayloadV1: ExecutionPayloadV1{Timestamp: hexutil.Uint64(test.timestamp)},
				Withdrawals:        test.withdrawals,
			})
			if test.wantError {
				if resp != nil {
					t.Fatalf("response = %#v, want nil", resp)
				}
				coded, ok := err.(interface{ ErrorCode() int })
				if !ok || coded.ErrorCode() != -32602 {
					t.Fatalf("error = %v (%T), want -32602", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if resp == nil || resp.Status != PayloadStatusInvalid {
				t.Fatalf("response = %#v, want ordinary payload validation result", resp)
			}
		})
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
	payloadV2, err := engine.GetPayloadV2(context.Background(), *forkchoiceResp.PayloadID)
	if err != nil {
		t.Fatalf("GetPayloadV2() for V1-built payload error = %v", err)
	}
	if payloadV2.ExecutionPayload.BlockHash != payload.BlockHash {
		t.Fatalf("GetPayloadV2().BlockHash = %s, want %s", payloadV2.ExecutionPayload.BlockHash, payload.BlockHash)
	}
	if payloadV2.ExecutionPayload.Withdrawals != nil {
		t.Fatalf("GetPayloadV2().Withdrawals = %#v, want nil before Shanghai", payloadV2.ExecutionPayload.Withdrawals)
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

func TestForkchoiceUpdatedV1PayloadIDChangesWithPrevRandao(t *testing.T) {
	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	firstResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	})
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1(first) error = %v", err)
	}
	if firstResp.PayloadID == nil {
		t.Fatal("ForkchoiceUpdatedV1(first) returned nil payload ID")
	}

	secondResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: headHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x12),
		SuggestedFeeRecipient: types.Address{0x22},
	})
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1(second) error = %v", err)
	}
	if secondResp.PayloadID == nil {
		t.Fatal("ForkchoiceUpdatedV1(second) returned nil payload ID")
	}
	if *firstResp.PayloadID == *secondResp.PayloadID {
		t.Fatalf("ForkchoiceUpdatedV1() reused payload ID %s for different prevRandao", *firstResp.PayloadID)
	}
}

func TestForkchoiceUpdatedV2UsesOverlayHeadHashWhenAttrsNil(t *testing.T) {
	t.Parallel()

	genesisHeader := &block.Header{
		Number:     uint256.NewInt(0),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       14999,
		BaseFee:    uint256.NewInt(1),
	}
	genesisBlock := block.NewBlock(genesisHeader, nil)
	chain := &canonicalCheckChainStub{
		header: genesisHeader,
		blk:    genesisBlock,
	}
	cfg := &params.ChainConfig{
		LondonBlock:  big.NewInt(0),
		ShanghaiTime: big.NewInt(15000),
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	genesisHash := engine.currentHeadHash()
	attrs := &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             15000,
			PrevRandao:            typesHashFromHexByte(0x33),
			SuggestedFeeRecipient: types.Address{0x44},
		},
		Withdrawals: []*Withdrawal{{
			Index:          hexutil.Uint64(1),
			ValidatorIndex: hexutil.Uint64(2),
			Address:        types.Address{0x55},
			Amount:         hexutil.Uint64(3),
		}},
	}

	buildResp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: genesisHash,
	}, attrs)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV2(build) error = %v", err)
	}
	if buildResp.PayloadStatus.Status != PayloadStatusValid {
		t.Fatalf("ForkchoiceUpdatedV2(build).Status = %q, want %q", buildResp.PayloadStatus.Status, PayloadStatusValid)
	}
	if buildResp.PayloadID == nil {
		t.Fatal("ForkchoiceUpdatedV2(build) returned nil payload ID")
	}

	built, err := engine.GetPayloadV2(context.Background(), *buildResp.PayloadID)
	if err != nil {
		t.Fatalf("GetPayloadV2() error = %v", err)
	}
	importResp, err := engine.NewPayloadV2(context.Background(), built.ExecutionPayload)
	if err != nil {
		t.Fatalf("NewPayloadV2() error = %v", err)
	}
	if importResp.Status != PayloadStatusValid {
		t.Fatalf("NewPayloadV2().Status = %q, want %q", importResp.Status, PayloadStatusValid)
	}

	finalResp, err := engine.ForkchoiceUpdatedV2(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: built.ExecutionPayload.BlockHash,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV2(finalize) error = %v", err)
	}
	if finalResp.PayloadStatus.Status != PayloadStatusValid {
		t.Fatalf("ForkchoiceUpdatedV2(finalize).Status = %q, want %q", finalResp.PayloadStatus.Status, PayloadStatusValid)
	}
	if finalResp.PayloadID != nil {
		t.Fatalf("ForkchoiceUpdatedV2(finalize).PayloadID = %v, want nil", finalResp.PayloadID)
	}
}

func TestNewPayloadV1DoesNotAdvanceHeadBeforeForkchoice(t *testing.T) {
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

	genesisHash := engine.currentHeadHash()
	buildResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: genesisHash,
	}, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x55),
		SuggestedFeeRecipient: types.Address{0x66},
	})
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1(build) error = %v", err)
	}
	if buildResp.PayloadID == nil {
		t.Fatal("ForkchoiceUpdatedV1(build) returned nil payload ID")
	}

	payload, err := engine.GetPayloadV1(context.Background(), *buildResp.PayloadID)
	if err != nil {
		t.Fatalf("GetPayloadV1() error = %v", err)
	}
	importResp, err := engine.NewPayloadV1(context.Background(), payload)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if importResp.Status != PayloadStatusValid {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", importResp.Status, PayloadStatusValid)
	}
	if got := engine.currentHeadHash(); got != genesisHash {
		t.Fatalf("currentHeadHash after NewPayload = %s, want genesis %s", got, genesisHash)
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
	if got := engine.currentHeadHash(); got != payload.BlockHash {
		t.Fatalf("currentHeadHash after ForkchoiceUpdated = %s, want %s", got, payload.BlockHash)
	}
}

func TestNewPayloadV1ReturnsParentLatestValidHashForInvalidGasLimit(t *testing.T) {
	t.Parallel()

	api, headHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	payload := &ExecutionPayloadV1{
		ParentHash:    headHash,
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x55),
		BlockNumber:   hexutil.Uint64(1),
		GasLimit:      hexutil.Uint64(params.MinGasLimit - 1),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(2),
		BaseFeePerGas: hexBigFromUint64(1),
	}
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	payload.BlockHash = ethCompatibleBlockHash(blk, api.chainConfig)

	resp, err := engine.NewPayloadV1(context.Background(), payload)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if resp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", resp.Status, PayloadStatusInvalid)
	}
	if resp.LatestValidHash == nil || *resp.LatestValidHash != headHash {
		t.Fatalf("NewPayloadV1().LatestValidHash = %v, want %s", resp.LatestValidHash, headHash)
	}
	if resp.ValidationError == nil || *resp.ValidationError != "invalid gas limit below 5000" {
		t.Fatalf("NewPayloadV1().ValidationError = %v, want invalid gas limit below 5000", resp.ValidationError)
	}
}

func TestNewPayloadV1AcceptsPayloadWithMissingParent(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	if chain, ok := api.bc.(*canonicalCheckChainStub); ok {
		chain.disableHeaderByHash = true
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	payload := &ExecutionPayloadV1{
		ParentHash:    typesHashFromHexByte(0xaa),
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x55),
		BlockNumber:   hexutil.Uint64(6),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(10),
		ExtraData:     hexutil.Bytes{0x01},
		BaseFeePerGas: hexBigFromUint64(1),
	}
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	payload.BlockHash = ethCompatibleBlockHash(blk, api.chainConfig)

	resp, err := engine.NewPayloadV1(context.Background(), payload)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if resp.Status != PayloadStatusAccepted {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", resp.Status, PayloadStatusAccepted)
	}
	if resp.LatestValidHash != nil {
		t.Fatalf("NewPayloadV1().LatestValidHash = %v, want nil", *resp.LatestValidHash)
	}
	if staged := api.engineOverlay.blockByHash(payload.BlockHash); staged == nil {
		t.Fatalf("overlay.blockByHash(%s) = nil, want staged block", payload.BlockHash)
	}
}

func TestForkchoiceUpdatedV1ReturnsSyncingForAcceptedPayload(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	if chain, ok := api.bc.(*canonicalCheckChainStub); ok {
		chain.disableHeaderByHash = true
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	payload := &ExecutionPayloadV1{
		ParentHash:    typesHashFromHexByte(0xaa),
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x55),
		BlockNumber:   hexutil.Uint64(6),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(10),
		ExtraData:     hexutil.Bytes{0x01},
		BaseFeePerGas: hexBigFromUint64(1),
	}
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	payload.BlockHash = ethCompatibleBlockHash(blk, api.chainConfig)

	resp, err := engine.NewPayloadV1(context.Background(), payload)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if resp.Status != PayloadStatusAccepted {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", resp.Status, PayloadStatusAccepted)
	}

	fcResp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      payload.BlockHash,
		SafeBlockHash:      payload.BlockHash,
		FinalizedBlockHash: payload.BlockHash,
	}, &PayloadAttributesV1{
		Timestamp:             payload.Timestamp,
		PrevRandao:            types.Hash{},
		SuggestedFeeRecipient: types.Address{},
	})
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if fcResp.PayloadStatus.Status != PayloadStatusSyncing {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", fcResp.PayloadStatus.Status, PayloadStatusSyncing)
	}
}

func TestNewPayloadV1AcceptedPayloadDoesNotBecomeKnownParent(t *testing.T) {
	t.Parallel()

	api, _ := newEnginePayloadTestAPI()
	if chain, ok := api.bc.(*canonicalCheckChainStub); ok {
		chain.disableHeaderByHash = true
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	parent := &ExecutionPayloadV1{
		ParentHash:    typesHashFromHexByte(0xaa),
		FeeRecipient:  types.Address{0x22},
		StateRoot:     types.Hash{0x33},
		ReceiptsRoot:  types.Hash{0x44},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x55),
		BlockNumber:   hexutil.Uint64(6),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(10),
		BaseFeePerGas: hexBigFromUint64(1),
	}
	parentBlk, err := executionPayloadV1ToBlock(parent)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(parent) error = %v", err)
	}
	parent.BlockHash = ethCompatibleBlockHash(parentBlk, api.chainConfig)

	parentResp, err := engine.NewPayloadV1(context.Background(), parent)
	if err != nil {
		t.Fatalf("NewPayloadV1(parent) error = %v", err)
	}
	if parentResp.Status != PayloadStatusAccepted {
		t.Fatalf("NewPayloadV1(parent).Status = %q, want %q", parentResp.Status, PayloadStatusAccepted)
	}

	child := &ExecutionPayloadV1{
		ParentHash:    parent.BlockHash,
		FeeRecipient:  types.Address{0x66},
		StateRoot:     types.Hash{0x77},
		ReceiptsRoot:  types.Hash{0x88},
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x99),
		BlockNumber:   hexutil.Uint64(7),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(11),
		BaseFeePerGas: hexBigFromUint64(1),
	}
	childBlk, err := executionPayloadV1ToBlock(child)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(child) error = %v", err)
	}
	child.BlockHash = ethCompatibleBlockHash(childBlk, api.chainConfig)

	childResp, err := engine.NewPayloadV1(context.Background(), child)
	if err != nil {
		t.Fatalf("NewPayloadV1(child) error = %v", err)
	}
	if childResp.Status != PayloadStatusAccepted {
		t.Fatalf("NewPayloadV1(child).Status = %q, want %q", childResp.Status, PayloadStatusAccepted)
	}
	if childResp.LatestValidHash != nil {
		t.Fatalf("NewPayloadV1(child).LatestValidHash = %v, want nil", *childResp.LatestValidHash)
	}
}

func TestNewPayloadV1ReturnsInvalidForDescendantOfRejectedPayload(t *testing.T) {
	t.Parallel()

	api, genesisHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	genesis := api.bc.CurrentBlock()

	validParent := buildExecutionPayloadV1(genesis, genesisHash, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	}, api.chainConfig)
	validParentBlock, err := executionPayloadV1ToBlock(validParent)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(validParent) error = %v", err)
	}
	api.engineOverlay.stageBlock(validParentBlock, validParent.BlockHash, nil, nil, true)

	rejectedPayload := buildExecutionPayloadV1(validParentBlock, validParent.BlockHash, &PayloadAttributesV1{
		Timestamp:             3,
		PrevRandao:            typesHashFromHexByte(0x33),
		SuggestedFeeRecipient: types.Address{0x44},
	}, api.chainConfig)
	rejectedPayload.GasLimit = hexutil.Uint64(params.MinGasLimit - 1)
	rejectedBlock, err := executionPayloadV1ToBlock(rejectedPayload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(rejectedPayload) error = %v", err)
	}
	rejectedPayload.BlockHash = ethCompatibleBlockHash(rejectedBlock, api.chainConfig)

	rejectedResp, err := engine.NewPayloadV1(context.Background(), rejectedPayload)
	if err != nil {
		t.Fatalf("NewPayloadV1(rejectedPayload) error = %v", err)
	}
	if rejectedResp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV1(rejectedPayload).Status = %q, want %q", rejectedResp.Status, PayloadStatusInvalid)
	}
	if rejectedResp.LatestValidHash == nil || *rejectedResp.LatestValidHash != validParent.BlockHash {
		t.Fatalf("NewPayloadV1(rejectedPayload).LatestValidHash = %v, want %s", rejectedResp.LatestValidHash, validParent.BlockHash)
	}

	descendantPayload := &ExecutionPayloadV1{
		ParentHash:    rejectedPayload.BlockHash,
		FeeRecipient:  types.Address{0x55},
		StateRoot:     typesHashFromHexByte(0x66),
		ReceiptsRoot:  typesHashFromHexByte(0x77),
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x88),
		BlockNumber:   hexutil.Uint64(3),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(4),
		ExtraData:     hexutil.Bytes{0x01},
		BaseFeePerGas: hexBigFromUint64(1),
	}
	descendantBlock, err := executionPayloadV1ToBlock(descendantPayload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(descendantPayload) error = %v", err)
	}
	descendantPayload.BlockHash = ethCompatibleBlockHash(descendantBlock, api.chainConfig)

	descendantResp, err := engine.NewPayloadV1(context.Background(), descendantPayload)
	if err != nil {
		t.Fatalf("NewPayloadV1(descendantPayload) error = %v", err)
	}
	if descendantResp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV1(descendantPayload).Status = %q, want %q", descendantResp.Status, PayloadStatusInvalid)
	}
	if descendantResp.LatestValidHash == nil || *descendantResp.LatestValidHash != validParent.BlockHash {
		t.Fatalf("NewPayloadV1(descendantPayload).LatestValidHash = %v, want %s", descendantResp.LatestValidHash, validParent.BlockHash)
	}
}

func TestForkchoiceUpdatedV1ReturnsInvalidForDescendantOfRejectedPayload(t *testing.T) {
	t.Parallel()

	api, genesisHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	genesis := api.bc.CurrentBlock()

	validParent := buildExecutionPayloadV1(genesis, genesisHash, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	}, api.chainConfig)
	validParentBlock, err := executionPayloadV1ToBlock(validParent)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(validParent) error = %v", err)
	}
	api.engineOverlay.stageBlock(validParentBlock, validParent.BlockHash, nil, nil, true)

	rejectedPayload := buildExecutionPayloadV1(validParentBlock, validParent.BlockHash, &PayloadAttributesV1{
		Timestamp:             3,
		PrevRandao:            typesHashFromHexByte(0x33),
		SuggestedFeeRecipient: types.Address{0x44},
	}, api.chainConfig)
	rejectedPayload.GasLimit = hexutil.Uint64(params.MinGasLimit - 1)
	rejectedBlock, err := executionPayloadV1ToBlock(rejectedPayload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(rejectedPayload) error = %v", err)
	}
	rejectedPayload.BlockHash = ethCompatibleBlockHash(rejectedBlock, api.chainConfig)

	if _, err := engine.NewPayloadV1(context.Background(), rejectedPayload); err != nil {
		t.Fatalf("NewPayloadV1(rejectedPayload) error = %v", err)
	}

	descendantPayload := &ExecutionPayloadV1{
		ParentHash:    rejectedPayload.BlockHash,
		FeeRecipient:  types.Address{0x55},
		StateRoot:     typesHashFromHexByte(0x66),
		ReceiptsRoot:  typesHashFromHexByte(0x77),
		LogsBloom:     make([]byte, 256),
		PrevRandao:    typesHashFromHexByte(0x88),
		BlockNumber:   hexutil.Uint64(3),
		GasLimit:      hexutil.Uint64(30_000_000),
		GasUsed:       0,
		Timestamp:     hexutil.Uint64(4),
		ExtraData:     hexutil.Bytes{0x01},
		BaseFeePerGas: hexBigFromUint64(1),
	}
	descendantBlock, err := executionPayloadV1ToBlock(descendantPayload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(descendantPayload) error = %v", err)
	}
	descendantPayload.BlockHash = ethCompatibleBlockHash(descendantBlock, api.chainConfig)

	if _, err := engine.NewPayloadV1(context.Background(), descendantPayload); err != nil {
		t.Fatalf("NewPayloadV1(descendantPayload) error = %v", err)
	}

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: descendantPayload.BlockHash,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if resp.PayloadStatus.Status != PayloadStatusInvalid {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", resp.PayloadStatus.Status, PayloadStatusInvalid)
	}
	if resp.PayloadStatus.LatestValidHash == nil || *resp.PayloadStatus.LatestValidHash != validParent.BlockHash {
		t.Fatalf("ForkchoiceUpdatedV1().LatestValidHash = %v, want %s", resp.PayloadStatus.LatestValidHash, validParent.BlockHash)
	}
}

func TestForkchoiceUpdatedV1AdoptsValidatedStagedPayload(t *testing.T) {
	t.Parallel()

	api, genesisHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	genesis := api.bc.CurrentBlock()

	payload := buildExecutionPayloadV1(genesis, genesisHash, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	}, api.chainConfig)
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock() error = %v", err)
	}
	api.engineOverlay.stageBlock(blk, payload.BlockHash, &engineStateOverlay{}, nil, true)

	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: payload.BlockHash,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if resp.PayloadStatus.Status != PayloadStatusValid {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", resp.PayloadStatus.Status, PayloadStatusValid)
	}
	if got := engine.currentHeadHash(); got != payload.BlockHash {
		t.Fatalf("currentHeadHash = %s, want %s", got, payload.BlockHash)
	}
}

func TestNewPayloadV1BadBlockHashOnSidechainReturnsNilLatestValidHash(t *testing.T) {
	t.Parallel()

	api, genesisHash := newEnginePayloadTestAPI()
	engine := NewEngineAPIV1(NewBlockChainAPI(api))
	genesis := api.bc.CurrentBlock()

	payload1 := buildExecutionPayloadV1(genesis, genesisHash, &PayloadAttributesV1{
		Timestamp:             2,
		PrevRandao:            typesHashFromHexByte(0x11),
		SuggestedFeeRecipient: types.Address{0x22},
	}, api.chainConfig)
	block1, err := executionPayloadV1ToBlock(payload1)
	if err != nil {
		t.Fatalf("executionPayloadV1ToBlock(block1) error = %v", err)
	}
	api.engineOverlay.importBlock(block1, payload1.BlockHash, nil)

	payload2 := buildExecutionPayloadV1(block1, payload1.BlockHash, &PayloadAttributesV1{
		Timestamp:             3,
		PrevRandao:            typesHashFromHexByte(0x33),
		SuggestedFeeRecipient: types.Address{0x44},
	}, api.chainConfig)
	payload2.ParentHash = genesisHash
	payload2.BlockHash = typesHashFromHexByte(0x99)

	resp, err := engine.NewPayloadV1(context.Background(), payload2)
	if err != nil {
		t.Fatalf("NewPayloadV1() error = %v", err)
	}
	if resp.Status != PayloadStatusInvalid {
		t.Fatalf("NewPayloadV1().Status = %q, want %q", resp.Status, PayloadStatusInvalid)
	}
	if resp.LatestValidHash != nil {
		t.Fatalf("NewPayloadV1().LatestValidHash = %v, want nil", *resp.LatestValidHash)
	}
	if resp.ValidationError == nil || *resp.ValidationError != "blockhash mismatch" {
		t.Fatalf("NewPayloadV1().ValidationError = %v, want blockhash mismatch", resp.ValidationError)
	}
}

func typesHashFromHexByte(b byte) types.Hash {
	var h types.Hash
	h[31] = b
	return h
}
