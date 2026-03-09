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

package bundler

import (
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// StateReader provides read-only access to account state for validation.
type StateReader interface {
	// GetCodeSize returns the code size of the given address.
	GetCodeSize(addr types.Address) int
	// GetNonce returns the nonce of the given address.
	GetNonce(addr types.Address) uint64
	// GetBalance returns the balance of the given address.
	GetBalance(addr types.Address) *uint256.Int
}

// Validator performs static and stateful validation of UserOperations.
type Validator struct {
	config *Config
}

// NewValidator creates a new UserOperation validator.
func NewValidator(config *Config) *Validator {
	return &Validator{config: config}
}

// ValidateStatic performs stateless validation checks that don't require state access.
func (v *Validator) ValidateStatic(op *vm.UserOperation, entryPoint types.Address) error {
	// Check entry point is supported.
	if !v.config.IsSupportedEntryPoint(entryPoint) {
		return fmt.Errorf("unsupported entry point: %s", entryPoint.Hex())
	}

	// Sender must not be zero.
	if op.Sender == (types.Address{}) {
		return fmt.Errorf("%w: sender is zero address", vm.ErrInvalidUserOp)
	}

	// Gas limits must be reasonable.
	if op.CallGasLimit == nil || op.CallGasLimit.IsZero() {
		return fmt.Errorf("%w: callGasLimit is zero", vm.ErrInvalidUserOp)
	}
	if op.VerificationGasLimit == nil || op.VerificationGasLimit.IsZero() {
		return fmt.Errorf("%w: verificationGasLimit is zero", vm.ErrInvalidUserOp)
	}
	if op.PreVerificationGas == nil || op.PreVerificationGas.IsZero() {
		return fmt.Errorf("%w: preVerificationGas is zero", vm.ErrInvalidUserOp)
	}

	// Fee fields must be set.
	if op.MaxFeePerGas == nil || op.MaxFeePerGas.IsZero() {
		return fmt.Errorf("%w: maxFeePerGas is zero", vm.ErrInvalidUserOp)
	}
	if op.MaxPriorityFeePerGas == nil {
		return fmt.Errorf("%w: maxPriorityFeePerGas is nil", vm.ErrInvalidUserOp)
	}

	// maxPriorityFeePerGas must not exceed maxFeePerGas.
	if op.MaxPriorityFeePerGas.Gt(op.MaxFeePerGas) {
		return fmt.Errorf("%w: maxPriorityFeePerGas exceeds maxFeePerGas", vm.ErrInvalidUserOp)
	}

	// Signature must be present.
	if len(op.Signature) == 0 {
		return fmt.Errorf("%w: signature is empty", vm.ErrInvalidUserOp)
	}

	// If paymasterAndData is set, it must be at least 20 bytes (address).
	if len(op.PaymasterAndData) > 0 && len(op.PaymasterAndData) < 20 {
		return fmt.Errorf("%w: paymasterAndData too short", vm.ErrInvalidPaymasterData)
	}

	// Nonce must be set.
	if op.Nonce == nil {
		return fmt.Errorf("%w: nonce is nil", vm.ErrInvalidUserOp)
	}

	return nil
}

// ValidateStateful performs state-dependent validation checks.
func (v *Validator) ValidateStateful(op *vm.UserOperation, state StateReader) error {
	senderExists := state.GetCodeSize(op.Sender) > 0 || state.GetNonce(op.Sender) > 0

	// If sender doesn't exist, initCode must be provided.
	if !senderExists && !op.HasInitCode() {
		return fmt.Errorf("%w: sender not deployed and no initCode", vm.ErrAccountNotDeployed)
	}

	// If sender exists, initCode must be empty.
	if senderExists && op.HasInitCode() {
		return fmt.Errorf("%w: sender already deployed but initCode provided", vm.ErrInvalidUserOp)
	}

	// If no paymaster, check sender has enough balance for the prefund.
	if !op.HasPaymaster() {
		requiredPrefund := vm.CalcRequiredPrefund(op)
		balance := state.GetBalance(op.Sender)
		if balance.Lt(requiredPrefund) {
			return fmt.Errorf("%w: sender balance %s < required prefund %s",
				vm.ErrInvalidUserOp, balance.String(), requiredPrefund.String())
		}
	}

	// If paymaster specified, verify it has code deployed.
	if op.HasPaymaster() {
		paymaster := op.GetPaymaster()
		if state.GetCodeSize(paymaster) == 0 {
			return vm.ErrPaymasterNotDeployed
		}
	}

	return nil
}
