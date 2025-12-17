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

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Engine API Types for EIP-4844 (Cancun)
// =============================================================================

// ExecutionPayloadV3 extends ExecutionPayloadV2 with blob fields
type ExecutionPayloadV3 struct {
	ParentHash    types.Hash    `json:"parentHash"`
	FeeRecipient  types.Address `json:"feeRecipient"`
	StateRoot     types.Hash    `json:"stateRoot"`
	ReceiptsRoot  types.Hash    `json:"receiptsRoot"`
	LogsBloom     hexutil.Bytes `json:"logsBloom"`
	PrevRandao    types.Hash    `json:"prevRandao"`
	BlockNumber   hexutil.Uint64 `json:"blockNumber"`
	GasLimit      hexutil.Uint64 `json:"gasLimit"`
	GasUsed       hexutil.Uint64 `json:"gasUsed"`
	Timestamp     hexutil.Uint64 `json:"timestamp"`
	ExtraData     hexutil.Bytes  `json:"extraData"`
	BaseFeePerGas hexutil.Uint64 `json:"baseFeePerGas"`
	BlockHash     types.Hash     `json:"blockHash"`
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
	Timestamp             hexutil.Uint64  `json:"timestamp"`
	PrevRandao            types.Hash      `json:"prevRandao"`
	SuggestedFeeRecipient types.Address   `json:"suggestedFeeRecipient"`
	Withdrawals           []*Withdrawal   `json:"withdrawals"`
	ParentBeaconBlockRoot *types.Hash     `json:"parentBeaconBlockRoot"`
}

// GetPayloadResponseV3 is the response for engine_getPayloadV3
type GetPayloadResponseV3 struct {
	ExecutionPayload *ExecutionPayloadV3 `json:"executionPayload"`
	BlockValue       hexutil.Uint64      `json:"blockValue"`
	BlobsBundle      *BlobsBundleV1      `json:"blobsBundle"`
	ShouldOverrideBuilder bool            `json:"shouldOverrideBuilder"`
}

// ForkchoiceUpdatedResponseV3 includes blob-related validation status
type ForkchoiceUpdatedResponseV3 struct {
	PayloadStatus PayloadStatusV1 `json:"payloadStatus"`
	PayloadID     *PayloadID      `json:"payloadId"`
}

// NewPayloadResponseV3 includes validation status
type NewPayloadResponseV3 struct {
	Status          PayloadStatusV1  `json:"status"`
	LatestValidHash *types.Hash      `json:"latestValidHash"`
	ValidationError *string          `json:"validationError"`
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
}

// NewEngineAPIBlob creates a new Engine API blob instance
func NewEngineAPIBlob(api *BlockChainAPI) *EngineAPIBlob {
	return &EngineAPIBlob{api: api}
}

// NewPayloadV3 processes a new execution payload with blob support
// engine_newPayloadV3
func (e *EngineAPIBlob) NewPayloadV3(ctx context.Context, payload *ExecutionPayloadV3, expectedBlobVersionedHashes []types.Hash, parentBeaconBlockRoot *types.Hash) (*NewPayloadResponseV3, error) {
	// Validate blob gas fields are present for Cancun
	if payload.BlobGasUsed == nil || payload.ExcessBlobGas == nil {
		return invalidPayloadResponse("missing blob gas fields"), nil
	}
	
	// Validate blob gas used is within limits
	if uint64(*payload.BlobGasUsed) > transaction.MaxBlobGasPerBlock {
		return invalidPayloadResponse("blob gas used exceeds maximum"), nil
	}
	
	// Count expected blobs from versioned hashes
	expectedBlobCount := len(expectedBlobVersionedHashes)
	expectedBlobGas := uint64(expectedBlobCount) * transaction.BlobTxBlobGasPerBlob
	
	if uint64(*payload.BlobGasUsed) != expectedBlobGas {
		return invalidPayloadResponse("blob gas mismatch"), nil
	}
	
	// Validate versioned hashes format
	for i, hash := range expectedBlobVersionedHashes {
		if !transaction.IsValidVersionedHash(hash) {
			return invalidPayloadResponse("invalid versioned hash at index " + string(rune(i))), nil
		}
	}
	
	// TODO: Implement actual payload processing
	// This would:
	// 1. Decode and validate transactions
	// 2. Verify state root
	// 3. Execute transactions
	// 4. Validate receipts root
	
	return &NewPayloadResponseV3{
		Status: PayloadStatusV1{
			Status:          PayloadStatusValid,
			LatestValidHash: &payload.BlockHash,
		},
	}, nil
}

// GetPayloadV3 retrieves a payload with blob bundle
// engine_getPayloadV3
func (e *EngineAPIBlob) GetPayloadV3(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV3, error) {
	// TODO: Implement payload retrieval
	// This would:
	// 1. Look up payload by ID
	// 2. Retrieve blob sidecar
	// 3. Build response with execution payload and blobs bundle
	
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV3 updates the fork choice with blob support
// engine_forkchoiceUpdatedV3
func (e *EngineAPIBlob) ForkchoiceUpdatedV3(ctx context.Context, state *ForkchoiceStateV1, attrs *PayloadAttributesV3) (*ForkchoiceUpdatedResponseV3, error) {
	// Validate attributes if present
	if attrs != nil {
		// Parent beacon block root is required for Cancun
		if attrs.ParentBeaconBlockRoot == nil {
			return invalidForkchoiceResponse("missing parent beacon block root"), nil
		}
	}
	
	// TODO: Implement fork choice update
	// This would:
	// 1. Update fork choice
	// 2. Start payload building if attributes present
	
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status: PayloadStatusValid,
		},
	}, nil
}

// GetBlobsBundleV1 retrieves the blobs bundle for a payload
// engine_getBlobsBundleV1
func (e *EngineAPIBlob) GetBlobsBundleV1(ctx context.Context, payloadID PayloadID) (*BlobsBundleV1, error) {
	// TODO: Implement blobs bundle retrieval
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

func invalidPayloadResponse(reason string) *NewPayloadResponseV3 {
	return &NewPayloadResponseV3{
		Status: PayloadStatusV1{
			Status:          PayloadStatusInvalid,
			ValidationError: &reason,
		},
	}
}

func invalidForkchoiceResponse(reason string) *ForkchoiceUpdatedResponseV3 {
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status:          PayloadStatusInvalid,
			ValidationError: &reason,
		},
	}
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
		
		// Check if this is a blob transaction (type 0x03)
		if txBytes[0] == transaction.BlobTxType {
			// TODO: Decode transaction and extract blob hashes
			// For now, skip validation
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
	errPayloadNotFound      = &engineError{"payload not found"}
	errBlobHashCountMismatch = &engineError{"blob hash count mismatch"}
	errBlobHashMismatch     = &engineError{"blob hash mismatch"}
	errInvalidPayloadID     = &engineError{"invalid payload ID"}
)

type engineError struct {
	msg string
}

func (e *engineError) Error() string {
	return e.msg
}

