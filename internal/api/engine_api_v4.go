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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// =============================================================================
// Engine API v4 Types (Pectra)
// =============================================================================

// ExecutionPayloadV4 extends ExecutionPayloadV3 with Pectra fields
type ExecutionPayloadV4 struct {
	// Base fields from V3
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

	// Cancun fields
	BlobGasUsed   *hexutil.Uint64 `json:"blobGasUsed"`
	ExcessBlobGas *hexutil.Uint64 `json:"excessBlobGas"`

	// Pectra fields (EIP-7685: Execution layer requests)
	DepositRequests       []DepositRequest       `json:"depositRequests,omitempty"`
	WithdrawalRequests    []WithdrawalRequest    `json:"withdrawalRequests,omitempty"`
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

func (e *EngineAPIv4) blobAPI() *EngineAPIBlob {
	return &EngineAPIBlob{api: e.api}
}

// NewPayloadV4 processes a new execution payload with Pectra support
// engine_newPayloadV4
func (e *EngineAPIv4) NewPayloadV4(
	ctx context.Context,
	payload *ExecutionPayloadV4,
	expectedBlobVersionedHashes []types.Hash,
	parentBeaconBlockRoot *types.Hash,
	executionRequests []hexutil.Bytes,
) (*PayloadStatusV1, error) {
	if payload == nil {
		return invalidPayloadResponse("missing execution payload"), nil
	}
	if parentBeaconBlockRoot == nil {
		return invalidPayloadResponse("missing parent beacon block root"), nil
	}
	if payload.Withdrawals == nil {
		return invalidPayloadResponse("missing withdrawals in Pectra+ payload"), nil
	}

	maxBlobGas := uint64(vm.PectraMaxBlobGasPerBlock)
	maxBlobs := uint64(vm.PectraMaxBlobsPerBlock)
	gasPerBlob := uint64(vm.PectraBlobGasPerBlob)
	if cfg := e.blobAPI().v1().chainConfig(); cfg != nil {
		maxBlobGas = cfg.BlobMaxGasPerBlock(uint64(payload.Timestamp))
		maxBlobs = cfg.BlobMaxBlobsPerBlock(uint64(payload.Timestamp))
		gasPerBlob = cfg.BlobGasPerBlob(uint64(payload.Timestamp))
	}
	// Validate blob gas and versioned hashes for the active blob schedule.
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
	blk, err := executionPayloadV4ToBlock(payload)
	if err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadHeader(blockHeader(blk), e.blobAPI().v1().parentHeader(payload.ParentHash), e.blobAPI().v1().chainConfig()); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	if err := validateExecutionPayloadTransactions(payload.Transactions, e.blobAPI().v1().chainConfig(), uint64(payload.BlockNumber), uint64(payload.Timestamp), uint64(payload.BaseFeePerGas), uint64(*payload.ExcessBlobGas), uint64(payload.GasLimit)); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}

	// Validate execution requests (EIP-7685)
	if err := validateExecutionRequests(executionRequests, payload); err != nil {
		return nil, &engineInvalidParamsError{msg: err.Error()}
	}
	requestsHash := executionRequestsHash(executionRequests)
	if err := validateExecutionPayloadBlockRLPSize(blk, payload.Transactions, e.blobAPI().v1().chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   parentBeaconBlockRoot,
		requestsHash:       &requestsHash,
	}); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	blockHash := ethCompatibleEngineBlockHash(blk, e.blobAPI().v1().chainConfig(), enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   parentBeaconBlockRoot,
		requestsHash:       &requestsHash,
	})
	if payload.BlockHash != blockHash {
		return invalidPayloadResponse("block hash mismatch"), nil
	}
	if err := e.blobAPI().v1().validatePayloadExecution(blk, payload.ParentHash, parentBeaconBlockRoot, executionRequests); err != nil {
		return invalidPayloadResponse(err.Error()), nil
	}
	headHash := e.blobAPI().v1().currentHeadHash()
	if headHash == (types.Hash{}) || payload.ParentHash != headHash {
		return syncingPayloadResponse(), nil
	}
	if overlay := e.blobAPI().v1().overlay(); overlay != nil {
		overlay.importBlock(blk, blockHash)
	}
	return validPayloadResponse(blockHash), nil
}

// GetPayloadV4 retrieves a payload with Pectra fields
// engine_getPayloadV4
func (e *EngineAPIv4) GetPayloadV4(ctx context.Context, payloadID PayloadID) (*GetPayloadResponseV4, error) {
	if built := e.blobAPI().v1().builtPayload(payloadID); built != nil && built.v4 != nil {
		return &GetPayloadResponseV4{
			ExecutionPayload:      built.v4,
			BlockValue:            hexutil.Uint64(0),
			BlobsBundle:           built.blobsBundle,
			ShouldOverrideBuilder: false,
			ExecutionRequests:     cloneHexutilBytesList(built.executionRequests),
		}, nil
	}
	return nil, errPayloadNotFound
}

// ForkchoiceUpdatedV4 updates the fork choice with Pectra support
// engine_forkchoiceUpdatedV4
func (e *EngineAPIv4) ForkchoiceUpdatedV4(
	ctx context.Context,
	state *ForkchoiceStateV1,
	attrs *PayloadAttributesV4,
) (*ForkchoiceUpdatedResponseV3, error) {
	if state == nil {
		return invalidForkchoiceResponse("missing forkchoice state"), nil
	}
	head := e.blobAPI().v1().currentHead()
	if head == nil {
		return syncingForkchoiceResponse(), nil
	}
	headHash := e.blobAPI().v1().currentHeadHash()
	if state.HeadBlockHash != headHash {
		return syncingForkchoiceResponse(), nil
	}
	if attrs == nil {
		return validForkchoiceResponse(headHash, nil), nil
	}
	if attrs.Withdrawals == nil {
		return invalidForkchoiceResponse("missing withdrawals"), nil
	}
	if attrs.ParentBeaconBlockRoot == nil {
		return invalidForkchoiceResponse("missing parent beacon block root"), nil
	}
	if attrs.TargetBlobsPerBlock != nil {
		target := uint64(*attrs.TargetBlobsPerBlock)
		maxTarget := uint64(vm.PectraMaxBlobsPerBlock)
		if cfg := e.blobAPI().v1().chainConfig(); cfg != nil {
			maxTarget = cfg.BlobMaxBlobsPerBlock(uint64(attrs.Timestamp))
		}
		if target > maxTarget {
			return invalidForkchoiceResponse("target blobs exceeds maximum"), nil
		}
	}
	if uint64(attrs.Timestamp) <= head.Time() {
		return invalidForkchoiceResponse("payload timestamp must be greater than parent"), nil
	}
	payload := buildExecutionPayloadV4(head, headHash, attrs, e.blobAPI().v1().chainConfig())
	if payload == nil {
		return invalidForkchoiceResponse("failed to build payload"), nil
	}
	withdrawalsRoot := withdrawalsRoot(attrs.Withdrawals)
	var targetBlobBytes [8]byte
	if attrs.TargetBlobsPerBlock != nil {
		binary.BigEndian.PutUint64(targetBlobBytes[:], uint64(*attrs.TargetBlobsPerBlock))
	}
	payloadID := makePayloadIDWithExtras(
		headHash,
		uint64(attrs.Timestamp),
		attrs.SuggestedFeeRecipient,
		withdrawalsRoot[:],
		attrs.ParentBeaconBlockRoot[:],
		targetBlobBytes[:],
	)
	if overlay := e.blobAPI().v1().overlay(); overlay != nil {
		overlay.storeBuiltPayload(payloadID, &engineBuiltPayload{
			v4:                payload,
			v3:                &ExecutionPayloadV3{ParentHash: payload.ParentHash, FeeRecipient: payload.FeeRecipient, StateRoot: payload.StateRoot, ReceiptsRoot: payload.ReceiptsRoot, LogsBloom: append(hexutil.Bytes(nil), payload.LogsBloom...), PrevRandao: payload.PrevRandao, BlockNumber: payload.BlockNumber, GasLimit: payload.GasLimit, GasUsed: payload.GasUsed, Timestamp: payload.Timestamp, ExtraData: append(hexutil.Bytes(nil), payload.ExtraData...), BaseFeePerGas: payload.BaseFeePerGas, BlockHash: payload.BlockHash, Transactions: cloneHexutilBytesList(payload.Transactions), Withdrawals: cloneWithdrawals(payload.Withdrawals), BlobGasUsed: payload.BlobGasUsed, ExcessBlobGas: payload.ExcessBlobGas},
			v2:                &ExecutionPayloadV2{ExecutionPayloadV1: ExecutionPayloadV1{ParentHash: payload.ParentHash, FeeRecipient: payload.FeeRecipient, StateRoot: payload.StateRoot, ReceiptsRoot: payload.ReceiptsRoot, LogsBloom: append(hexutil.Bytes(nil), payload.LogsBloom...), PrevRandao: payload.PrevRandao, BlockNumber: payload.BlockNumber, GasLimit: payload.GasLimit, GasUsed: payload.GasUsed, Timestamp: payload.Timestamp, ExtraData: append(hexutil.Bytes(nil), payload.ExtraData...), BaseFeePerGas: payload.BaseFeePerGas, BlockHash: payload.BlockHash, Transactions: cloneHexutilBytesList(payload.Transactions)}, Withdrawals: cloneWithdrawals(payload.Withdrawals)},
			v1:                &ExecutionPayloadV1{ParentHash: payload.ParentHash, FeeRecipient: payload.FeeRecipient, StateRoot: payload.StateRoot, ReceiptsRoot: payload.ReceiptsRoot, LogsBloom: append(hexutil.Bytes(nil), payload.LogsBloom...), PrevRandao: payload.PrevRandao, BlockNumber: payload.BlockNumber, GasLimit: payload.GasLimit, GasUsed: payload.GasUsed, Timestamp: payload.Timestamp, ExtraData: append(hexutil.Bytes(nil), payload.ExtraData...), BaseFeePerGas: payload.BaseFeePerGas, BlockHash: payload.BlockHash, Transactions: cloneHexutilBytesList(payload.Transactions)},
			blobsBundle:       &BlobsBundleV1{Commitments: []hexutil.Bytes{}, Proofs: []hexutil.Bytes{}, Blobs: []hexutil.Bytes{}},
			executionRequests: []hexutil.Bytes{},
		})
	}
	return validForkchoiceResponse(headHash, &payloadID), nil
}

// GetBlobsV1 retrieves blob sidecars by versioned hashes.
// engine_getBlobsV1 (new in Pectra)
//
// Per Engine API spec: returns an array of BlobAndProofV1 entries
// matching the requested versioned hashes. Entries are null if the
// blob is not available locally.
func (e *EngineAPIv4) GetBlobsV1(ctx context.Context, versionedHashes []types.Hash) ([]*BlobAndProofV1, error) {
	if len(versionedHashes) == 0 {
		return []*BlobAndProofV1{}, nil
	}

	// Search through built payloads' blob bundles for matching hashes.
	overlay := e.blobAPI().v1().overlay()
	results := make([]*BlobAndProofV1, len(versionedHashes))

	if overlay == nil {
		return results, nil // all null entries
	}

	// Build an index of versioned hash → (blob, commitment, proof) from all
	// built payloads. This is a scan over recently built payloads, which is
	// bounded by maxOverlayPayloads.
	type blobEntry struct {
		blob       hexutil.Bytes
		commitment hexutil.Bytes
		proof      hexutil.Bytes
	}
	blobIndex := make(map[types.Hash]*blobEntry)

	overlay.mu.RLock()
	for _, built := range overlay.builtPayloads {
		if built == nil || built.blobsBundle == nil {
			continue
		}
		bundle := built.blobsBundle
		for i := 0; i < len(bundle.Commitments) && i < len(bundle.Blobs) && i < len(bundle.Proofs); i++ {
			// Compute versioned hash from commitment: SHA256(commitment)[1:] with version prefix
			if len(bundle.Commitments[i]) >= 48 {
				vHash := computeVersionedHash(bundle.Commitments[i])
				blobIndex[vHash] = &blobEntry{
					blob:       bundle.Blobs[i],
					commitment: bundle.Commitments[i],
					proof:      bundle.Proofs[i],
				}
			}
		}
	}
	overlay.mu.RUnlock()

	// Match requested hashes
	for i, h := range versionedHashes {
		if entry, ok := blobIndex[h]; ok {
			results[i] = &BlobAndProofV1{
				Blob:       entry.blob,
				Commitment: entry.commitment,
				Proof:      entry.proof,
			}
		}
		// else: results[i] remains nil (blob not available)
	}

	return results, nil
}

// computeVersionedHash computes a versioned hash from a KZG commitment.
// versionedHash = 0x01 || SHA256(commitment)[1:]
func computeVersionedHash(commitment []byte) types.Hash {
	h := sha256.Sum256(commitment)
	h[0] = 0x01 // KZG version byte
	return types.Hash(h)
}

// GetPayloadBodiesByHashV1 returns execution payload bodies for the given block hashes.
// engine_getPayloadBodiesByHashV1
func (e *EngineAPIv4) GetPayloadBodiesByHashV1(ctx context.Context, hashes []types.Hash) ([]*ExecutionPayloadBodyV1, error) {
	results := make([]*ExecutionPayloadBodyV1, len(hashes))

	overlay := e.blobAPI().v1().overlay()
	if overlay == nil {
		return results, nil
	}

	overlay.mu.RLock()
	defer overlay.mu.RUnlock()

	for i, hash := range hashes {
		blk, ok := overlay.blocksByHash[hash]
		if !ok || blk == nil {
			continue
		}
		body := payloadBodyFromBlock(blk)
		results[i] = body
	}
	return results, nil
}

// GetPayloadBodiesByRangeV1 returns execution payload bodies for a range of blocks.
// engine_getPayloadBodiesByRangeV1
func (e *EngineAPIv4) GetPayloadBodiesByRangeV1(ctx context.Context, start hexutil.Uint64, count hexutil.Uint64) ([]*ExecutionPayloadBodyV1, error) {
	if count == 0 || count > 1024 {
		if count > 1024 {
			count = 1024
		}
		if count == 0 {
			return []*ExecutionPayloadBodyV1{}, nil
		}
	}

	results := make([]*ExecutionPayloadBodyV1, count)

	overlay := e.blobAPI().v1().overlay()
	if overlay == nil {
		return results, nil
	}

	overlay.mu.RLock()
	defer overlay.mu.RUnlock()

	for i := uint64(0); i < uint64(count); i++ {
		num := uint64(start) + i
		blk, ok := overlay.blocksByNum[num]
		if !ok || blk == nil {
			continue
		}
		results[i] = payloadBodyFromBlock(blk)
	}
	return results, nil
}

// ExecutionPayloadBodyV1 is the response type for getPayloadBodies methods.
type ExecutionPayloadBodyV1 struct {
	Transactions []hexutil.Bytes `json:"transactions"`
	Withdrawals  []*Withdrawal   `json:"withdrawals"`
}

// payloadBodyFromBlock extracts the payload body from a block.
func payloadBodyFromBlock(blk block.IBlock) *ExecutionPayloadBodyV1 {
	txs := blk.Transactions()
	encodedTxs := make([]hexutil.Bytes, len(txs))
	for i, tx := range txs {
		if data, err := tx.Marshal(); err == nil {
			encodedTxs[i] = data
		}
	}
	return &ExecutionPayloadBodyV1{
		Transactions: encodedTxs,
		Withdrawals:  []*Withdrawal{}, // N42 uses deposit contracts instead of protocol withdrawals
	}
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
	// EIP-7685: request types must appear in ascending order
	lastType := byte(0)
	firstSeen := true

	for _, req := range requests {
		if len(req) == 0 {
			return errInvalidRequestEncoding
		}

		// Enforce ascending request type ordering per EIP-7685
		reqType := req[0]
		if !firstSeen && reqType <= lastType {
			return fmt.Errorf("execution requests not in ascending type order: type %d after %d", reqType, lastType)
		}
		lastType = reqType
		firstSeen = false

		var (
			payloadLen int
			reqSize    int
		)
		switch reqType {
		case DepositRequestType:
			reqSize = vm.DepositRequestSize
			payloadLen = len(payload.DepositRequests)
		case WithdrawalRequestType:
			reqSize = vm.WithdrawalRequestSize
			payloadLen = len(payload.WithdrawalRequests)
		case ConsolidationRequestType:
			reqSize = vm.ConsolidationRequestSize
			payloadLen = len(payload.ConsolidationRequests)
		default:
			return errUnknownRequestType
		}

		// When payload-local request arrays are absent, the executionRequests
		// parameter is an opaque byte blob per EIP-7685 and must not be
		// re-parsed into fixed-size records.
		if payloadLen == 0 {
			if len(req) == 1 {
				return errInvalidRequestEncoding
			}
			continue
		}

		if (len(req)-1)%reqSize != 0 {
			return errInvalidRequestEncoding
		}
		count := (len(req) - 1) / reqSize
		switch reqType {
		case DepositRequestType:
			depositCount += count
		case WithdrawalRequestType:
			withdrawalCount += count
		case ConsolidationRequestType:
			consolidationCount += count
		}
		// Current Prague/Osaka fixtures provide the canonical request lists only
		// via the fourth engine_newPayloadV4 parameter. The payload-local request
		// arrays are legacy and may be omitted entirely.
		if payloadLen > 0 && payloadLen != count {
			return errRequestCountMismatch
		}
	}

	// Only enforce count parity when legacy payload-local request arrays are
	// explicitly populated. Current fixtures omit them and rely solely on the
	// flattened executionRequests parameter.
	if len(payload.DepositRequests) > 0 && depositCount != len(payload.DepositRequests) {
		return errRequestCountMismatch
	}
	if len(payload.WithdrawalRequests) > 0 && withdrawalCount != len(payload.WithdrawalRequests) {
		return errRequestCountMismatch
	}
	if len(payload.ConsolidationRequests) > 0 && consolidationCount != len(payload.ConsolidationRequests) {
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
	target := uint64(vm.PectraTargetBlobsPerBlock)
	max := uint64(vm.PectraMaxBlobsPerBlock)
	if cfg := e.blobAPI().v1().chainConfig(); cfg != nil {
		head := blockHeader(e.blobAPI().v1().currentHead())
		timestamp := uint64(0)
		if head != nil {
			timestamp = head.Time
		}
		target = cfg.BlobTargetBlobsPerBlock(timestamp)
		max = cfg.BlobMaxBlobsPerBlock(timestamp)
	}
	return &BlobScheduleResponse{
		TargetBlobsPerBlock: target,
		MaxBlobsPerBlock:    max,
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
	SupportedForks   []string `json:"supportedForks"`   // List of supported forks
	SupportedMethods []string `json:"supportedMethods"` // List of supported Engine API methods
	ProtocolVersion  string   `json:"protocolVersion"`  // Protocol version
	CanCancelFork    bool     `json:"canCancelFork"`    // Whether fork can be cancelled
}

// GetClientCapabilitiesV1 returns the client's capabilities
// engine_getClientCapabilitiesV1
func (e *EngineAPIv4) GetClientCapabilitiesV1(ctx context.Context) (*ClientCapabilities, error) {
	return &ClientCapabilities{
		SupportedForks:   supportedEngineForks(),
		SupportedMethods: supportedEngineMethods(),
		ProtocolVersion:  "4.0.0",
		CanCancelFork:    true,
	}, nil
}

// =============================================================================
// Fork Candidate Management (CFI mechanism)
// =============================================================================

// ForkCandidate represents a candidate fork for activation
type ForkCandidate struct {
	Name           string     `json:"name"`           // Fork name (e.g., "pectra")
	ActivationTime *big.Int   `json:"activationTime"` // Activation timestamp
	ConfigHash     types.Hash `json:"configHash"`     // Hash of fork configuration
	Status         string     `json:"status"`         // "candidate", "scheduled", "active", "cancelled"
	Cancellable    bool       `json:"cancellable"`    // Whether fork can be cancelled
}

// ForkCandidateStatus represents the status of fork candidates
type ForkCandidateStatus struct {
	Candidates    []ForkCandidate `json:"candidates"`
	ActiveFork    string          `json:"activeFork"`
	NextScheduled *ForkCandidate  `json:"nextScheduled,omitempty"`
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
	errBlobNotFound           = &engineError{"blob not found"}
	errUnknownRequestType     = &engineError{"unknown request type"}
	errInvalidRequestEncoding = &engineError{"invalid request encoding"}
	errRequestCountMismatch   = &engineError{"request count mismatch"}
)
