// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package api

import (
	"context"
	"math/big"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

// ExecutionPayloadV1 represents a Paris Engine API payload.
type ExecutionPayloadV1 struct {
	ParentHash    types.Hash      `json:"parentHash"`
	FeeRecipient  types.Address   `json:"feeRecipient"`
	StateRoot     types.Hash      `json:"stateRoot"`
	ReceiptsRoot  types.Hash      `json:"receiptsRoot"`
	LogsBloom     hexutil.Bytes   `json:"logsBloom"`
	PrevRandao    types.Hash      `json:"prevRandao"`
	BlockNumber   hexutil.Uint64  `json:"blockNumber"`
	GasLimit      hexutil.Uint64  `json:"gasLimit"`
	GasUsed       hexutil.Uint64  `json:"gasUsed"`
	Timestamp     hexutil.Uint64  `json:"timestamp"`
	ExtraData     hexutil.Bytes   `json:"extraData"`
	BaseFeePerGas hexutil.Uint64  `json:"baseFeePerGas"`
	BlockHash     types.Hash      `json:"blockHash"`
	Transactions  []hexutil.Bytes `json:"transactions"`
}

// ExecutionPayloadV2 represents a Shanghai Engine API payload.
type ExecutionPayloadV2 struct {
	ExecutionPayloadV1
	Withdrawals   []*Withdrawal   `json:"withdrawals"`
	BlobGasUsed   *hexutil.Uint64 `json:"blobGasUsed,omitempty"`
	ExcessBlobGas *hexutil.Uint64 `json:"excessBlobGas,omitempty"`
}

// PayloadAttributesV1 represents Paris payload attributes.
type PayloadAttributesV1 struct {
	Timestamp             hexutil.Uint64 `json:"timestamp"`
	PrevRandao            types.Hash     `json:"prevRandao"`
	SuggestedFeeRecipient types.Address  `json:"suggestedFeeRecipient"`
}

// PayloadAttributesV2 represents Shanghai payload attributes.
type PayloadAttributesV2 struct {
	PayloadAttributesV1
	Withdrawals []*Withdrawal `json:"withdrawals"`
}

// GetPayloadResponseV2 is the response for engine_getPayloadV2.
type GetPayloadResponseV2 struct {
	ExecutionPayload *ExecutionPayloadV2 `json:"executionPayload"`
	BlockValue       hexutil.Uint64      `json:"blockValue"`
}

// TransitionConfigurationV1 exchanges merge transition parameters.
type TransitionConfigurationV1 struct {
	TerminalTotalDifficulty *hexutil.Big   `json:"terminalTotalDifficulty"`
	TerminalBlockHash       types.Hash     `json:"terminalBlockHash"`
	TerminalBlockNumber     hexutil.Uint64 `json:"terminalBlockNumber"`
}

// EngineAPIV1 provides Paris/Shanghai compatible Engine API methods.
type EngineAPIV1 struct {
	api *BlockChainAPI
}

// NewEngineAPIV1 creates a new Engine API v1/v2 handler.
func NewEngineAPIV1(api *BlockChainAPI) *EngineAPIV1 {
	return &EngineAPIV1{api: api}
}

// EngineAPIs returns the authenticated Engine API namespaces exposed by the node.
func EngineAPIs(api *API) []jsonrpc.API {
	blockChainAPI := NewBlockChainAPI(api)
	return []jsonrpc.API{
		{
			Namespace:     "engine",
			Service:       NewEngineAPIV1(blockChainAPI),
			Authenticated: true,
		},
		{
			Namespace:     "engine",
			Service:       NewEngineAPIBlob(blockChainAPI),
			Authenticated: true,
		},
		{
			Namespace:     "engine",
			Service:       NewEngineAPIv4(blockChainAPI),
			Authenticated: true,
		},
	}
}

func supportedEngineMethods() []string {
	return []string{
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
}

func supportedEngineForks() []string {
	return []string{
		"paris",
		"shanghai",
		"cancun",
		"prague",
		"pectra",
	}
}

// NewPayloadV1 processes a Paris execution payload.
func (e *EngineAPIV1) NewPayloadV1(ctx context.Context, payload *ExecutionPayloadV1) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadHeader(blockHeader(blk), e.parentHeader(payload.ParentHash), e.chainConfig()); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), 0, uint64(payload.GasLimit)); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.chainConfig(), enginePayloadHashOptions{}); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blockHash := ethCompatibleBlockHash(blk, e.chainConfig())
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("blockhash mismatch"), nil
	}
	if err := e.validatePayloadExecution(blk, payload.ParentHash, nil, nil); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	headHash := e.currentHeadHash()
	if headHash == (types.Hash{}) || payload.ParentHash != headHash {
		return syncingPayloadResponse(), nil
	}
	if overlay := e.overlay(); overlay != nil {
		overlay.importBlock(blk, blockHash)
	}
	return validPayloadResponse(blockHash), nil
}

// NewPayloadV2 processes a Shanghai execution payload.
func (e *EngineAPIV1) NewPayloadV2(ctx context.Context, payload *ExecutionPayloadV2) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	if payload.Withdrawals == nil {
		return invalidPayloadResponse("missing withdrawals"), nil
	}
	if payload.BlobGasUsed != nil || payload.ExcessBlobGas != nil {
		return nil, &engineInvalidParamsError{msg: "blob fields not allowed before Cancun"}
	}
	blk, err := executionPayloadV2ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadHeader(blockHeader(blk), e.parentHeader(payload.ParentHash), e.chainConfig()); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), 0, uint64(payload.GasLimit)); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
	}); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blockHash := ethCompatibleEngineBlockHash(blk, e.chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
	})
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("blockhash mismatch"), nil
	}
	if err := e.validatePayloadExecution(blk, payload.ParentHash, nil, nil); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	headHash := e.currentHeadHash()
	if headHash == (types.Hash{}) || payload.ParentHash != headHash {
		return syncingPayloadResponse(), nil
	}
	if overlay := e.overlay(); overlay != nil {
		overlay.importBlock(blk, blockHash)
	}
	return validPayloadResponse(blockHash), nil
}

// GetPayloadV1 retrieves a Paris payload.
func (e *EngineAPIV1) GetPayloadV1(ctx context.Context, payloadID PayloadID) (*ExecutionPayloadV1, error) {
	if built := e.builtPayload(payloadID); built != nil && built.v1 != nil {
		return built.v1, nil
	}
	return nil, errPayloadNotFound
}

// GetPayloadV2 retrieves a Shanghai payload.
func (e *EngineAPIV1) GetPayloadV2(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV2, error) {
	if built := e.builtPayload(payloadID); built != nil && built.v2 != nil {
		return &GetPayloadResponseV2{
			ExecutionPayload: built.v2,
			BlockValue:       hexutil.Uint64(0),
		}, nil
	}
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV1 updates the forkchoice state for Paris.
func (e *EngineAPIV1) ForkchoiceUpdatedV1(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV1) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	head := e.currentHead()
	if head == nil {
		return syncingForkchoiceResponse(), nil
	}
	headHash := e.currentHeadHash()
	if state.HeadBlockHash != headHash {
		return syncingForkchoiceResponse(), nil
	}
	if attrs == nil {
		return validForkchoiceResponse(headHash, nil), nil
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return invalidForkchoiceResponse("payload timestamp must be greater than parent"), nil
	}
	payload := buildExecutionPayloadV1(head, headHash, attrs, e.chainConfig())
	payloadID := makePayloadID(headHash, uint64(attrs.Timestamp), attrs.SuggestedFeeRecipient)
	if overlay := e.overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{v1: payload})
	}
	return validForkchoiceResponse(headHash, &payloadID), nil
}

// ForkchoiceUpdatedV2 updates the forkchoice state for Shanghai.
func (e *EngineAPIV1) ForkchoiceUpdatedV2(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV2) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	if attrs != nil && attrs.Withdrawals == nil {
		return invalidForkchoiceResponse("missing withdrawals"), nil
	}
	if attrs == nil {
		return e.ForkchoiceUpdatedV1(ctx, state, nil)
	}
	head := e.currentHead()
	if head == nil {
		return syncingForkchoiceResponse(), nil
	}
	headHash := e.currentHeadHash()
	if state.HeadBlockHash != headHash {
		return syncingForkchoiceResponse(), nil
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return invalidForkchoiceResponse("payload timestamp must be greater than parent"), nil
	}
	payloadV2 := buildExecutionPayloadV2(head, headHash, attrs, e.chainConfig())
	if payloadV2 == nil {
		return invalidForkchoiceResponse("failed to build payload"), nil
	}
	withdrawalsRoot := withdrawalsRoot(attrs.Withdrawals)
	payloadID := makePayloadIDWithExtras(headHash, uint64(attrs.Timestamp), attrs.SuggestedFeeRecipient, withdrawalsRoot[:])
	if overlay := e.overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{
			v1: &payloadV2.ExecutionPayloadV1,
			v2: payloadV2,
		})
	}
	return validForkchoiceResponse(headHash, &payloadID), nil
}

// ExchangeCapabilities returns the Engine API methods supported by this node.
func (e *EngineAPIV1) ExchangeCapabilities(ctx context.Context, clCapabilities []string) ([]string, error) {
	return supportedEngineMethods(), nil
}

// ExchangeTransitionConfigurationV1 exchanges transition configuration.
func (e *EngineAPIV1) ExchangeTransitionConfigurationV1(ctx context.Context, conf *TransitionConfigurationV1) (*TransitionConfigurationV1, error) {
	local := localTransitionConfiguration(e)
	if local.TerminalTotalDifficulty == nil && conf != nil {
		return conf, nil
	}
	return local, nil
}

func localTransitionConfiguration(e *EngineAPIV1) *TransitionConfigurationV1 {
	resp := &TransitionConfigurationV1{}
	if e == nil || e.api == nil || e.api.api == nil {
		return resp
	}
	cfg := e.api.api.GetChainConfig()
	if cfg == nil || cfg.TerminalTotalDifficulty == nil {
		return resp
	}
	ttd := hexutil.Big(*new(big.Int).Set(cfg.TerminalTotalDifficulty))
	resp.TerminalTotalDifficulty = &ttd
	return resp
}

func (e *EngineAPIV1) overlay() *engineOverlay {
	if e == nil || e.api == nil || e.api.api == nil {
		return nil
	}
	// engineOverlay is always initialized in NewAPI(); no lazy init needed.
	// Returning nil is safe — all overlay methods are nil-receiver safe.
	return e.api.api.engineOverlay
}

func (e *EngineAPIV1) currentHead() block.IBlock {
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	base := e.api.api.BlockChain().CurrentBlock()
	if overlay := e.overlay(); overlay != nil {
		return overlay.headBlock(base)
	}
	return base
}

func (e *EngineAPIV1) currentHeadHash() types.Hash {
	head := e.currentHead()
	if overlay := e.overlay(); overlay != nil {
		return overlay.hashForBlock(head, e.chainConfig())
	}
	return ethCompatibleBlockHash(head, e.chainConfig())
}

func (e *EngineAPIV1) parentHeader(hash types.Hash) *block.Header {
	if head := e.currentHead(); head != nil && e.currentHeadHash() == hash {
		return blockHeader(head)
	}
	if overlay := e.overlay(); overlay != nil {
		if blk := overlay.blockByHash(hash); blk != nil {
			return blockHeader(blk)
		}
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	header, _ := e.api.api.BlockChain().GetHeaderByHash(hash)
	if concrete, ok := header.(*block.Header); ok {
		return concrete
	}
	return nil
}

func (e *EngineAPIV1) builtPayload(payloadID PayloadID) *engineBuiltPayload {
	if overlay := e.overlay(); overlay != nil {
		return overlay.builtPayload(payloadID)
	}
	return nil
}

func (e *EngineAPIV1) chainConfig() *params.ChainConfig {
	if e == nil || e.api == nil || e.api.api == nil {
		return nil
	}
	return e.api.api.GetChainConfig()
}

func blockHeader(blk block.IBlock) *block.Header {
	if blk == nil {
		return nil
	}
	header, _ := blk.Header().(*block.Header)
	return header
}

func cloneWithdrawals(withdrawals []*Withdrawal) []*Withdrawal {
	if withdrawals == nil {
		return nil
	}
	cloned := make([]*Withdrawal, 0, len(withdrawals))
	for _, withdrawal := range withdrawals {
		if withdrawal == nil {
			continue
		}
		copy := *withdrawal
		cloned = append(cloned, &copy)
	}
	return cloned
}

func validPayloadResponse(hash types.Hash) *PayloadStatusV1 {
	return &PayloadStatusV1{
		Status:          PayloadStatusValid,
		LatestValidHash: &hash,
	}
}

func validForkchoiceResponse(hash types.Hash, payloadID *PayloadID) *ForkchoiceUpdatedResponseV3 {
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status:          PayloadStatusValid,
			LatestValidHash: &hash,
		},
		PayloadID: payloadID,
	}
}

func syncingPayloadResponse() *PayloadStatusV1 {
	return &PayloadStatusV1{
		Status: PayloadStatusSyncing,
	}
}

func syncingForkchoiceResponse() *ForkchoiceUpdatedResponseV3 {
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status: PayloadStatusSyncing,
		},
	}
}
