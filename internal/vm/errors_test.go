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
	"strings"
	"testing"
)

func TestErrStackUnderflowError(t *testing.T) {
	err := &ErrStackUnderflow{stackLen: 2, required: 5}

	errStr := err.Error()
	if !strings.Contains(errStr, "stack underflow") {
		t.Errorf("Error() = %q, want to contain 'stack underflow'", errStr)
	}
	if !strings.Contains(errStr, "2") || !strings.Contains(errStr, "5") {
		t.Errorf("Error() = %q, want to contain stack length and required values", errStr)
	}
}

func TestErrStackOverflowError(t *testing.T) {
	err := &ErrStackOverflow{stackLen: 1025, limit: 1024}

	errStr := err.Error()
	if !strings.Contains(errStr, "stack limit reached") {
		t.Errorf("Error() = %q, want to contain 'stack limit reached'", errStr)
	}
	if !strings.Contains(errStr, "1025") || !strings.Contains(errStr, "1024") {
		t.Errorf("Error() = %q, want to contain stack length and limit values", errStr)
	}
}

func TestErrInvalidOpCodeError(t *testing.T) {
	err := &ErrInvalidOpCode{opcode: PUSH1}

	errStr := err.Error()
	if !strings.Contains(errStr, "invalid opcode") {
		t.Errorf("Error() = %q, want to contain 'invalid opcode'", errStr)
	}
}

func TestErrorConstants(t *testing.T) {
	// Test that error constants are properly defined
	errors := []error{
		ErrInvalidSubroutineEntry,
		ErrOutOfGas,
		ErrCodeStoreOutOfGas,
		ErrDepth,
		ErrInsufficientBalance,
		ErrContractAddressCollision,
		ErrExecutionReverted,
		ErrMaxCodeSizeExceeded,
		ErrInvalidJump,
		ErrWriteProtection,
		ErrReturnDataOutOfBounds,
		ErrGasUintOverflow,
		ErrInvalidRetsub,
		ErrReturnStackExceeded,
		ErrInvalidCode,
		ErrNonceUintOverflow,
	}

	for _, err := range errors {
		if err == nil {
			t.Error("Error constant should not be nil")
		}
		if err.Error() == "" {
			t.Errorf("Error message should not be empty: %v", err)
		}
	}
}
