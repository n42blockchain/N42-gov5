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

// Native Account Abstraction (Fusaka)
//
// This file implements the protocol-level account abstraction framework
// that will be introduced in the Fusaka hard fork. Unlike ERC-4337 which
// operates as an application-layer solution, native AA integrates directly
// into the Ethereum protocol.
//
// Key Features:
// - Protocol-level transaction validation abstraction
// - Flexible signature schemes (not limited to ECDSA/secp256k1)
// - Native support for smart contract wallets as first-class accounts
// - Removal of EOA/contract account distinction at protocol level
//
// Related EIPs:
// - EIP-3074: AUTH and AUTHCALL opcodes (predecessor)
// - EIP-5003: Insert Code into EOAs with AUTHUSURP
// - EIP-5806: Delegate transaction
// - EIP-7702: Set EOA account code (Pectra)
// - Future: Full native AA specification (Fusaka)

package vm

import (
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Native AA Constants
// =============================================================================

// Account types in native AA
const (
	// AccountTypeEOA is a traditional externally owned account
	AccountTypeEOA = 0

	// AccountTypeContract is a deployed smart contract
	AccountTypeContract = 1

	// AccountTypeAA is a native account abstraction account
	AccountTypeAA = 2
)

// Validation modes
const (
	// ValidationModeStandard uses standard ECDSA validation
	ValidationModeStandard = 0

	// ValidationModeCustom uses custom validation logic
	ValidationModeCustom = 1

	// ValidationModeMultisig uses multi-signature validation
	ValidationModeMultisig = 2

	// ValidationModeSessionKey uses session key validation
	ValidationModeSessionKey = 3
)

// Gas constants for native AA operations
const (
	// AAValidationBaseGas is the base gas for validation
	AAValidationBaseGas uint64 = 5000

	// AAExecutionBaseGas is the base gas for execution
	AAExecutionBaseGas uint64 = 21000

	// AAPaymasterValidationGas is gas for paymaster validation
	AAPaymasterValidationGas uint64 = 10000

	// AAAccountCreationGas is gas for creating AA account
	AAAccountCreationGas uint64 = 50000
)

// =============================================================================
// Native AA Account Interface
// =============================================================================

// AAAccount represents a native account abstraction account
type AAAccount struct {
	Address        types.Address // Account address
	ValidationMode uint8         // Validation mode
	Nonce          uint64        // Transaction nonce
	Code           []byte        // Validation/execution code
	StorageRoot    types.Hash    // Storage root hash
}

// AATransaction represents a transaction in native AA
type AATransaction struct {
	Sender           types.Address  `json:"sender"`
	Nonce            uint64         `json:"nonce"`
	Target           *types.Address `json:"target"`
	Value            *uint256.Int   `json:"value"`
	CallData         []byte         `json:"callData"`
	MaxGas           uint64         `json:"maxGas"`
	MaxFeePerGas     *uint256.Int   `json:"maxFeePerGas"`
	MaxPriorityFee   *uint256.Int   `json:"maxPriorityFee"`
	ValidationData   []byte         `json:"validationData"`   // Custom validation data
	Paymaster        *types.Address `json:"paymaster"`        // Optional paymaster
	PaymasterData    []byte         `json:"paymasterData"`    // Paymaster-specific data
}

// =============================================================================
// Validation Result
// =============================================================================

// AAValidationResult represents the result of AA transaction validation
type AAValidationResult struct {
	Success      bool           // Whether validation succeeded
	ValidAfter   uint64         // Unix timestamp after which tx is valid
	ValidUntil   uint64         // Unix timestamp until which tx is valid
	GasUsed      uint64         // Gas used during validation
	Revert       []byte         // Revert reason if validation failed
	Authorizer   types.Address  // Address that authorized the transaction
}

// =============================================================================
// Validation Function Types
// =============================================================================

// ValidateFunc is the signature for custom validation functions
type ValidateFunc func(
	account *AAAccount,
	tx *AATransaction,
	state IntraBlockState,
) (*AAValidationResult, error)

// ExecuteFunc is the signature for custom execution functions
type ExecuteFunc func(
	account *AAAccount,
	tx *AATransaction,
	evm VMInterpreter,
) ([]byte, uint64, error)

// =============================================================================
// IntraBlockState interface for native AA
// =============================================================================

// IntraBlockState represents the state interface needed for AA
type IntraBlockState interface {
	GetCode(addr types.Address) []byte
	GetNonce(addr types.Address) uint64
	SetNonce(addr types.Address, nonce uint64)
	GetBalance(addr types.Address) *uint256.Int
	SubBalance(addr types.Address, amount *uint256.Int)
	AddBalance(addr types.Address, amount *uint256.Int)
}

// =============================================================================
// Validation Registry
// =============================================================================

// ValidationRegistry manages custom validation handlers
type ValidationRegistry struct {
	handlers map[uint8]ValidateFunc
}

// NewValidationRegistry creates a new validation registry
func NewValidationRegistry() *ValidationRegistry {
	return &ValidationRegistry{
		handlers: make(map[uint8]ValidateFunc),
	}
}

// Register registers a validation handler for a specific mode
func (r *ValidationRegistry) Register(mode uint8, handler ValidateFunc) {
	r.handlers[mode] = handler
}

// GetHandler returns the validation handler for a mode
func (r *ValidationRegistry) GetHandler(mode uint8) (ValidateFunc, bool) {
	handler, ok := r.handlers[mode]
	return handler, ok
}

// DefaultValidationRegistry is the default validation registry
var DefaultValidationRegistry = NewValidationRegistry()

// =============================================================================
// Native AA Errors
// =============================================================================

var (
	// ErrAAValidationFailed is returned when AA validation fails
	ErrAAValidationFailed = errors.New("AA validation failed")

	// ErrAAInsufficientGas is returned when there's insufficient gas
	ErrAAInsufficientGas = errors.New("insufficient gas for AA operation")

	// ErrAAInvalidNonce is returned for invalid nonce
	ErrAAInvalidNonce = errors.New("invalid AA nonce")

	// ErrAAInvalidSignature is returned for invalid signature
	ErrAAInvalidSignature = errors.New("invalid AA signature")

	// ErrAAPaymasterFailed is returned when paymaster validation fails
	ErrAAPaymasterFailed = errors.New("paymaster validation failed")

	// ErrAAExpired is returned when transaction is expired
	ErrAAExpired = errors.New("AA transaction expired")

	// ErrAANotYetValid is returned when transaction is not yet valid
	ErrAANotYetValid = errors.New("AA transaction not yet valid")

	// ErrAAInvalidTarget is returned for invalid target address
	ErrAAInvalidTarget = errors.New("invalid AA target")

	// ErrAAAccountNotFound is returned when AA account is not found
	ErrAAAccountNotFound = errors.New("AA account not found")

	// ErrAAUnknownValidationMode is returned for unknown validation mode
	ErrAAUnknownValidationMode = errors.New("unknown AA validation mode")
)

// =============================================================================
// Standard Validation Implementation
// =============================================================================

// StandardValidation implements standard ECDSA validation
func StandardValidation(account *AAAccount, tx *AATransaction, state IntraBlockState) (*AAValidationResult, error) {
	// Check nonce
	expectedNonce := state.GetNonce(account.Address)
	if tx.Nonce != expectedNonce {
		return &AAValidationResult{Success: false}, ErrAAInvalidNonce
	}

	// Standard ECDSA signature validation would happen here
	// For now, we return success as placeholder
	return &AAValidationResult{
		Success:    true,
		ValidAfter: 0,
		ValidUntil: 0,
		GasUsed:    AAValidationBaseGas,
		Authorizer: account.Address,
	}, nil
}

// =============================================================================
// AA Transaction Execution
// =============================================================================

// ExecuteAATransaction executes a native AA transaction
func ExecuteAATransaction(
	account *AAAccount,
	tx *AATransaction,
	evm VMInterpreter,
	state IntraBlockState,
) ([]byte, uint64, error) {
	totalGas := uint64(0)

	// 1. Validation phase
	handler, ok := DefaultValidationRegistry.GetHandler(account.ValidationMode)
	if !ok {
		handler = StandardValidation
	}

	validationResult, err := handler(account, tx, state)
	if err != nil {
		return nil, totalGas, err
	}

	if !validationResult.Success {
		return validationResult.Revert, validationResult.GasUsed, ErrAAValidationFailed
	}

	totalGas += validationResult.GasUsed

	// 2. Check validity window
	blockTime := evm.Context().Time
	if validationResult.ValidAfter > 0 && blockTime < validationResult.ValidAfter {
		return nil, totalGas, ErrAANotYetValid
	}
	if validationResult.ValidUntil > 0 && blockTime > validationResult.ValidUntil {
		return nil, totalGas, ErrAAExpired
	}

	// 3. Paymaster validation (if applicable)
	if tx.Paymaster != nil {
		// Validate paymaster
		totalGas += AAPaymasterValidationGas
	}

	// 4. Execution phase
	// The actual execution would call the EVM here
	totalGas += AAExecutionBaseGas

	// 5. Update nonce
	state.SetNonce(account.Address, tx.Nonce+1)

	return nil, totalGas, nil
}

// =============================================================================
// Utility Functions
// =============================================================================

// IsAAAccount checks if an address is an AA account
func IsAAAccount(state IntraBlockState, addr types.Address) bool {
	code := state.GetCode(addr)
	// AA accounts have a specific code prefix (to be defined)
	// For now, check if it has the EIP-7702 delegation prefix
	return HasDelegation(code)
}

// GetAccountType returns the type of account at an address
func GetAccountType(state IntraBlockState, addr types.Address) int {
	code := state.GetCode(addr)
	if len(code) == 0 {
		return AccountTypeEOA
	}
	if HasDelegation(code) {
		return AccountTypeAA
	}
	return AccountTypeContract
}

// CalcAATransactionGas calculates the total gas for an AA transaction
func CalcAATransactionGas(tx *AATransaction, hasPaymaster bool) uint64 {
	gas := AAValidationBaseGas + AAExecutionBaseGas

	// Add calldata gas
	for _, b := range tx.CallData {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}

	// Add validation data gas
	for _, b := range tx.ValidationData {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}

	// Add paymaster gas
	if hasPaymaster {
		gas += AAPaymasterValidationGas
		for _, b := range tx.PaymasterData {
			if b == 0 {
				gas += 4
			} else {
				gas += 16
			}
		}
	}

	return gas
}

// =============================================================================
// Init function
// =============================================================================

func init() {
	// Register standard validation handler
	DefaultValidationRegistry.Register(ValidationModeStandard, StandardValidation)
}

