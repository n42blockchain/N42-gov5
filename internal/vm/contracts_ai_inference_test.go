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
	"errors"
	"testing"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

func TestAIInferencePrecompileAddress(t *testing.T) {
	expected := types.HexToAddress("0x0000000000000000000000000000000000000301")
	if AIInferenceAddress != expected {
		t.Fatalf("AIInferenceAddress = %s, want %s", AIInferenceAddress.Hex(), expected.Hex())
	}

	// Verify it's in the precompile map.
	pc, ok := PrecompiledContractsAIInference[AIInferenceAddress]
	if !ok {
		t.Fatal("AIInferenceAddress not found in PrecompiledContractsAIInference map")
	}
	if pc == nil {
		t.Fatal("precompile contract at AIInferenceAddress is nil")
	}
}

func TestAIInferenceRequiredGas(t *testing.T) {
	c := &aiInference{}

	tests := []struct {
		name  string
		input []byte
		want  uint64
	}{
		{
			name:  "empty input",
			input: []byte{},
			want:  AIInferenceGetResultGas,
		},
		{
			name:  "requestInference selector with 65 bytes payload",
			input: append([]byte{aiRequestInference}, make([]byte, 65)...),
			want:  AIInferenceBaseGas + AIInferencePerByteGas*65,
		},
		{
			name:  "getResult selector",
			input: []byte{aiGetResult},
			want:  AIInferenceGetResultGas,
		},
		{
			name:  "getModel selector",
			input: []byte{aiGetModel},
			want:  AIInferenceGetModelGas,
		},
		{
			name:  "listModels selector",
			input: []byte{aiListModels},
			want:  AIInferenceListModelsGas,
		},
		{
			name:  "unknown selector",
			input: []byte{0xFF},
			want:  AIInferenceGetResultGas,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.RequiredGas(tc.input)
			if got != tc.want {
				t.Errorf("RequiredGas() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAIInferenceRunNoBackend(t *testing.T) {
	// Ensure no backend is set for this test.
	SetInferenceBackend(nil)
	defer SetInferenceBackend(nil)

	c := &aiInference{}

	// Test requestInference with nil backend.
	input := make([]byte, 66)
	input[0] = aiRequestInference
	_, err := c.Run(input)
	if err == nil {
		t.Fatal("expected error when backend is nil")
	}
	if !errors.Is(err, errAIInferenceNoBackend) {
		t.Fatalf("expected errAIInferenceNoBackend, got: %v", err)
	}

	// Test getResult with nil backend.
	input2 := make([]byte, 33)
	input2[0] = aiGetResult
	_, err = c.Run(input2)
	if err == nil {
		t.Fatal("expected error when backend is nil for getResult")
	}
	if !errors.Is(err, errAIInferenceNoBackend) {
		t.Fatalf("expected errAIInferenceNoBackend for getResult, got: %v", err)
	}

	// Test invalid input.
	_, err = c.Run(nil)
	if err == nil {
		t.Fatal("expected error for nil input")
	}
	if !errors.Is(err, errAIInferenceInvalidInput) {
		t.Fatalf("expected errAIInferenceInvalidInput, got: %v", err)
	}

	// Test unknown selector.
	_, err = c.Run([]byte{0xFE})
	if err == nil {
		t.Fatal("expected error for unknown selector")
	}
}

// --- Mock backend for TestAIInferenceWithMockBackend ---

type mockInferenceBackend struct{}

func (m *mockInferenceBackend) SubmitRequest(modelHash, inputCAS types.Hash, tier uint8, caller types.Address) (types.Hash, error) {
	return crypto.Keccak256Hash(modelHash[:]), nil
}

func (m *mockInferenceBackend) GetResult(requestID types.Hash) (uint8, types.Hash, error) {
	return 3, crypto.Keccak256Hash(requestID[:]), nil // status=verified
}

func (m *mockInferenceBackend) GetModel(modelHash types.Hash) (string, string, string, error) {
	return "test-model", "onnx", "text-gen", nil
}

func (m *mockInferenceBackend) ListModels(capability string) ([]types.Hash, error) {
	return []types.Hash{crypto.Keccak256Hash([]byte(capability))}, nil
}

func TestAIInferenceWithMockBackend(t *testing.T) {
	mock := &mockInferenceBackend{}
	SetInferenceBackend(mock)
	defer SetInferenceBackend(nil)

	c := &aiInference{}

	// --- requestInference (selector 0x00) ---
	t.Run("requestInference", func(t *testing.T) {
		input := make([]byte, 1+32+32+1) // selector + modelHash + inputCAS + tier
		input[0] = aiRequestInference

		// Fill modelHash (bytes 1..32)
		var modelHash types.Hash
		modelHash[0] = 0xAA
		copy(input[1:33], modelHash[:])

		// Fill inputCAS (bytes 33..64)
		var inputCAS types.Hash
		inputCAS[0] = 0xBB
		copy(input[33:65], inputCAS[:])

		// Tier byte
		input[65] = 1

		out, err := c.Run(input)
		if err != nil {
			t.Fatalf("requestInference failed: %v", err)
		}
		if len(out) != 32 {
			t.Fatalf("requestInference output length = %d, want 32", len(out))
		}

		// The mock returns Keccak256Hash(modelHash[:]), verify it matches.
		expected := crypto.Keccak256Hash(modelHash[:])
		var got types.Hash
		copy(got[:], out)
		if got != expected {
			t.Fatalf("requestInference requestID = %s, want %s", got.Hex(), expected.Hex())
		}
	})

	// --- getResult (selector 0x01) ---
	t.Run("getResult", func(t *testing.T) {
		var requestID types.Hash
		requestID[0] = 0xCC
		input := make([]byte, 1+32)
		input[0] = aiGetResult
		copy(input[1:], requestID[:])

		out, err := c.Run(input)
		if err != nil {
			t.Fatalf("getResult failed: %v", err)
		}
		// Output: 32-byte status (left-padded) + 32-byte outputCAS = 64 bytes
		if len(out) != 64 {
			t.Fatalf("getResult output length = %d, want 64", len(out))
		}

		// Status byte is at out[31] (left-padded uint8 in 32 bytes)
		status := out[31]
		if status != 3 {
			t.Fatalf("getResult status = %d, want 3 (verified)", status)
		}

		// outputCAS is Keccak256Hash(requestID[:])
		expectedCAS := crypto.Keccak256Hash(requestID[:])
		var gotCAS types.Hash
		copy(gotCAS[:], out[32:64])
		if gotCAS != expectedCAS {
			t.Fatalf("getResult outputCAS = %s, want %s", gotCAS.Hex(), expectedCAS.Hex())
		}
	})

	// --- getModel (selector 0x02) ---
	t.Run("getModel", func(t *testing.T) {
		var modelHash types.Hash
		modelHash[0] = 0xDD
		input := make([]byte, 1+32)
		input[0] = aiGetModel
		copy(input[1:], modelHash[:])

		out, err := c.Run(input)
		if err != nil {
			t.Fatalf("getModel failed: %v", err)
		}
		// Output should be ABI-encoded strings: "test-model", "onnx", "text-gen"
		if len(out) == 0 {
			t.Fatal("getModel returned empty output")
		}

		strs, err := DecodeABIStrings(out, 3)
		if err != nil {
			t.Fatalf("DecodeABIStrings: %v", err)
		}
		if strs[0] != "test-model" {
			t.Fatalf("name = %q, want %q", strs[0], "test-model")
		}
		if strs[1] != "onnx" {
			t.Fatalf("format = %q, want %q", strs[1], "onnx")
		}
		if strs[2] != "text-gen" {
			t.Fatalf("capabilities = %q, want %q", strs[2], "text-gen")
		}
	})

	// --- listModels (selector 0x03) ---
	t.Run("listModels", func(t *testing.T) {
		capability := "text-gen"
		input := make([]byte, 1+len(capability))
		input[0] = aiListModels
		copy(input[1:], capability)

		out, err := c.Run(input)
		if err != nil {
			t.Fatalf("listModels failed: %v", err)
		}
		// Output: 32-byte count + N * 32-byte hashes
		if len(out) < 32 {
			t.Fatalf("listModels output too short: %d bytes", len(out))
		}

		// Count is in the last 8 bytes of the first 32-byte word (big-endian uint64).
		count := uint64(0)
		for i := 24; i < 32; i++ {
			count = (count << 8) | uint64(out[i])
		}
		if count != 1 {
			t.Fatalf("listModels count = %d, want 1", count)
		}

		if len(out) != 32+32 {
			t.Fatalf("listModels output length = %d, want 64", len(out))
		}

		expectedHash := crypto.Keccak256Hash([]byte(capability))
		var gotHash types.Hash
		copy(gotHash[:], out[32:64])
		if gotHash != expectedHash {
			t.Fatalf("listModels hash = %s, want %s", gotHash.Hex(), expectedHash.Hex())
		}
	})
}

func TestAIInferenceGasBoundsCheck(t *testing.T) {
	c := &aiInference{}

	// Build a 1MB input for requestInference.
	largeInput := make([]byte, 1+1024*1024) // selector + 1MB payload
	largeInput[0] = aiRequestInference

	gas := c.RequiredGas(largeInput)
	expectedGas := AIInferenceBaseGas + AIInferencePerByteGas*uint64(1024*1024)
	if gas != expectedGas {
		t.Fatalf("RequiredGas for 1MB input = %d, want %d", gas, expectedGas)
	}

	// Sanity check: gas should be very large for 1MB of data.
	if gas < 100_000_000 {
		t.Fatalf("gas for 1MB input suspiciously low: %d", gas)
	}
}

func TestAIInferenceListModelsCapabilityTooLong(t *testing.T) {
	mock := &mockInferenceBackend{}
	SetInferenceBackend(mock)
	defer SetInferenceBackend(nil)

	c := &aiInference{}

	// Build input with capability > 1024 bytes.
	longCapability := make([]byte, 1025)
	for i := range longCapability {
		longCapability[i] = 'a'
	}
	input := make([]byte, 1+len(longCapability))
	input[0] = aiListModels
	copy(input[1:], longCapability)

	_, err := c.Run(input)
	if err == nil {
		t.Fatal("expected error for capability > 1024 bytes")
	}
	if !errors.Is(err, errAIInferenceInvalidInput) {
		t.Fatalf("expected errAIInferenceInvalidInput, got: %v", err)
	}
}
