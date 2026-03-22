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

package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// AI Inference precompile address (0x0301).
var AIInferenceAddress = types.HexToAddress("0x0000000000000000000000000000000000000301")

// PrecompiledContractsAIInference contains the AI inference precompile.
// N42-specific, independent of standard fork precompile sets.
var PrecompiledContractsAIInference = map[types.Address]PrecompiledContract{
	AIInferenceAddress: &aiInference{},
}

// PrecompiledAddressesAIInference is populated in init().
var PrecompiledAddressesAIInference []types.Address

func init() {
	PrecompiledAddressesAIInference = collectAddresses(PrecompiledContractsAIInference)
}

// InferenceBackend abstracts the inference service for the precompile.
// Implementations live in internal/distributed/compute/inference.
type InferenceBackend interface {
	// SubmitRequest submits an inference request for the given model,
	// with input stored at inputCAS. Returns a unique request ID.
	SubmitRequest(modelHash types.Hash, inputCAS types.Hash, tier uint8, caller types.Address) (types.Hash, error)

	// GetResult returns the status and output CAS hash for a request.
	GetResult(requestID types.Hash) (status uint8, outputCAS types.Hash, err error)

	// GetModel returns metadata for a registered model.
	GetModel(modelHash types.Hash) (name, format, capabilities string, err error)

	// ListModels returns model hashes matching the given capability.
	// An empty capability string returns all models.
	ListModels(capability string) ([]types.Hash, error)
}

// Global inference backend, protected by RWMutex for thread-safe access.
var (
	globalInferenceBackendMu sync.RWMutex
	globalInferenceBackend   InferenceBackend
)

// SetInferenceBackend sets the global inference backend for the precompile.
// Called during node startup when the AI inference subsystem is initialized.
func SetInferenceBackend(backend InferenceBackend) {
	globalInferenceBackendMu.Lock()
	defer globalInferenceBackendMu.Unlock()
	globalInferenceBackend = backend
}

// getInferenceBackend returns the current global inference backend.
func getInferenceBackend() InferenceBackend {
	globalInferenceBackendMu.RLock()
	defer globalInferenceBackendMu.RUnlock()
	return globalInferenceBackend
}

// GetAIInference returns an AI inference precompile instance.
func GetAIInference() PrecompiledContract {
	return &aiInference{}
}

// AI Inference function selectors (first byte of input).
const (
	aiRequestInference byte = 0x00 // requestInference(modelHash, inputCAS, tier) -> requestID
	aiGetResult        byte = 0x01 // getResult(requestID) -> (status, outputCAS)
	aiGetModel         byte = 0x02 // getModel(modelHash) -> (name, format, capabilities) ABI-encoded
	aiListModels       byte = 0x03 // listModels(capability) -> modelHashes[]
)

// Gas costs for AI inference precompile operations.
const (
	// AIInferenceBaseGas is the base cost for submitting an inference request.
	AIInferenceBaseGas uint64 = 10000

	// AIInferencePerByteGas is the per-byte cost added to inference request gas.
	AIInferencePerByteGas uint64 = 100

	// AIInferenceGetResultGas is the cost for querying a request result.
	AIInferenceGetResultGas uint64 = 2600

	// AIInferenceGetModelGas is the cost for querying model metadata.
	AIInferenceGetModelGas uint64 = 2600

	// AIInferenceListModelsGas is the cost for listing models by capability.
	AIInferenceListModelsGas uint64 = 5000
)

// Error sentinels for AI inference precompile.
var (
	errAIInferenceInvalidInput = errors.New("ai inference: invalid input")
	errAIInferenceNoBackend    = errors.New("ai inference: backend not available")
	errAIInferenceUnknownOp    = errors.New("ai inference: unknown selector")
)

// aiInference implements the AI inference precompile at address 0x0301.
//
// This is an N42-specific precompile that provides EVM-level access to the
// distributed AI inference engine. Contracts can submit inference requests,
// query results, and discover available models.
//
// Function selectors:
//
//	0x00: requestInference(modelHash [32]byte, inputCAS [32]byte, tier uint8) -> requestID [32]byte
//	0x01: getResult(requestID [32]byte) -> (status uint8, outputCAS [32]byte)
//	0x02: getModel(modelHash [32]byte) -> (name string, format string, capabilities string)
//	0x03: listModels(capability string) -> modelHashes [][32]byte
type aiInference struct{}

// RequiredGas returns the gas required to execute the AI inference precompile.
func (c *aiInference) RequiredGas(input []byte) uint64 {
	if len(input) < 1 {
		return AIInferenceGetResultGas // minimum gas for invalid input
	}

	switch input[0] {
	case aiRequestInference:
		// Base gas + per-byte cost for the payload after the selector
		dataLen := uint64(len(input) - 1)
		return AIInferenceBaseGas + AIInferencePerByteGas*dataLen

	case aiGetResult:
		return AIInferenceGetResultGas

	case aiGetModel:
		return AIInferenceGetModelGas

	case aiListModels:
		return AIInferenceListModelsGas

	default:
		return AIInferenceGetResultGas
	}
}

// Run executes the AI inference precompile.
func (c *aiInference) Run(input []byte) ([]byte, error) {
	if len(input) < 1 {
		return nil, errAIInferenceInvalidInput
	}

	backend := getInferenceBackend()

	switch input[0] {
	case aiRequestInference:
		return c.runRequestInference(backend, input[1:])
	case aiGetResult:
		return c.runGetResult(backend, input[1:])
	case aiGetModel:
		return c.runGetModel(backend, input[1:])
	case aiListModels:
		return c.runListModels(backend, input[1:])
	default:
		return nil, fmt.Errorf("%w: 0x%02x", errAIInferenceUnknownOp, input[0])
	}
}

// runRequestInference handles selector 0x00.
// Input: modelHash [32]byte + inputCAS [32]byte + tier [1]byte = 65 bytes minimum.
// Output: requestID [32]byte.
func (c *aiInference) runRequestInference(backend InferenceBackend, data []byte) ([]byte, error) {
	if backend == nil {
		return nil, errAIInferenceNoBackend
	}

	// Require at least 65 bytes: 32 (modelHash) + 32 (inputCAS) + 1 (tier)
	const minLen = 65
	if len(data) < minLen {
		return nil, fmt.Errorf("%w: requestInference requires %d bytes, got %d", errAIInferenceInvalidInput, minLen, len(data))
	}

	var modelHash, inputCAS types.Hash
	copy(modelHash[:], data[0:32])
	copy(inputCAS[:], data[32:64])
	tier := data[64]

	// NOTE: The caller address is set to the zero address as a placeholder.
	// The precompile's Run(input) signature does not carry the EVM caller
	// (msg.sender). In production, this should be injected from the EVM
	// execution context (e.g., via a RunWithCaller method or by embedding
	// the caller in the input). Until that plumbing is added, the zero
	// address is used so the request can still be submitted and tracked.
	caller := types.Address{}

	requestID, err := backend.SubmitRequest(modelHash, inputCAS, tier, caller)
	if err != nil {
		return nil, err
	}

	return requestID[:], nil
}

// runGetResult handles selector 0x01.
// Input: requestID [32]byte.
// Output: status [32]byte (left-padded uint8) + outputCAS [32]byte = 64 bytes.
func (c *aiInference) runGetResult(backend InferenceBackend, data []byte) ([]byte, error) {
	if backend == nil {
		return nil, errAIInferenceNoBackend
	}

	if len(data) < 32 {
		return nil, fmt.Errorf("%w: getResult requires 32 bytes, got %d", errAIInferenceInvalidInput, len(data))
	}

	var requestID types.Hash
	copy(requestID[:], data[0:32])

	status, outputCAS, err := backend.GetResult(requestID)
	if err != nil {
		return nil, err
	}

	// Encode: 32-byte left-padded status + 32-byte outputCAS
	result := make([]byte, 64)
	result[31] = status
	copy(result[32:64], outputCAS[:])

	return result, nil
}

// runGetModel handles selector 0x02.
// Input: modelHash [32]byte.
// Output: ABI-encoded (name string, format string, capabilities string).
//
// ABI encoding layout (simplified, no dynamic offsets — uses length-prefixed fields):
//
//	[0:32]   name length
//	[32:32+nameLen] name bytes (padded to 32-byte boundary)
//	...      format length + format bytes (padded)
//	...      capabilities length + capabilities bytes (padded)
func (c *aiInference) runGetModel(backend InferenceBackend, data []byte) ([]byte, error) {
	if backend == nil {
		return nil, errAIInferenceNoBackend
	}

	if len(data) < 32 {
		return nil, fmt.Errorf("%w: getModel requires 32 bytes, got %d", errAIInferenceInvalidInput, len(data))
	}

	var modelHash types.Hash
	copy(modelHash[:], data[0:32])

	name, format, capabilities, err := backend.GetModel(modelHash)
	if err != nil {
		return nil, err
	}

	return encodeABIStrings(name, format, capabilities), nil
}

// Maximum capability string length for listModels to prevent DoS via oversized inputs.
const maxCapabilityLen = 1024

// runListModels handles selector 0x03.
// Input: capability string (raw bytes, no length prefix from caller).
// Output: count [32]byte + modelHashes (count * 32 bytes).
func (c *aiInference) runListModels(backend InferenceBackend, data []byte) ([]byte, error) {
	if backend == nil {
		return nil, errAIInferenceNoBackend
	}

	// Reject oversized capability strings to prevent DoS via large inputs.
	if len(data) > maxCapabilityLen {
		return nil, fmt.Errorf("%w: capability string too long (%d bytes, max %d)", errAIInferenceInvalidInput, len(data), maxCapabilityLen)
	}

	capability := string(data)

	hashes, err := backend.ListModels(capability)
	if err != nil {
		return nil, err
	}

	// Encode: 32-byte count + hashes
	count := uint64(len(hashes))
	result := make([]byte, 32+len(hashes)*32)

	// Left-padded count as uint256
	binary.BigEndian.PutUint64(result[24:32], count)

	for i, h := range hashes {
		copy(result[32+i*32:32+(i+1)*32], h[:])
	}

	return result, nil
}

// encodeABIStrings encodes multiple strings into a simplified ABI format.
// Each string is encoded as: [32-byte length] + [data padded to 32-byte boundary].
//
// This is a simplified encoding used within the precompile. For full ABI
// compatibility, callers should use the standard ABI offset-based encoding.
func encodeABIStrings(strs ...string) []byte {
	// Calculate total output size
	totalSize := 0
	for _, s := range strs {
		totalSize += 32 // length word
		totalSize += paddedLen(len(s))
	}

	out := make([]byte, totalSize)
	offset := 0

	for _, s := range strs {
		// Write length
		binary.BigEndian.PutUint64(out[offset+24:offset+32], uint64(len(s)))
		offset += 32

		// Write string data
		copy(out[offset:], s)
		offset += paddedLen(len(s))
	}

	return out
}

// paddedLen returns the length padded up to the next 32-byte boundary.
// Returns 32 for empty input to maintain ABI alignment.
func paddedLen(n int) int {
	if n == 0 {
		return 32
	}
	return ((n + 31) / 32) * 32
}

// --- ABI decoding helpers for callers ---

// DecodeABIStrings decodes the simplified ABI string encoding produced by encodeABIStrings.
// Returns the decoded strings and any error.
func DecodeABIStrings(data []byte, count int) ([]string, error) {
	result := make([]string, 0, count)
	offset := 0

	for i := 0; i < count; i++ {
		if offset+32 > len(data) {
			return nil, fmt.Errorf("ai inference: truncated ABI data at string %d", i)
		}

		strLen := binary.BigEndian.Uint64(data[offset+24 : offset+32])
		offset += 32

		padded := paddedLen(int(strLen))
		if offset+padded > len(data) {
			return nil, fmt.Errorf("ai inference: truncated string data at string %d", i)
		}

		result = append(result, strings.TrimRight(string(data[offset:offset+int(strLen)]), "\x00"))
		offset += padded
	}

	return result, nil
}
