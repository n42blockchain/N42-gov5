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

// Engine API v4 for Pectra
// Reference: https://github.com/ethereum/execution-apis/blob/main/src/engine/prague.md

package api

import (
	"context"
	"math/big"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// =============================================================================
// Engine API v4 Types (Pectra)
// =============================================================================

// ExecutionPayloadV4 extends ExecutionPayloadV3 with Pectra fields
type ExecutionPayloadV4 struct {
	// Base fields from V3
	ParentHash    types.Hash     `json:"parentHash"`
	FeeRecipient  types.Address  `json:"feeRecipient"`
	StateRoot     types.Hash     `json:"stateRoot"`
	ReceiptsRoot  types.Hash     `json:"receiptsRoot"`
	LogsBloom     hexutil.Bytes  `json:"logsBloom"`
	PrevRandao    types.Hash     `json:"prevRandao"`
	BlockNumber   hexutil.Uint64 `json:"blockNumber"`
	GasLimit      hexutil.Uint64 `json:"gasLimit"`
	GasUsed       hexutil.Uint64 `json:"gasUsed"`
	Timestamp     hexutil.Uint64 `json:"timestamp"`
	ExtraData     hexutil.Bytes  `json:"extraData"`
	BaseFeePerGas hexutil.Uint64 `json:"baseFeePerGas"`
	BlockHash     types.Hash     `json:"blockHash"`
	Transactions  []hexutil.Bytes `json:"transactions"`
	Withdrawals   []*Withdrawal   `json:"withdrawals"`
	
	// Cancun fields
	BlobGasUsed   *hexutil.Uint64 `json:"blobGasUsed"`
	ExcessBlobGas *hexutil.Uint64 `json:"excessBlobGas"`
	
	// Pectra fields (EIP-7685: Execution layer requests)
	DepositRequests    []DepositRequest    `json:"depositRequests,omitempty"`
	WithdrawalRequests []WithdrawalRequest `json:"withdrawalRequests,omitempty"`
	ConsolidationRequests []ConsolidationRequest `json:"consolidationRequests,omitempty"`
}

// DepositRequest represents a validator deposit request (EIP-6110)
type DepositRequest struct {
	Pubkey                hexutil.Bytes  `json:"pubkey"`                // BLS public key
	WithdrawalCredentials hexutil.Bytes  `json:"withdrawalCredentials"` // Withdrawal credentials
	Amount                hexutil.Uint64 `json:"amount"`                // Amount in Gwei
	Signature             hexutil.Bytes  `json:"signature"`             // BLS signature
	Index                 hexutil.Uint64 `json:"index"`                 // Deposit index
}

// WithdrawalRequest represents a validator withdrawal request (EIP-7002)
type WithdrawalRequest struct {
	SourceAddress   types.Address  `json:"sourceAddress"`   // Source address
	ValidatorPubkey hexutil.Bytes  `json:"validatorPubkey"` // Validator BLS public key
	Amount          hexutil.Uint64 `json:"amount"`          // Amount in Gwei
}

// ConsolidationRequest represents a validator consolidation request (EIP-7251)
type ConsolidationRequest struct {
	SourceAddress types.Address `json:"sourceAddress"` // Source address
	SourcePubkey  hexutil.Bytes `json:"sourcePubkey"`  // Source validator BLS public key
	TargetPubkey  hexutil.Bytes `json:"targetPubkey"`  // Target validator BLS public key
}

// PayloadAttributesV4 extends PayloadAttributesV3 with Pectra fields
type PayloadAttributesV4 struct {
	Timestamp             hexutil.Uint64 `json:"timestamp"`
	PrevRandao            types.Hash     `json:"prevRandao"`
	SuggestedFeeRecipient types.Address  `json:"suggestedFeeRecipient"`
	Withdrawals           []*Withdrawal  `json:"withdrawals"`
	ParentBeaconBlockRoot *types.Hash    `json:"parentBeaconBlockRoot"`
	
	// Pectra additions
	TargetBlobsPerBlock *hexutil.Uint64 `json:"targetBlobsPerBlock,omitempty"` // EIP-7840
}

// GetPayloadResponseV4 is the response for engine_getPayloadV4
type GetPayloadResponseV4 struct {
	ExecutionPayload      *ExecutionPayloadV4 `json:"executionPayload"`
	BlockValue            hexutil.Uint64      `json:"blockValue"`
	BlobsBundle           *BlobsBundleV1      `json:"blobsBundle"`
	ShouldOverrideBuilder bool                `json:"shouldOverrideBuilder"`
	ExecutionRequests     []hexutil.Bytes     `json:"executionRequests"` // EIP-7685
}

// =============================================================================
// Engine API v4 Methods
// =============================================================================

// EngineAPIv4 provides Engine API v4 methods for Pectra
type EngineAPIv4 struct {
	api *BlockChainAPI
}

// NewEngineAPIv4 creates a new Engine API v4 instance
func NewEngineAPIv4(api *BlockChainAPI) *EngineAPIv4 {
	return &EngineAPIv4{api: api}
}

// NewPayloadV4 processes a new execution payload with Pectra support
// engine_newPayloadV4
func (e *EngineAPIv4) NewPayloadV4(
	ctx context.Context,
	payload *ExecutionPayloadV4,
	expectedBlobVersionedHashes []types.Hash,
	parentBeaconBlockRoot *types.Hash,
	executionRequests []hexutil.Bytes,
) (*NewPayloadResponseV3, error) {
	// Validate Pectra-specific fields
	if payload.BlobGasUsed == nil || payload.ExcessBlobGas == nil {
		return invalidPayloadResponse("missing blob gas fields"), nil
	}

	// Validate blob gas with Pectra limits (EIP-7691)
	blobGasUsed := uint64(*payload.BlobGasUsed)
	if err := vm.VerifyBlobGasEIP7691(blobGasUsed, true); err != nil {
		return invalidPayloadResponse("blob gas exceeds Pectra limit"), nil
	}

	// Validate blob count
	expectedBlobCount := len(expectedBlobVersionedHashes)
	if uint64(expectedBlobCount) > vm.PectraMaxBlobsPerBlock {
		return invalidPayloadResponse("too many blobs for Pectra"), nil
	}

	// Verify blob gas matches expected
	expectedBlobGas := uint64(expectedBlobCount) * vm.PectraBlobGasPerBlob
	if blobGasUsed != expectedBlobGas {
		return invalidPayloadResponse("blob gas mismatch"), nil
	}

	// Validate versioned hashes
	for _, hash := range expectedBlobVersionedHashes {
		if !transaction.IsValidVersionedHash(hash) {
			return invalidPayloadResponse("invalid versioned hash"), nil
		}
	}

	// Validate execution requests (EIP-7685)
	if err := validateExecutionRequests(executionRequests, payload); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}

	// TODO: Implement full payload processing
	return &NewPayloadResponseV3{
		Status: PayloadStatusV1{
			Status:          PayloadStatusValid,
			LatestValidHash: &payload.BlockHash,
		},
	}, nil
}

// GetPayloadV4 retrieves a payload with Pectra fields
// engine_getPayloadV4
func (e *EngineAPIv4) GetPayloadV4(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV4, error) {
	// TODO: Implement payload retrieval with Pectra fields
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV4 updates the fork choice with Pectra support
// engine_forkchoiceUpdatedV4
func (e *EngineAPIv4) ForkchoiceUpdatedV4(
	ctx context.Context,
	state *ForkchoiceStateV1,
	attrs *PayloadAttributesV4,
) (*ForkchoiceUpdatedResponseV3, error) {
	// Validate attributes if present
	if attrs != nil {
		// Parent beacon block root is required for Pectra
		if attrs.ParentBeaconBlockRoot == nil {
			return invalidForkchoiceResponse("missing parent beacon block root"), nil
		}

		// Validate target blobs if specified (EIP-7840)
		if attrs.TargetBlobsPerBlock != nil {
			target := uint64(*attrs.TargetBlobsPerBlock)
			if target > vm.PectraMaxBlobsPerBlock {
				return invalidForkchoiceResponse("target blobs exceeds maximum"), nil
			}
		}
	}

	// TODO: Implement fork choice update
	return &ForkchoiceUpdatedResponseV3{
		PayloadStatus: PayloadStatusV1{
			Status: PayloadStatusValid,
		},
	}, nil
}

// GetBlobsV1 retrieves blob sidecars by versioned hashes
// engine_getBlobsV1 (new in Pectra)
func (e *EngineAPIv4) GetBlobsV1(ctx context.Context, versionedHashes []types.Hash) (*BlobAndProofV1, error) {
	// TODO: Implement blob retrieval by hash
	return nil, errBlobNotFound
}

// =============================================================================
// Pectra-specific Types
// =============================================================================

// BlobAndProofV1 contains blob data with its proof
type BlobAndProofV1 struct {
	Blob       hexutil.Bytes `json:"blob"`       // Blob data
	Commitment hexutil.Bytes `json:"commitment"` // KZG commitment
	Proof      hexutil.Bytes `json:"proof"`      // KZG proof
}

// =============================================================================
// Execution Requests Validation (EIP-7685)
// =============================================================================

// Request type identifiers (EIP-7685)
const (
	DepositRequestType       = 0x00
	WithdrawalRequestType    = 0x01
	ConsolidationRequestType = 0x02
)

// validateExecutionRequests validates the execution requests in a payload
func validateExecutionRequests(requests []hexutil.Bytes, payload *ExecutionPayloadV4) error {
	// Count requests by type
	var depositCount, withdrawalCount, consolidationCount int

	for _, req := range requests {
		if len(req) == 0 {
			continue
		}

		switch req[0] {
		case DepositRequestType:
			depositCount++
		case WithdrawalRequestType:
			withdrawalCount++
		case ConsolidationRequestType:
			consolidationCount++
		default:
			return errUnknownRequestType
		}
	}

	// Verify counts match payload
	if depositCount != len(payload.DepositRequests) {
		return errRequestCountMismatch
	}
	if withdrawalCount != len(payload.WithdrawalRequests) {
		return errRequestCountMismatch
	}
	if consolidationCount != len(payload.ConsolidationRequests) {
		return errRequestCountMismatch
	}

	return nil
}

// =============================================================================
// Blob Schedule for Pectra (EIP-7840)
// =============================================================================

// GetBlobScheduleV1 retrieves the current blob schedule
// engine_getBlobScheduleV1 (new in Pectra)
func (e *EngineAPIv4) GetBlobScheduleV1(ctx context.Context) (*BlobScheduleResponse, error) {
	return &BlobScheduleResponse{
		TargetBlobsPerBlock: vm.PectraTargetBlobsPerBlock,
		MaxBlobsPerBlock:    vm.PectraMaxBlobsPerBlock,
		BlobGasPerBlob:      vm.PectraBlobGasPerBlob,
	}, nil
}

// BlobScheduleResponse contains the blob schedule parameters
type BlobScheduleResponse struct {
	TargetBlobsPerBlock uint64 `json:"targetBlobsPerBlock"`
	MaxBlobsPerBlock    uint64 `json:"maxBlobsPerBlock"`
	BlobGasPerBlob      uint64 `json:"blobGasPerBlob"`
}

// =============================================================================
// Client Capabilities (CFI-like mechanism)
// =============================================================================

// ClientCapabilities represents client capabilities for fork management
type ClientCapabilities struct {
	SupportedForks      []string `json:"supportedForks"`      // List of supported forks
	SupportedMethods    []string `json:"supportedMethods"`    // List of supported Engine API methods
	ProtocolVersion     string   `json:"protocolVersion"`     // Protocol version
	CanCancelFork       bool     `json:"canCancelFork"`       // Whether fork can be cancelled
}

// GetClientCapabilitiesV1 returns the client's capabilities
// engine_getClientCapabilitiesV1
func (e *EngineAPIv4) GetClientCapabilitiesV1(ctx context.Context) (*ClientCapabilities, error) {
	return &ClientCapabilities{
		SupportedForks: []string{
			"cancun",
			"prague",
			"pectra",
		},
		SupportedMethods: []string{
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
			"engine_getBlobsV1",
			"engine_getBlobScheduleV1",
			"engine_getClientCapabilitiesV1",
		},
		ProtocolVersion: "4.0.0",
		CanCancelFork:   true,
	}, nil
}

// =============================================================================
// Fork Candidate Management (CFI mechanism)
// =============================================================================

// ForkCandidate represents a candidate fork for activation
type ForkCandidate struct {
	Name            string         `json:"name"`            // Fork name (e.g., "pectra")
	ActivationTime  *big.Int       `json:"activationTime"`  // Activation timestamp
	ConfigHash      types.Hash     `json:"configHash"`      // Hash of fork configuration
	Status          string         `json:"status"`          // "candidate", "scheduled", "active", "cancelled"
	Cancellable     bool           `json:"cancellable"`     // Whether fork can be cancelled
}

// ForkCandidateStatus represents the status of fork candidates
type ForkCandidateStatus struct {
	Candidates      []ForkCandidate `json:"candidates"`
	ActiveFork      string          `json:"activeFork"`
	NextScheduled   *ForkCandidate  `json:"nextScheduled,omitempty"`
}

// GetForkCandidatesV1 returns the current fork candidates
// engine_getForkCandidatesV1
func (e *EngineAPIv4) GetForkCandidatesV1(ctx context.Context) (*ForkCandidateStatus, error) {
	// Return current fork status
	return &ForkCandidateStatus{
		Candidates: []ForkCandidate{
			{
				Name:        "pectra",
				Status:      "candidate",
				Cancellable: true,
			},
		},
		ActiveFork: "cancun",
	}, nil
}

// =============================================================================
// Errors
// =============================================================================

var (
	errBlobNotFound        = &engineErrorV4{"blob not found"}
	errUnknownRequestType  = &engineErrorV4{"unknown request type"}
	errRequestCountMismatch = &engineErrorV4{"request count mismatch"}
)

type engineErrorV4 struct {
	msg string
}

func (e *engineErrorV4) Error() string {
	return e.msg
}

