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

// Error-path coverage for the AI inference precompile (0x0301): backend
// error propagation for each selector, and malformed/short-input rejection,
// complementing the happy-path mock in contracts_ai_inference_test.go.

package vm

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

var errMockBackendFailure = errors.New("mock backend failure")

// erroringInferenceBackend always fails, to exercise Run()'s backend
// error-propagation branch for every selector.
type erroringInferenceBackend struct{}

func (erroringInferenceBackend) SubmitRequest(types.Hash, types.Hash, uint8, types.Address) (types.Hash, error) {
	return types.Hash{}, errMockBackendFailure
}

func (erroringInferenceBackend) GetResult(types.Hash) (uint8, types.Hash, error) {
	return 0, types.Hash{}, errMockBackendFailure
}

func (erroringInferenceBackend) GetModel(types.Hash) (string, string, string, error) {
	return "", "", "", errMockBackendFailure
}

func (erroringInferenceBackend) ListModels(string) ([]types.Hash, error) {
	return nil, errMockBackendFailure
}

func TestAIInferenceBackendErrorsPropagate(t *testing.T) {
	SetInferenceBackend(erroringInferenceBackend{})
	defer SetInferenceBackend(nil)

	c := &aiInference{}

	t.Run("requestInference", func(t *testing.T) {
		input := make([]byte, 66)
		input[0] = aiRequestInference
		_, err := c.Run(input)
		if !errors.Is(err, errMockBackendFailure) {
			t.Fatalf("err = %v, want %v", err, errMockBackendFailure)
		}
	})

	t.Run("getResult", func(t *testing.T) {
		input := make([]byte, 33)
		input[0] = aiGetResult
		_, err := c.Run(input)
		if !errors.Is(err, errMockBackendFailure) {
			t.Fatalf("err = %v, want %v", err, errMockBackendFailure)
		}
	})

	t.Run("getModel", func(t *testing.T) {
		input := make([]byte, 33)
		input[0] = aiGetModel
		_, err := c.Run(input)
		if !errors.Is(err, errMockBackendFailure) {
			t.Fatalf("err = %v, want %v", err, errMockBackendFailure)
		}
	})

	t.Run("listModels", func(t *testing.T) {
		input := []byte{aiListModels}
		input = append(input, []byte("some-capability")...)
		_, err := c.Run(input)
		if !errors.Is(err, errMockBackendFailure) {
			t.Fatalf("err = %v, want %v", err, errMockBackendFailure)
		}
	})
}

func TestAIInferenceShortInputRejected(t *testing.T) {
	SetInferenceBackend(erroringInferenceBackend{}) // backend present, so we hit length checks not nil-backend checks
	defer SetInferenceBackend(nil)

	c := &aiInference{}

	tests := []struct {
		name  string
		input []byte
	}{
		{"requestInference_too_short", []byte{aiRequestInference, 0x01, 0x02}},
		{"getResult_too_short", []byte{aiGetResult, 0x01}},
		{"getModel_too_short", []byte{aiGetModel, 0x01}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.Run(tt.input)
			if !errors.Is(err, errAIInferenceInvalidInput) {
				t.Fatalf("err = %v, want errAIInferenceInvalidInput", err)
			}
		})
	}
}

func TestAIInferenceListModelsCapabilityAtLimit(t *testing.T) {
	SetInferenceBackend(erroringInferenceBackend{})
	defer SetInferenceBackend(nil)

	c := &aiInference{}
	// Exactly maxCapabilityLen bytes must be accepted (reach the backend,
	// which then fails), not rejected as "too long".
	input := append([]byte{aiListModels}, make([]byte, maxCapabilityLen)...)
	_, err := c.Run(input)
	if !errors.Is(err, errMockBackendFailure) {
		t.Fatalf("err = %v, want backend failure (input at the length limit should be accepted)", err)
	}
}

func TestAIInferenceRequiredGasPerSelector(t *testing.T) {
	c := &aiInference{}

	if got := c.RequiredGas(nil); got != AIInferenceGetResultGas {
		t.Errorf("RequiredGas(nil) = %d, want %d", got, AIInferenceGetResultGas)
	}
	if got := c.RequiredGas([]byte{aiGetResult}); got != AIInferenceGetResultGas {
		t.Errorf("RequiredGas(getResult) = %d, want %d", got, AIInferenceGetResultGas)
	}
	if got := c.RequiredGas([]byte{aiGetModel}); got != AIInferenceGetModelGas {
		t.Errorf("RequiredGas(getModel) = %d, want %d", got, AIInferenceGetModelGas)
	}
	if got := c.RequiredGas([]byte{aiListModels}); got != AIInferenceListModelsGas {
		t.Errorf("RequiredGas(listModels) = %d, want %d", got, AIInferenceListModelsGas)
	}
	if got := c.RequiredGas([]byte{0xFE}); got != AIInferenceGetResultGas {
		t.Errorf("RequiredGas(unknown) = %d, want %d", got, AIInferenceGetResultGas)
	}
	req := append([]byte{aiRequestInference}, make([]byte, 65)...)
	if got, want := c.RequiredGas(req), AIInferenceBaseGas+AIInferencePerByteGas*65; got != want {
		t.Errorf("RequiredGas(requestInference) = %d, want %d", got, want)
	}
}
