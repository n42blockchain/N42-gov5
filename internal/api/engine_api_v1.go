// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Engine API v1/v2 payload types for Paris and Shanghai forks.
// ExecutionPayloadV1 is the Paris wire format with parent/state/receipts
// roots, logs bloom, prevRandao, gas limit/used, timestamp, base fee,
// block hash and RLP-encoded transaction list. ExecutionPayloadV2
// extends it with withdrawals plus optional Cancun blob gas fields.

package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"

	"github.com/holiman/uint256"
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
	BlockValue       *hexutil.Big        `json:"blockValue"`
}

// TransitionConfigurationV1 exchanges merge transition parameters.
type TransitionConfigurationV1 struct {
	TerminalTotalDifficulty *hexutil.Big   `json:"terminalTotalDifficulty"`
	TerminalBlockHash       types.Hash     `json:"terminalBlockHash"`
	TerminalBlockNumber     hexutil.Uint64 `json:"terminalBlockNumber"`
}

// EngineAPIV1 provides Paris/Shanghai compatible Engine API methods.
type EngineAPIV1 struct {
	api                     *BlockChainAPI
	stateAdapter            *EngineStateAdapter // optional: when set, NewPayload persists state (ETH EL mode)
	missingAncestorObserver func(types.Hash)
}

// NewEngineAPIV1 creates a new Engine API v1/v2 handler.
func NewEngineAPIV1(api *BlockChainAPI) *EngineAPIV1 {
	return &EngineAPIV1{api: api}
}

// SetStateAdapter enables persistent state execution for ETH EL mode.
func (e *EngineAPIV1) SetStateAdapter(adapter *EngineStateAdapter) {
	e.stateAdapter = adapter
}

// SetMissingAncestorObserver installs a non-blocking sync trigger for payloads
// whose parent is not yet available locally.
func (e *EngineAPIV1) SetMissingAncestorObserver(observer func(types.Hash)) {
	e.missingAncestorObserver = observer
}

// HasBlockHash reports whether the Engine path can resolve a validated or
// persisted block header by hash.
func (e *EngineAPIV1) HasBlockHash(hash types.Hash) bool {
	return e.parentHeader(hash) != nil
}

// ImportSyncedParisBlock feeds a devp2p-retrieved Paris block through the same
// validation and overlay path as engine_newPayloadV1.
func (e *EngineAPIV1) ImportSyncedParisBlock(ctx context.Context, blk *block.Block) (*PayloadStatusV1, error) {
	payload := blockToExecutionPayloadV1(blk, e.chainConfig())
	if payload == nil {
		return nil, errors.New("cannot convert synced block to execution payload")
	}
	return e.NewPayloadV1(ctx, payload)
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

const engineRejectedAncestorReason = "links to previously rejected block"

// NewPayloadV1 processes a Paris execution payload.
func (e *EngineAPIV1) NewPayloadV1(ctx context.Context, payload *ExecutionPayloadV1) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	blk, err := executionPayloadV1ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blockHash := ethCompatibleBlockHash(blk, e.chainConfig())
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("blockhash mismatch"), nil
	}
	body := &ExecutionPayloadBodyV1{
		Transactions: cloneHexutilBytesList(payload.Transactions),
	}
	if latestValidHash, ok := e.rejectedAncestorLatestValidHash(payload.ParentHash); ok {
		return e.invalidPayloadStatus(engineRejectedAncestorReason, blk, blockHash, latestValidHash, body), nil
	}
	parent := e.parentHeader(payload.ParentHash)
	if err := validateExecutionPayloadHeader(blockHeader(blk), parent, e.chainConfig()); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), 0, uint64(payload.GasLimit)); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.chainConfig(), enginePayloadHashOptions{}); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	return e.executeOrValidateWithBody(blk, blockHash, payload.ParentHash, nil, nil, body)
}

// NewPayloadV2 processes a Shanghai execution payload.
func (e *EngineAPIV1) NewPayloadV2(ctx context.Context, payload *ExecutionPayloadV2) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	shanghai := isShanghaiActive(e.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp))
	if shanghai && payload.Withdrawals == nil {
		return nil, &engineInvalidParamsError{msg: "missing withdrawals"}
	}
	if !shanghai && payload.Withdrawals != nil {
		return nil, &engineInvalidParamsError{msg: "withdrawals before Shanghai"}
	}
	if payload.BlobGasUsed != nil || payload.ExcessBlobGas != nil {
		return nil, &engineInvalidParamsError{msg: "blob fields not allowed before Cancun"}
	}
	blk, err := executionPayloadV2ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blockHash := ethCompatibleEngineBlockHash(blk, e.chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: payload.Withdrawals != nil,
		withdrawals:        payload.Withdrawals,
	})
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("blockhash mismatch"), nil
	}
	body := &ExecutionPayloadBodyV1{
		Transactions: cloneHexutilBytesList(payload.Transactions),
		Withdrawals:  clonePayloadBodyWithdrawals(payload.Withdrawals),
	}
	if latestValidHash, ok := e.rejectedAncestorLatestValidHash(payload.ParentHash); ok {
		return e.invalidPayloadStatus(engineRejectedAncestorReason, blk, blockHash, latestValidHash, body), nil
	}
	parent := e.parentHeader(payload.ParentHash)
	if err := validateExecutionPayloadHeader(blockHeader(blk), parent, e.chainConfig()); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), 0, uint64(payload.GasLimit)); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: payload.Withdrawals != nil,
		withdrawals:        payload.Withdrawals,
	}); err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	return e.executeOrValidateWithBody(blk, blockHash, payload.ParentHash, nil, nil, body)
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
			BlockValue:       enginePayloadBlockValue(built.blockValue),
		}, nil
	}
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV1 updates the forkchoice state for Paris.
func (e *EngineAPIV1) ForkchoiceUpdatedV1(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV1) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	head, headHash, syncResp, err := e.resolveForkchoiceState(state)
	if syncResp != nil || err != nil {
		return syncResp, err
	}
	if attrs == nil {
		return validForkchoiceResponse(headHash, nil), nil
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return nil, &engineInvalidPayloadAttributesError{msg: "payload timestamp must be greater than parent"}
	}
	payload, blockValue := e.buildExecutionPayloadV1WithValue(head, headHash, attrs)
	payloadID := makePayloadID(headHash, uint64(attrs.Timestamp), attrs.SuggestedFeeRecipient, attrs.PrevRandao)
	if overlay := e.overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{
			v1:         payload,
			v2:         &ExecutionPayloadV2{ExecutionPayloadV1: *payload},
			blockValue: blockValue,
		})
	}
	return validForkchoiceResponse(headHash, &payloadID), nil
}

// ForkchoiceUpdatedV2 updates the forkchoice state for Shanghai.
func (e *EngineAPIV1) ForkchoiceUpdatedV2(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV2) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	head, headHash, syncResp, err := e.resolveForkchoiceState(state)
	if syncResp != nil || err != nil {
		return syncResp, err
	}
	if attrs == nil {
		return validForkchoiceResponse(headHash, nil), nil
	}
	nextNumber := uint64(0)
	if head != nil && head.Number64() != nil {
		nextNumber = head.Number64().Uint64() + 1
	}
	shanghai := isShanghaiActive(e.chainConfig(), nextNumber, uint64(attrs.Timestamp))
	if shanghai && attrs.Withdrawals == nil {
		return nil, &engineInvalidPayloadAttributesError{msg: "missing withdrawals"}
	}
	if !shanghai && attrs.Withdrawals != nil {
		return nil, &engineInvalidPayloadAttributesError{msg: "withdrawals before Shanghai"}
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return nil, &engineInvalidPayloadAttributesError{msg: "payload timestamp must be greater than parent"}
	}
	payloadV2, blockValue := e.buildExecutionPayloadV2StatefulWithValue(head, headHash, attrs)
	if payloadV2 == nil {
		payloadV2 = buildExecutionPayloadV2(head, headHash, attrs, e.chainConfig())
		blockValue = new(big.Int)
	}
	if payloadV2 == nil {
		return invalidForkchoiceResponse("failed to build payload"), nil
	}
	withdrawalsRoot := withdrawalsRoot(attrs.Withdrawals)
	payloadID := makePayloadIDWithExtras(headHash, uint64(attrs.Timestamp), attrs.SuggestedFeeRecipient, attrs.PrevRandao, withdrawalsRoot[:])
	if overlay := e.overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{
			v1:         &payloadV2.ExecutionPayloadV1,
			v2:         payloadV2,
			blockValue: blockValue,
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
	if e == nil {
		return nil
	}
	// EthEL mode: read from chaindata via the state adapter. The eth-el
	// node constructs an EngineAPIV1 with nil bc and a non-nil
	// stateAdapter; without this branch ForkchoiceUpdated would always
	// return SYNCING because the BlockChain path returns nil.
	if e.stateAdapter != nil {
		if blk := e.stateAdapter.CurrentHead(); blk != nil {
			return blk
		}
		return nil
	}
	if e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	base := e.api.api.BlockChain().CurrentBlock()
	if overlay := e.overlay(); overlay != nil {
		return overlay.headBlock(base)
	}
	return base
}

func (e *EngineAPIV1) currentHeadHash() types.Hash {
	if e != nil && e.stateAdapter != nil {
		return e.stateAdapter.CurrentHeadHash()
	}
	head := e.currentHead()
	if overlay := e.overlay(); overlay != nil {
		return overlay.hashForBlock(head, e.chainConfig())
	}
	return ethCompatibleBlockHash(head, e.chainConfig())
}

func (e *EngineAPIV1) rejectedAncestorLatestValidHash(hash types.Hash) (*types.Hash, bool) {
	if hash == (types.Hash{}) {
		return nil, false
	}
	overlay := e.overlay()
	if overlay == nil {
		return nil, false
	}
	headHash := e.currentHeadHash()
	for steps, current := 0, hash; current != (types.Hash{}) && steps < maxOverlayBlocks*2; steps++ {
		if latestValidHash, ok := overlay.rejectedLatestValidHash(current); ok {
			return latestValidHash, true
		}
		if current == headHash || overlay.blockValidated(current) || e.canonicalHeader(current) != nil {
			return nil, false
		}
		blk := overlay.blockByHash(current)
		if blk == nil {
			return nil, false
		}
		header := blockHeader(blk)
		if header == nil {
			return nil, false
		}
		current = header.ParentHash
	}
	return nil, false
}

func latestValidHashForParent(parentHash types.Hash, parent *block.Header) *types.Hash {
	if parent == nil {
		return nil
	}
	hash := parentHash
	return &hash
}

func (e *EngineAPIV1) parentHeader(hash types.Hash) *block.Header {
	if head := e.currentHead(); head != nil && e.currentHeadHash() == hash {
		return blockHeader(head)
	}
	if overlay := e.overlay(); overlay != nil {
		if overlay.blockValidated(hash) {
			if blk := overlay.blockByHash(hash); blk != nil {
				return blockHeader(blk)
			}
		}
	}
	// EthEL mode: read from chaindata via the state adapter. Without
	// this the nil-bc path (used by cmd/eth-el) cannot resolve the
	// parent header for NewPayload verification.
	if e != nil && e.stateAdapter != nil {
		return e.stateAdapter.HeaderByHash(hash)
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	header, _ := e.api.api.BlockChain().GetHeaderByHash(hash)
	if concrete, ok := header.(*block.Header); ok && ethCompatibleHeaderHash(concrete, e.chainConfig()) == hash {
		return concrete
	}
	return nil
}

func (e *EngineAPIV1) canonicalHeader(hash types.Hash) *block.Header {
	if hash == (types.Hash{}) {
		return nil
	}
	if head := e.canonicalHead(); head != nil && ethCompatibleBlockHash(head, e.chainConfig()) == hash {
		return blockHeader(head)
	}
	if e != nil && e.stateAdapter != nil {
		if hdr := e.stateAdapter.HeaderByHash(hash); hdr != nil {
			return hdr
		}
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	header, _ := e.api.api.BlockChain().GetHeaderByHash(hash)
	if concrete, ok := header.(*block.Header); ok && ethCompatibleHeaderHash(concrete, e.chainConfig()) == hash {
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

func (e *EngineAPIV1) blockByHash(hash types.Hash) block.IBlock {
	if hash == (types.Hash{}) {
		return nil
	}
	if overlay := e.overlay(); overlay != nil {
		if blk := overlay.blockByHash(hash); blk != nil {
			return blk
		}
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	blk, _ := e.api.api.BlockChain().GetBlockByHash(hash)
	if ethCompatibleBlockHash(blk, e.chainConfig()) == hash {
		return blk
	}
	return nil
}

func (e *EngineAPIV1) headerByHash(hash types.Hash) *block.Header {
	if hash == (types.Hash{}) {
		return nil
	}
	if blk := e.blockByHash(hash); blk != nil {
		return blockHeader(blk)
	}
	// EthEL mode: the in-memory overlay only holds recent blocks and
	// BlockChain() is nil, so after a restart (empty overlay) or once a ref
	// falls past maxOverlayBlocks the header must come from chaindata via the
	// state adapter — mirroring parentHeader. Without this, forkchoiceUpdated
	// hard-rejects a valid safe/finalized hash with -38002 instead of
	// accepting it, until the CL happens to re-deliver those payloads.
	if e != nil && e.stateAdapter != nil {
		if hdr := e.stateAdapter.HeaderByHash(hash); hdr != nil {
			return hdr
		}
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	header, _ := e.api.api.BlockChain().GetHeaderByHash(hash)
	if concrete, ok := header.(*block.Header); ok && ethCompatibleHeaderHash(concrete, e.chainConfig()) == hash {
		return concrete
	}
	return nil
}

func (e *EngineAPIV1) isAncestorHash(descendantHash, ancestorHash types.Hash) bool {
	if ancestorHash == (types.Hash{}) {
		return true
	}
	for steps, hash := 0, descendantHash; hash != (types.Hash{}) && steps < maxOverlayBlocks*2; steps++ {
		if hash == ancestorHash {
			return true
		}
		header := e.headerByHash(hash)
		if header == nil {
			return false
		}
		hash = header.ParentHash
	}
	return false
}

func (e *EngineAPIV1) validateForkchoiceState(state *ForkchoiceStateV1) error {
	if state.HeadBlockHash == (types.Hash{}) {
		return &engineInvalidForkchoiceStateError{msg: "head block hash is zero"}
	}
	headKnown := e.headerByHash(state.HeadBlockHash) != nil
	allowUnknownHeadRef := func(hash types.Hash) bool {
		return !headKnown && hash == state.HeadBlockHash
	}
	safeKnown := false
	if state.SafeBlockHash != (types.Hash{}) {
		// Existence is a header-level fact; use headerByHash so the eth-el
		// state-adapter fallback resolves refs that are no longer in the
		// overlay (post-restart, or older than maxOverlayBlocks). blockByHash
		// has no such fallback and would spuriously report a valid finalized
		// ref as unknown.
		safeKnown = e.headerByHash(state.SafeBlockHash) != nil
		if !safeKnown && !allowUnknownHeadRef(state.SafeBlockHash) {
			return &engineInvalidForkchoiceStateError{msg: "unknown safe block hash"}
		}
		if headKnown && safeKnown && !e.isAncestorHash(state.HeadBlockHash, state.SafeBlockHash) {
			return &engineInvalidForkchoiceStateError{msg: "safe block hash is not an ancestor of head block hash"}
		}
	}
	finalizedKnown := false
	if state.FinalizedBlockHash != (types.Hash{}) {
		finalizedKnown = e.headerByHash(state.FinalizedBlockHash) != nil
		if !finalizedKnown && !allowUnknownHeadRef(state.FinalizedBlockHash) {
			return &engineInvalidForkchoiceStateError{msg: "unknown finalized block hash"}
		}
		if headKnown && finalizedKnown && !e.isAncestorHash(state.HeadBlockHash, state.FinalizedBlockHash) {
			return &engineInvalidForkchoiceStateError{msg: "finalized block hash is not an ancestor of head block hash"}
		}
		if state.SafeBlockHash != (types.Hash{}) && safeKnown && finalizedKnown && !e.isAncestorHash(state.SafeBlockHash, state.FinalizedBlockHash) {
			return &engineInvalidForkchoiceStateError{msg: "finalized block hash is not an ancestor of safe block hash"}
		}
	}
	return nil
}

func (e *EngineAPIV1) resolveForkchoiceState(state *ForkchoiceStateV1) (block.IBlock, types.Hash, *ForkchoiceUpdatedResponseV3, error) {
	head := e.currentHead()
	if head == nil {
		return nil, types.Hash{}, syncingForkchoiceResponse(), nil
	}
	if err := e.validateForkchoiceState(state); err != nil {
		return nil, types.Hash{}, nil, err
	}
	headHash := e.currentHeadHash()
	if state.HeadBlockHash != headHash {
		if latestValidHash, ok := e.rejectedAncestorLatestValidHash(state.HeadBlockHash); ok {
			return nil, types.Hash{}, invalidForkchoiceResponseWithLatestValid(engineRejectedAncestorReason, latestValidHash), nil
		}
		if !e.adoptForkchoiceHead(state.HeadBlockHash) {
			return nil, types.Hash{}, syncingForkchoiceResponse(), nil
		}
		head = e.currentHead()
		headHash = e.currentHeadHash()
	}
	if overlay := e.overlay(); overlay != nil {
		overlay.setForkchoiceHashes(state.SafeBlockHash, state.FinalizedBlockHash)
	}
	if e.stateAdapter != nil {
		if err := e.persistForkchoiceHead(state.HeadBlockHash); err != nil {
			log.Warn("Persist forkchoice head error", "err", err)
			return nil, types.Hash{}, syncingForkchoiceResponse(), nil
		}
		if err := e.stateAdapter.ForkchoiceUpdated(state.HeadBlockHash, state.SafeBlockHash, state.FinalizedBlockHash); err != nil {
			log.Warn("ForkchoiceUpdated error", "err", err)
		} else {
			head = e.currentHead()
			headHash = e.currentHeadHash()
		}
	}
	return head, headHash, nil, nil
}

func (e *EngineAPIV1) persistForkchoiceHead(headHash types.Hash) error {
	if e == nil || e.stateAdapter == nil || headHash == (types.Hash{}) {
		return nil
	}
	currentHash := e.stateAdapter.CurrentHeadHash()
	if currentHash == headHash {
		return nil
	}
	overlay := e.overlay()
	if overlay == nil {
		return nil
	}

	type stagedPayload struct {
		hash types.Hash
		blk  *block.Block
		body *ExecutionPayloadBodyV1
	}
	var reversePath []stagedPayload
	ancestorHash := currentHash
	for hash := headHash; hash != currentHash; {
		_, canonical, err := e.stateAdapter.canonicalBlockNumber(hash)
		if err != nil {
			return err
		}
		if canonical {
			ancestorHash = hash
			break
		}
		staged := overlay.blockByHash(hash)
		concrete, ok := staged.(*block.Block)
		if !ok || concrete == nil {
			return fmt.Errorf("staged forkchoice block not found: %s", hash.Hex())
		}
		header := blockHeader(concrete)
		if header == nil {
			return fmt.Errorf("staged forkchoice header not found: %s", hash.Hex())
		}
		reversePath = append(reversePath, stagedPayload{
			hash: hash,
			blk:  concrete,
			body: overlay.payloadBodyByHash(hash),
		})
		hash = header.ParentHash
	}
	if ancestorHash != currentHash {
		if err := e.stateAdapter.reorgCanonicalTo(ancestorHash); err != nil {
			return err
		}
	}

	for i := len(reversePath) - 1; i >= 0; i-- {
		payload := reversePath[i]
		header := blockHeader(payload.blk)
		var parentBeaconRoot *types.Hash
		if header != nil && header.ParentBeaconRoot != nil {
			root := *header.ParentBeaconRoot
			parentBeaconRoot = &root
		}
		var executionRequests []hexutil.Bytes
		if payload.body != nil {
			executionRequests = cloneHexutilBytesList(payload.body.executionRequests)
		}
		result, err := e.stateAdapter.executePayloadDetailed(
			payload.blk,
			parentBeaconRoot,
			executionRequests,
			bodyWithdrawals(payload.body),
			false,
		)
		if err != nil {
			return err
		}
		if result.validationError != nil {
			return result.validationError
		}
	}
	return nil
}

func (e *EngineAPIV1) adoptForkchoiceHead(hash types.Hash) bool {
	if hash == (types.Hash{}) {
		return false
	}
	overlay := e.overlay()
	if overlay == nil {
		return false
	}
	if !overlay.blockValidated(hash) {
		return false
	}
	if e.canonicalHeader(hash) != nil {
		// The persistent ETH EL adapter writes a validated payload before
		// forkchoiceUpdated. Even though the header is already canonical in
		// chaindata, the shared overlay still has to adopt it so public eth_*
		// RPC methods observe the same latest head as the Engine API.
		if blk := overlay.blockByHash(hash); blk != nil {
			overlay.importBlock(blk, hash, overlay.receiptsByBlockHash(hash))
		}
		return true
	}
	blk := overlay.blockByHash(hash)
	if blk == nil {
		return false
	}
	overlay.importBlock(blk, hash, overlay.receiptsByBlockHash(hash))
	return true
}

func (e *EngineAPIV1) chainConfig() *params.ChainConfig {
	if e == nil || e.api == nil || e.api.api == nil {
		return nil
	}
	return e.api.api.GetChainConfig()
}

func (e *EngineAPIV1) buildExecutionPayloadV1(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV1) *ExecutionPayloadV1 {
	payload, _ := e.buildExecutionPayloadV1WithValue(parent, parentHash, attrs)
	return payload
}

func (e *EngineAPIV1) buildExecutionPayloadV1WithValue(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV1) (*ExecutionPayloadV1, *big.Int) {
	result := e.buildExecutionPayloadStateful(parent, parentHash, attrs, nil, nil)
	if result != nil && result.block != nil {
		if payload := blockToExecutionPayloadV1(result.block, e.chainConfig()); payload != nil {
			return payload, result.blockValue
		}
	}
	return buildExecutionPayloadV1(parent, parentHash, attrs, e.chainConfig()), new(big.Int)
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

func (e *EngineAPIV1) buildExecutionPayloadV2Stateful(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV2) *ExecutionPayloadV2 {
	payload, _ := e.buildExecutionPayloadV2StatefulWithValue(parent, parentHash, attrs)
	return payload
}

func (e *EngineAPIV1) buildExecutionPayloadV2StatefulWithValue(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV2) (*ExecutionPayloadV2, *big.Int) {
	if attrs == nil {
		return nil, nil
	}
	result := e.buildExecutionPayloadStateful(parent, parentHash, &attrs.PayloadAttributesV1, attrs.Withdrawals, nil)
	if result == nil || result.block == nil {
		return nil, nil
	}
	v1 := blockToExecutionPayloadV1(result.block, e.chainConfig())
	if v1 == nil {
		return nil, nil
	}
	payload := &ExecutionPayloadV2{
		ExecutionPayloadV1: *v1,
		Withdrawals:        cloneWithdrawals(attrs.Withdrawals),
	}
	payload.BlockHash = ethCompatibleEngineBlockHash(result.block, e.chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: payload.Withdrawals != nil,
		withdrawals:        payload.Withdrawals,
	})
	return payload, result.blockValue
}

func enginePayloadBlockValue(value *big.Int) *hexutil.Big {
	if value == nil {
		value = new(big.Int)
	}
	return (*hexutil.Big)(new(big.Int).Set(value))
}

func isShanghaiActive(cfg *params.ChainConfig, blockNumber, timestamp uint64) bool {
	return cfg != nil && cfg.IsShanghaiAt(blockNumber, timestamp)
}

func validPayloadResponse(hash types.Hash) *PayloadStatusV1 {
	return &PayloadStatusV1{
		Status:          PayloadStatusValid,
		LatestValidHash: &hash,
	}
}

func invalidPayloadResponseForParent(reason string, parentHash types.Hash, parent *block.Header) *PayloadStatusV1 {
	if parent != nil {
		return invalidPayloadResponseWithLatestValid(reason, &parentHash)
	}
	return invalidPayloadResponse(reason)
}

func (e *EngineAPIV1) invalidPayloadStatus(reason string, blk block.IBlock, blockHash types.Hash, latestValidHash *types.Hash, body *ExecutionPayloadBodyV1) *PayloadStatusV1 {
	if overlay := e.overlay(); overlay != nil && blk != nil && blockHash != (types.Hash{}) {
		overlay.stageRejectedBlockWithBody(blk, blockHash, latestValidHash, body)
	}
	return invalidPayloadResponseWithLatestValid(reason, latestValidHash)
}

// executeOrValidate either persists state (ETH EL) or just validates (N42).
func (e *EngineAPIV1) executeOrValidate(blk block.IBlock, blockHash, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes) (*PayloadStatusV1, error) {
	return e.executeOrValidateWithBody(blk, blockHash, parentHash, parentBeaconRoot, expectedRequests, nil)
}

func (e *EngineAPIV1) executeOrValidateWithBody(blk block.IBlock, blockHash, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, body *ExecutionPayloadBodyV1) (*PayloadStatusV1, error) {
	if latestValidHash, ok := e.rejectedAncestorLatestValidHash(parentHash); ok {
		return e.invalidPayloadStatus(engineRejectedAncestorReason, blk, blockHash, latestValidHash, body), nil
	}
	if e.parentHeader(parentHash) == nil {
		if overlay := e.overlay(); overlay != nil {
			overlay.stageBlockWithBody(blk, blockHash, nil, nil, false, body)
		}
		if e.missingAncestorObserver != nil {
			e.missingAncestorObserver(parentHash)
		}
		return acceptedPayloadResponse(), nil
	}

	// ETH EL mode: execute and persist.
	if e.stateAdapter != nil {
		if overlay := e.overlay(); overlay != nil && overlay.blockValidated(blockHash) {
			return validPayloadResponse(blockHash), nil
		}
		concreteBlock, ok := blk.(*block.Block)
		if !ok {
			return invalidPayloadResponse("unexpected block type"), nil
		}
		result, err := e.stateAdapter.validatePayloadDetailed(
			concreteBlock,
			parentHash,
			parentBeaconRoot,
			expectedRequests,
			bodyWithdrawals(body),
			e.parentStateOverlay(parentHash),
		)
		if err != nil {
			return e.invalidPayloadStatus(err.Error(), blk, blockHash, &parentHash, body), nil
		}
		if result.validationError != nil {
			return e.invalidPayloadStatus(result.validationError.Error(), blk, blockHash, &parentHash, body), nil
		}
		if overlay := e.overlay(); overlay != nil {
			overlay.stageBlockWithBody(blk, blockHash, result.stateOverlay, result.receipts, true, body)
		}
		return validPayloadResponse(blockHash), nil
	}

	// N42 mode: validate only.
	overlayState, receipts, err := e.executePayloadOnParentStateAndValidateWithWithdrawals(blk, parentHash, parentBeaconRoot, expectedRequests, bodyWithdrawals(body))
	if err != nil {
		return e.invalidPayloadStatus(err.Error(), blk, blockHash, &parentHash, body), nil
	}
	if overlay := e.overlay(); overlay != nil {
		overlay.stageBlockWithBody(blk, blockHash, overlayState, receipts, true, body)
	}
	return validPayloadResponse(blockHash), nil
}

func cloneExecutionValidationBlock(blk block.IBlock) (block.IBlock, bool) {
	concreteBlock, ok := blk.(*block.Block)
	if !ok || concreteBlock == nil {
		return nil, false
	}
	header := blockHeader(concreteBlock)
	if header == nil {
		return nil, false
	}
	headerCopy := *header
	headerCopy.Extra = append([]byte(nil), header.Extra...)
	if header.Number != nil {
		headerCopy.Number = new(uint256.Int).Set(header.Number)
	}
	if header.Difficulty != nil {
		headerCopy.Difficulty = new(uint256.Int).Set(header.Difficulty)
	}
	if header.BaseFee != nil {
		headerCopy.BaseFee = new(uint256.Int).Set(header.BaseFee)
	}
	if header.WithdrawalsHash != nil {
		withdrawalsHash := *header.WithdrawalsHash
		headerCopy.WithdrawalsHash = &withdrawalsHash
	}
	if header.BlobGasUsed != nil {
		blobGasUsed := *header.BlobGasUsed
		headerCopy.BlobGasUsed = &blobGasUsed
	}
	if header.ExcessBlobGas != nil {
		excessBlobGas := *header.ExcessBlobGas
		headerCopy.ExcessBlobGas = &excessBlobGas
	}
	if header.ParentBeaconRoot != nil {
		parentBeaconRoot := *header.ParentBeaconRoot
		headerCopy.ParentBeaconRoot = &parentBeaconRoot
	}
	if header.RequestsHash != nil {
		requestsHash := *header.RequestsHash
		headerCopy.RequestsHash = &requestsHash
	}
	txs := append([]*transaction.Transaction(nil), concreteBlock.Transactions()...)
	return block.NewBlock(&headerCopy, txs), true
}

func bodyWithdrawals(body *ExecutionPayloadBodyV1) []*Withdrawal {
	if body == nil {
		return nil
	}
	return body.Withdrawals
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

func acceptedPayloadResponse() *PayloadStatusV1 {
	return &PayloadStatusV1{
		Status: PayloadStatusAccepted,
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
