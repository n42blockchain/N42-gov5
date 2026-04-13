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

// EIP-4844: Shard Blob Transactions - Engine API Extensions
// Reference: https://github.com/ethereum/execution-apis/blob/main/src/engine/cancun.md

package api

import (
	"context"
	"strconv"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// =============================================================================
// Engine API Types for EIP-4844 (Cancun)
// =============================================================================

// ExecutionPayloadV3 extends ExecutionPayloadV2 with blob fields
type ExecutionPayloadV3 struct {
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
	Withdrawals   []*Withdrawal   `json:"withdrawals"`

	// EIP-4844 fields
	BlobGasUsed   *hexutil.Uint64 `json:"blobGasUsed"`
	ExcessBlobGas *hexutil.Uint64 `json:"excessBlobGas"`
}

// BlobsBundleV1 contains the blobs, commitments, and proofs for a block
type BlobsBundleV1 struct {
	Commitments []hexutil.Bytes `json:"commitments"` // KZG commitments
	Proofs      []hexutil.Bytes `json:"proofs"`      // KZG proofs
	Blobs       []hexutil.Bytes `json:"blobs"`       // Blob data
}

// PayloadAttributesV3 extends PayloadAttributesV2 with parent beacon block root
type PayloadAttributesV3 struct {
	Timestamp             hexutil.Uint64 `json:"timestamp"`
	PrevRandao            types.Hash     `json:"prevRandao"`
	SuggestedFeeRecipient types.Address  `json:"suggestedFeeRecipient"`
	Withdrawals           []*Withdrawal  `json:"withdrawals"`
	ParentBeaconBlockRoot *types.Hash    `json:"parentBeaconBlockRoot"`
}

// GetPayloadResponseV3 is the response for engine_getPayloadV3
type GetPayloadResponseV3 struct {
	ExecutionPayload      *ExecutionPayloadV3 `json:"executionPayload"`
	BlockValue            hexutil.Uint64      `json:"blockValue"`
	BlobsBundle           *BlobsBundleV1      `json:"blobsBundle"`
	ShouldOverrideBuilder bool                `json:"shouldOverrideBuilder"`
}

// ForkchoiceUpdatedResponseV3 includes blob-related validation status
type ForkchoiceUpdatedResponseV3 struct {
	PayloadStatus PayloadStatusV1 `json:"payloadStatus"`
	PayloadID     *PayloadID      `json:"payloadId"`
}

// PayloadStatusV1 represents the status of payload validation
type PayloadStatusV1 struct {
	Status          string      `json:"status"`
	LatestValidHash *types.Hash `json:"latestValidHash"`
	ValidationError *string     `json:"validationError"`
}

// PayloadID is a unique identifier for a payload
type PayloadID [8]byte

// MarshalText implements encoding.TextMarshaler
func (id PayloadID) MarshalText() ([]byte, error) {
	return hexutil.Bytes(id[:]).MarshalText()
}

// UnmarshalText implements encoding.TextUnmarshaler
func (id *PayloadID) UnmarshalText(text []byte) error {
	var b hexutil.Bytes
	if err := b.UnmarshalText(text); err != nil {
		return err
	}
	if len(b) != 8 {
		return errInvalidPayloadID
	}
	copy(id[:], b)
	return nil
}

// Withdrawal represents a validator withdrawal
type Withdrawal struct {
	Index          hexutil.Uint64 `json:"index"`
	ValidatorIndex hexutil.Uint64 `json:"validatorIndex"`
	Address        types.Address  `json:"address"`
	Amount         hexutil.Uint64 `json:"amount"`
}

// =============================================================================
// Payload Status Constants
// =============================================================================

const (
	// PayloadStatusValid indicates the payload is valid
	PayloadStatusValid = "VALID"

	// PayloadStatusInvalid indicates the payload is invalid
	PayloadStatusInvalid = "INVALID"

	// PayloadStatusSyncing indicates the client is syncing
	PayloadStatusSyncing = "SYNCING"

	// PayloadStatusAccepted indicates the payload was accepted for processing
	PayloadStatusAccepted = "ACCEPTED"

	// PayloadStatusInvalidBlockHash indicates the block hash is invalid
	PayloadStatusInvalidBlockHash = "INVALID_BLOCK_HASH"
)

// =============================================================================
// Engine API Blob Methods
// =============================================================================

// EngineAPIBlob provides Engine API methods for EIP-4844 blob transactions
type EngineAPIBlob struct {
	api *BlockChainAPI
	v1i *EngineAPIV1
}

// NewEngineAPIBlob creates a new Engine API blob instance
func NewEngineAPIBlob(api *BlockChainAPI) *EngineAPIBlob {
	return &EngineAPIBlob{
		api: api,
		v1i: &EngineAPIV1{api: api},
	}
}

func (e *EngineAPIBlob) v1() *EngineAPIV1 {
	if e == nil {
		return nil
	}
	if e.v1i == nil {
		e.v1i = &EngineAPIV1{api: e.api}
	}
	return e.v1i
}

func (e *EngineAPIBlob) SetStateAdapter(adapter *EngineStateAdapter) {
	if v1 := e.v1(); v1 != nil {
		v1.SetStateAdapter(adapter)
	}
}

// NewPayloadV3 processes a new execution payload with blob support
// engine_newPayloadV3
func (e *EngineAPIBlob) NewPayloadV3(ctx context.Context, payload *ExecutionPayloadV3, expectedBlobVersionedHashes []types.Hash, parentBeaconBlockRoot *types.Hash) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	blockNumber := uint64(payload.BlockNumber)
	timestamp := uint64(payload.Timestamp)
	// Engine API spec: newPayloadV3 must not be used for Pectra+ payloads.
	if cfg := e.v1().chainConfig(); cfg != nil && cfg.PectraTime != nil && cfg.IsPectra(timestamp) {
		return nil, &engineUnsupportedForkError{msg: "newPayloadV3 called for Pectra+ timestamp, use newPayloadV4"}
	}
	if cfg := e.v1().chainConfig(); cfg != nil {
		if !cfg.IsCancunAt(blockNumber, timestamp) {
			if payload.BlobGasUsed != nil || payload.ExcessBlobGas != nil || len(expectedBlobVersionedHashes) > 0 {
				return nil, &engineInvalidParamsError{msg: "blob fields not allowed before Cancun"}
			}
		} else if payload.BlobGasUsed == nil || payload.ExcessBlobGas == nil {
			return nil, &engineInvalidParamsError{msg: "missing blob gas fields"}
		}
	}
	if parentBeaconBlockRoot == nil {
		return invalidPayloadResponse("missing parent beacon block root"), nil
	}
	if payload.Withdrawals == nil {
		return invalidPayloadResponse("missing withdrawals in Cancun+ payload"), nil
	}

	// Validate blob gas and versioned hashes for Cancun
	maxBlobGas := uint64(transaction.MaxBlobGasPerBlock)
	maxBlobs := uint64(transaction.MaxBlobsPerBlock)
	gasPerBlob := uint64(transaction.BlobTxBlobGasPerBlob)
	if cfg := e.v1().chainConfig(); cfg != nil {
		maxBlobGas = cfg.BlobMaxGasPerBlock(uint64(payload.Timestamp))
		maxBlobs = cfg.BlobMaxBlobsPerBlock(uint64(payload.Timestamp))
		gasPerBlob = cfg.BlobGasPerBlob(uint64(payload.Timestamp))
	}
	if resp := validateBlobGasAndHashes(
		payload.BlobGasUsed,
		payload.ExcessBlobGas,
		expectedBlobVersionedHashes,
		maxBlobGas,
		maxBlobs,
		gasPerBlob,
	); resp != nil {
		return resp, nil
	}
	if err := ValidateBlobTransactions(payload.Transactions, expectedBlobVersionedHashes); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blk, err := executionPayloadV3ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blk = applyExecutionPayloadForkFields(blk, nil, nil, nil, parentBeaconBlockRoot, nil)
	blockHash := ethCompatibleEngineBlockHash(blk, e.v1().chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   parentBeaconBlockRoot,
	})
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("blockhash mismatch"), nil
	}
	body := &ExecutionPayloadBodyV1{
		Transactions: cloneHexutilBytesList(payload.Transactions),
		Withdrawals:  clonePayloadBodyWithdrawals(payload.Withdrawals),
	}
	parent := e.v1().parentHeader(payload.ParentHash)
	if latestValidHash, ok := e.v1().rejectedAncestorLatestValidHash(payload.ParentHash); ok {
		return e.v1().invalidPayloadStatus(engineRejectedAncestorReason, blk, blockHash, latestValidHash, body), nil
	}
	if err := validateExecutionPayloadHeader(blockHeader(blk), parent, e.v1().chainConfig()); err != nil {
		return e.v1().invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.v1().chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), uint64(*payload.ExcessBlobGas), uint64(payload.GasLimit)); err != nil {
		return e.v1().invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.v1().chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   parentBeaconBlockRoot,
	}); err != nil {
		return e.v1().invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
	}
	var (
		overlayState *engineStateOverlay
		receipts     block.Receipts
	)
	if parent != nil && e.v1().stateAdapter == nil {
		overlayState, receipts, err = e.v1().executePayloadOnParentStateAndValidateWithWithdrawals(blk, payload.ParentHash, parentBeaconBlockRoot, nil, payload.Withdrawals)
		if err != nil {
			return e.v1().invalidPayloadStatus(err.Error(), blk, blockHash, latestValidHashForParent(payload.ParentHash, parent), body), nil
		}
	}
	if parent == nil {
		if overlay := e.v1().overlay(); overlay != nil {
			overlay.stageBlockWithBody(blk, blockHash, nil, nil, false, body)
		}
		return acceptedPayloadResponse(), nil
	}
	if e.v1().stateAdapter != nil {
		return e.v1().executeOrValidateWithBody(blk, blockHash, payload.ParentHash, parentBeaconBlockRoot, nil, body)
	}
	headHash := e.v1().currentHeadHash()
	if headHash == (types.Hash{}) || payload.ParentHash != headHash {
		if overlay := e.v1().overlay(); overlay != nil {
			overlay.stageBlockWithBody(blk, blockHash, overlayState, receipts, true, body)
		}
		return acceptedPayloadResponse(), nil
	}
	if overlay := e.v1().overlay(); overlay != nil {
		overlay.stageBlockWithBody(blk, blockHash, overlayState, receipts, true, body)
		overlay.importBlockWithBody(blk, blockHash, receipts, body)
	}
	return validPayloadResponse(blockHash), nil
}

// GetPayloadV3 retrieves a payload with blob bundle
// engine_getPayloadV3
func (e *EngineAPIBlob) GetPayloadV3(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV3, error) {
	if built := e.v1().builtPayload(payloadID); built != nil && built.v3 != nil {
		return &GetPayloadResponseV3{
			ExecutionPayload:      built.v3,
			BlockValue:            hexutil.Uint64(0),
			BlobsBundle:           built.blobsBundle,
			ShouldOverrideBuilder: false,
		}, nil
	}
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV3 updates the fork choice with blob support
// engine_forkchoiceUpdatedV3
func (e *EngineAPIBlob) ForkchoiceUpdatedV3(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV3) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	head, headHash, syncResp, err := e.v1().resolveForkchoiceState(state)
	if syncResp != nil || err != nil {
		return syncResp, err
	}
	if attrs == nil {
		return validForkchoiceResponse(headHash, nil), nil
	}
	if attrs.Withdrawals == nil {
		return nil, &engineInvalidPayloadAttributesError{msg: "missing withdrawals"}
	}
	if attrs.ParentBeaconBlockRoot == nil {
		return nil, &engineInvalidPayloadAttributesError{msg: "missing parent beacon block root"}
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return nil, &engineInvalidPayloadAttributesError{msg: "payload timestamp must be greater than parent"}
	}
	payload := e.buildExecutionPayloadV3Stateful(head, headHash, attrs)
	if payload == nil {
		payload = buildExecutionPayloadV3(head, headHash, attrs, e.v1().chainConfig())
	}
	if payload == nil {
		return invalidForkchoiceResponse("failed to build payload"), nil
	}
	withdrawalsRoot := withdrawalsRoot(attrs.Withdrawals)
	payloadID := makePayloadIDWithExtras(
		headHash,
		uint64(attrs.Timestamp),
		attrs.SuggestedFeeRecipient,
		attrs.PrevRandao,
		withdrawalsRoot[:],
		attrs.ParentBeaconBlockRoot[:],
	)
	if overlay := e.v1().overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{
			v1: &ExecutionPayloadV1{
				ParentHash:    payload.ParentHash,
				FeeRecipient:  payload.FeeRecipient,
				StateRoot:     payload.StateRoot,
				ReceiptsRoot:  payload.ReceiptsRoot,
				LogsBloom:     cloneHexutilBytesList([]hexutil.Bytes{payload.LogsBloom})[0],
				PrevRandao:    payload.PrevRandao,
				BlockNumber:   payload.BlockNumber,
				GasLimit:      payload.GasLimit,
				GasUsed:       payload.GasUsed,
				Timestamp:     payload.Timestamp,
				ExtraData:     append(hexutil.Bytes(nil), payload.ExtraData...),
				BaseFeePerGas: payload.BaseFeePerGas,
				BlockHash:     payload.BlockHash,
				Transactions:  cloneHexutilBytesList(payload.Transactions),
			},
			v2: &ExecutionPayloadV2{
				ExecutionPayloadV1: ExecutionPayloadV1{
					ParentHash:    payload.ParentHash,
					FeeRecipient:  payload.FeeRecipient,
					StateRoot:     payload.StateRoot,
					ReceiptsRoot:  payload.ReceiptsRoot,
					LogsBloom:     cloneHexutilBytesList([]hexutil.Bytes{payload.LogsBloom})[0],
					PrevRandao:    payload.PrevRandao,
					BlockNumber:   payload.BlockNumber,
					GasLimit:      payload.GasLimit,
					GasUsed:       payload.GasUsed,
					Timestamp:     payload.Timestamp,
					ExtraData:     append(hexutil.Bytes(nil), payload.ExtraData...),
					BaseFeePerGas: payload.BaseFeePerGas,
					BlockHash:     payload.BlockHash,
					Transactions:  cloneHexutilBytesList(payload.Transactions),
				},
				Withdrawals: cloneWithdrawals(payload.Withdrawals),
			},
			v3:          payload,
			blobsBundle: &BlobsBundleV1{Commitments: []hexutil.Bytes{}, Proofs: []hexutil.Bytes{}, Blobs: []hexutil.Bytes{}},
		})
	}
	return validForkchoiceResponse(headHash, &payloadID), nil
}

func (e *EngineAPIBlob) buildExecutionPayloadV3Stateful(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV3) *ExecutionPayloadV3 {
	if attrs == nil {
		return nil
	}
	result := e.v1().buildExecutionPayloadStateful(parent, parentHash, &PayloadAttributesV1{
		Timestamp:             attrs.Timestamp,
		PrevRandao:            attrs.PrevRandao,
		SuggestedFeeRecipient: attrs.SuggestedFeeRecipient,
	}, attrs.Withdrawals, attrs.ParentBeaconBlockRoot)
	if result == nil || result.block == nil {
		return nil
	}
	v1 := blockToExecutionPayloadV1(result.block, e.v1().chainConfig())
	header := blockHeader(result.block)
	if v1 == nil || header == nil || header.BlobGasUsed == nil || header.ExcessBlobGas == nil {
		return nil
	}
	payload := &ExecutionPayloadV3{
		ParentHash:    v1.ParentHash,
		FeeRecipient:  v1.FeeRecipient,
		StateRoot:     v1.StateRoot,
		ReceiptsRoot:  v1.ReceiptsRoot,
		LogsBloom:     append(hexutil.Bytes(nil), v1.LogsBloom...),
		PrevRandao:    v1.PrevRandao,
		BlockNumber:   v1.BlockNumber,
		GasLimit:      v1.GasLimit,
		GasUsed:       v1.GasUsed,
		Timestamp:     v1.Timestamp,
		ExtraData:     append(hexutil.Bytes(nil), v1.ExtraData...),
		BaseFeePerGas: v1.BaseFeePerGas,
		BlockHash:     v1.BlockHash,
		Transactions:  cloneHexutilBytesList(v1.Transactions),
		Withdrawals:   cloneWithdrawals(attrs.Withdrawals),
		BlobGasUsed:   hexutilUint64Ptr(*header.BlobGasUsed),
		ExcessBlobGas: hexutilUint64Ptr(*header.ExcessBlobGas),
	}
	payload.BlockHash = ethCompatibleEngineBlockHash(result.block, e.v1().chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   attrs.ParentBeaconBlockRoot,
	})
	return payload
}

// GetBlobsBundleV1 retrieves the blobs bundle for a payload
// engine_getBlobsBundleV1
func (e *EngineAPIBlob) GetBlobsBundleV1(ctx context.Context, payloadID PayloadID) (*BlobsBundleV1, error) {
	if built := e.v1().builtPayload(payloadID); built != nil && built.blobsBundle != nil {
		return built.blobsBundle, nil
	}
	return nil, errPayloadNotFound
}

// =============================================================================
// Fork Choice State
// =============================================================================

// ForkchoiceStateV1 represents the fork choice state
type ForkchoiceStateV1 struct {
	HeadBlockHash      types.Hash `json:"headBlockHash"`
	SafeBlockHash      types.Hash `json:"safeBlockHash"`
	FinalizedBlockHash types.Hash `json:"finalizedBlockHash"`
}

// =============================================================================
// Helper Functions
// =============================================================================

func invalidPayloadResponse(reason string) *PayloadStatusV1 {
	return invalidPayloadResponseWithLatestValid(reason, nil)
}

func invalidPayloadResponseWithLatestValid(reason string, latestValidHash *types.Hash) *PayloadStatusV1 {
	log.Info("Engine payload invalid", "reason", reason)
	return &PayloadStatusV1{
		Status:          PayloadStatusInvalid,
		LatestValidHash: latestValidHash,
		ValidationError: &reason,
	}
}

func invalidForkchoiceResponse(reason string) *ForkchoiceUpdatedResponseV3 {
	log.Info("Engine forkchoice invalid", "reason", reason)
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status:          PayloadStatusInvalid,
			ValidationError: &reason,
		},
	}
}

func invalidForkchoiceResponseWithLatestValid(reason string, latestValidHash *types.Hash) *ForkchoiceUpdatedResponseV3 {
	log.Info("Engine forkchoice invalid", "reason", reason)
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status:          PayloadStatusInvalid,
			LatestValidHash: latestValidHash,
			ValidationError: &reason,
		},
	}
}

// =============================================================================
// Shared Blob Gas & Versioned Hash Validation
// =============================================================================

// validateBlobGasAndHashes validates blob gas fields and versioned hashes
// against the given fork-specific parameters. This logic is shared between
// engine_newPayloadV3 (Cancun) and engine_newPayloadV4 (Pectra).
//
// It performs the following checks in order:
//  1. BlobGasUsed and ExcessBlobGas must be non-nil.
//  2. BlobGasUsed must not exceed maxBlobGas.
//  3. The number of versioned hashes must not exceed maxBlobs.
//  4. BlobGasUsed must equal len(expectedHashes) * gasPerBlob.
//  5. Each versioned hash must have a valid version byte.
//
// Returns nil if all checks pass, or an invalid payload response on the first
// failing check.
func validateBlobGasAndHashes(
	blobGasUsed *hexutil.Uint64,
	excessBlobGas *hexutil.Uint64,
	expectedHashes []types.Hash,
	maxBlobGas uint64,
	maxBlobs uint64,
	gasPerBlob uint64,
) *PayloadStatusV1 {
	// 1. Validate blob gas fields are present
	if blobGasUsed == nil || excessBlobGas == nil {
		return invalidPayloadResponse("missing blob gas fields")
	}

	// 2. Validate blob gas used is within limits
	if uint64(*blobGasUsed) > maxBlobGas {
		return invalidPayloadResponse("blob gas used " + strconv.FormatUint(uint64(*blobGasUsed), 10) + " exceeds maximum allowance " + strconv.FormatUint(maxBlobGas, 10))
	}

	// 3. Validate blob count
	expectedBlobCount := uint64(len(expectedHashes))
	if expectedBlobCount > maxBlobs {
		return invalidPayloadResponse("too many blobs")
	}

	// 4. Verify blob gas matches expected
	expectedBlobGas := expectedBlobCount * gasPerBlob
	if uint64(*blobGasUsed) != expectedBlobGas {
		return invalidPayloadResponse("blob gas mismatch")
	}

	// 5. Validate versioned hashes format
	for i, hash := range expectedHashes {
		if !transaction.IsValidVersionedHash(hash) {
			return invalidPayloadResponse("invalid versioned hash at index " + strconv.Itoa(i))
		}
	}

	return nil
}

// =============================================================================
// Blob Validation Utilities
// =============================================================================

// ValidateBlobTransactions validates blob transactions in a payload
func ValidateBlobTransactions(txs []hexutil.Bytes, expectedHashes []types.Hash) error {
	var actualHashes []types.Hash

	for _, txBytes := range txs {
		if len(txBytes) == 0 {
			continue
		}

		tx, err := transaction.DecodeEthereumTransaction(txBytes)
		if err != nil {
			return err
		}

		if tx.Type() == transaction.BlobTxType {
			actualHashes = append(actualHashes, tx.BlobHashes()...)
		}
	}

	// Verify hash counts match
	if len(actualHashes) != len(expectedHashes) {
		return errBlobHashCountMismatch
	}

	// Verify each hash matches
	for i, hash := range actualHashes {
		if hash != expectedHashes[i] {
			return errBlobHashMismatch
		}
	}

	return nil
}

// CalcExcessBlobGas calculates excess blob gas for next block
func CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed uint64) uint64 {
	return transaction.CalcExcessBlobGas(parentExcessBlobGas, parentBlobGasUsed)
}

// CalcBlobFee calculates the blob fee based on excess blob gas
func CalcBlobFee(excessBlobGas uint64) uint64 {
	return transaction.CalcBlobFee(excessBlobGas).Uint64()
}

// =============================================================================
// Errors
// =============================================================================

var (
	errPayloadNotFound       = &engineError{"payload not found"}
	errBlobHashCountMismatch = &engineError{"blob hash count mismatch"}
	errBlobHashMismatch      = &engineError{"blob hash mismatch"}
	errInvalidPayloadID      = &engineError{"invalid payload ID"}
)

type engineError struct {
	msg string
}

func (e *engineError) Error() string {
	return e.msg
}

type engineInvalidParamsError struct {
	msg string
}

func (e *engineInvalidParamsError) Error() string {
	return e.msg
}

func (e *engineInvalidParamsError) ErrorCode() int {
	return -32602
}

type engineTooLargeRequestError struct {
	msg string
}

func (e *engineTooLargeRequestError) Error() string {
	return e.msg
}

func (e *engineTooLargeRequestError) ErrorCode() int {
	return -38004
}

type engineInvalidForkchoiceStateError struct {
	msg string
}

func (e *engineInvalidForkchoiceStateError) Error() string {
	return e.msg
}

func (e *engineInvalidForkchoiceStateError) ErrorCode() int {
	return -38002
}

type engineInvalidPayloadAttributesError struct {
	msg string
}

func (e *engineInvalidPayloadAttributesError) Error() string {
	return e.msg
}

func (e *engineInvalidPayloadAttributesError) ErrorCode() int {
	return -38003
}

// engineUnsupportedForkError is returned when Engine API is called with a
// payload version that does not match the fork active at the given timestamp.
// Error code -38005 per Engine API specification.
type engineUnsupportedForkError struct {
	msg string
}

func (e *engineUnsupportedForkError) Error() string {
	return e.msg
}

func (e *engineUnsupportedForkError) ErrorCode() int {
	return -38005
}
