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

// ERC-4337: Account Abstraction Using Alt Mempool
// This file implements the ERC-4337 specification for account abstraction
// prior to native protocol-level support in Pectra.
//
// Reference: https://eips.ethereum.org/EIPS/eip-4337

package vm

import (
	"errors"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// ERC-4337 Constants
// =============================================================================

// EntryPoint contract addresses for different versions
var (
	// EntryPointV06 is the official v0.6 EntryPoint address
	EntryPointV06 = types.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")

	// EntryPointV07 is the official v0.7 EntryPoint address
	EntryPointV07 = types.HexToAddress("0x0000000071727De22E5E9d8BAf0edAc6f37da032")

	// SenderCreator is the helper contract for creating accounts
	SenderCreator = types.HexToAddress("0x7fc98430eAEdbb6070B35B39D798725049088348")
)

// Gas constants for ERC-4337 operations
const (
	// UserOperationCallGasLimit is the minimum gas for executing a UserOperation
	UserOperationCallGasLimit = 35000

	// VerificationGasLimit is the minimum gas for verification
	VerificationGasLimit = 70000

	// PreVerificationGas is the overhead for processing a UserOperation
	PreVerificationGas = 21000

	// PaymasterPostOpGasLimit is gas for paymaster post-operation
	PaymasterPostOpGasLimit = 50000

	// CreateSenderGas is the gas for creating sender account
	CreateSenderGas = 1000000

	// MaxContextSize is the maximum size of paymaster context
	MaxContextSize = 65536
)

// Method selectors for EntryPoint contract
var (
	// HandleOps selector: handleOps(UserOperation[],address)
	HandleOpsSelector = []byte{0x1f, 0xad, 0x94, 0x8c}

	// HandleAggregatedOps selector
	HandleAggregatedOpsSelector = []byte{0x4b, 0x1d, 0x7c, 0xf5}

	// SimulateValidation selector
	SimulateValidationSelector = []byte{0xee, 0x21, 0x94, 0x23}

	// SimulateHandleOp selector
	SimulateHandleOpSelector = []byte{0xd6, 0x38, 0x3f, 0x94}
)

// =============================================================================
// UserOperation Structure (ERC-4337)
// =============================================================================

// UserOperation represents an ERC-4337 user operation
type UserOperation struct {
	Sender               types.Address  `json:"sender"`               // Account making the operation
	Nonce                *uint256.Int   `json:"nonce"`                // Anti-replay parameter
	InitCode             []byte         `json:"initCode"`             // Account creation code (if account doesn't exist)
	CallData             []byte         `json:"callData"`             // Data to pass to sender
	CallGasLimit         *uint256.Int   `json:"callGasLimit"`         // Gas for the main execution call
	VerificationGasLimit *uint256.Int   `json:"verificationGasLimit"` // Gas for verification phase
	PreVerificationGas   *uint256.Int   `json:"preVerificationGas"`   // Gas for data and pre-processing
	MaxFeePerGas         *uint256.Int   `json:"maxFeePerGas"`         // Maximum fee per gas (EIP-1559)
	MaxPriorityFeePerGas *uint256.Int   `json:"maxPriorityFeePerGas"` // Maximum priority fee per gas
	PaymasterAndData     []byte         `json:"paymasterAndData"`     // Paymaster address and data
	Signature            []byte         `json:"signature"`            // Signature over the entire operation
}

// UserOperationV07 represents an ERC-4337 v0.7 user operation with packed gas values
type UserOperationV07 struct {
	Sender             types.Address `json:"sender"`
	Nonce              *uint256.Int  `json:"nonce"`
	Factory            types.Address `json:"factory,omitempty"`
	FactoryData        []byte        `json:"factoryData,omitempty"`
	CallData           []byte        `json:"callData"`
	CallGasLimit       *uint256.Int  `json:"callGasLimit"`
	VerificationGasLimit *uint256.Int `json:"verificationGasLimit"`
	PreVerificationGas *uint256.Int  `json:"preVerificationGas"`
	MaxFeePerGas       *uint256.Int  `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *uint256.Int `json:"maxPriorityFeePerGas"`
	Paymaster          types.Address `json:"paymaster,omitempty"`
	PaymasterVerificationGasLimit *uint256.Int `json:"paymasterVerificationGasLimit,omitempty"`
	PaymasterPostOpGasLimit *uint256.Int `json:"paymasterPostOpGasLimit,omitempty"`
	PaymasterData      []byte        `json:"paymasterData,omitempty"`
	Signature          []byte        `json:"signature"`
}

// GetFactory extracts the factory address from initCode
func (op *UserOperation) GetFactory() types.Address {
	if len(op.InitCode) >= 20 {
		return types.BytesToAddress(op.InitCode[:20])
	}
	return types.Address{}
}

// GetFactoryData extracts the factory data from initCode
func (op *UserOperation) GetFactoryData() []byte {
	if len(op.InitCode) > 20 {
		return op.InitCode[20:]
	}
	return nil
}

// GetPaymaster extracts the paymaster address from paymasterAndData
func (op *UserOperation) GetPaymaster() types.Address {
	if len(op.PaymasterAndData) >= 20 {
		return types.BytesToAddress(op.PaymasterAndData[:20])
	}
	return types.Address{}
}

// GetPaymasterData extracts the paymaster data from paymasterAndData
func (op *UserOperation) GetPaymasterData() []byte {
	if len(op.PaymasterAndData) > 20 {
		return op.PaymasterAndData[20:]
	}
	return nil
}

// HasInitCode returns true if the operation has init code
func (op *UserOperation) HasInitCode() bool {
	return len(op.InitCode) > 0
}

// HasPaymaster returns true if the operation has a paymaster
func (op *UserOperation) HasPaymaster() bool {
	return len(op.PaymasterAndData) >= 20
}

// =============================================================================
// Account Interface (IAccount)
// =============================================================================

// AccountValidationResult represents the result of account validation
type AccountValidationResult struct {
	ValidAfter  uint64       // Unix timestamp after which the operation is valid
	ValidUntil  uint64       // Unix timestamp until which the operation is valid
	Authorizer  types.Address // 0 for valid, 1 for invalid, or aggregator address
}

// SIG_VALIDATION constants
const (
	// SIG_VALIDATION_SUCCEEDED means the signature is valid
	SIG_VALIDATION_SUCCEEDED = 0
	// SIG_VALIDATION_FAILED means the signature is invalid
	SIG_VALIDATION_FAILED = 1
)

// PackValidationData packs validation data into a single uint256
func PackValidationData(result *AccountValidationResult) *uint256.Int {
	packed := new(uint256.Int)

	// Format: authorizer (20 bytes) | validUntil (6 bytes) | validAfter (6 bytes)
	authorizerBig := new(big.Int).SetBytes(result.Authorizer.Bytes())
	packed.SetFromBig(authorizerBig)
	packed.Lsh(packed, 48) // Shift left 48 bits for validUntil
	packed.Or(packed, uint256.NewInt(result.ValidUntil))
	packed.Lsh(packed, 48) // Shift left 48 bits for validAfter
	packed.Or(packed, uint256.NewInt(result.ValidAfter))

	return packed
}

// UnpackValidationData unpacks validation data from a uint256
func UnpackValidationData(packed *uint256.Int) *AccountValidationResult {
	result := &AccountValidationResult{}

	// Extract validAfter (lowest 48 bits)
	validAfterMask := uint256.NewInt(0xffffffffffff)
	validAfterInt := new(uint256.Int).And(packed, validAfterMask)
	result.ValidAfter = validAfterInt.Uint64()

	// Extract validUntil (next 48 bits)
	shifted := new(uint256.Int).Rsh(packed, 48)
	validUntilInt := new(uint256.Int).And(shifted, validAfterMask)
	result.ValidUntil = validUntilInt.Uint64()

	// Extract authorizer (highest 160 bits)
	shifted = new(uint256.Int).Rsh(packed, 96)
	authorizerBytes := shifted.Bytes20()
	result.Authorizer = types.BytesToAddress(authorizerBytes[:])

	return result
}

// =============================================================================
// Stake Manager Interface
// =============================================================================

// StakeInfo represents staking information for an entity
type StakeInfo struct {
	Stake           *uint256.Int // Amount staked
	UnstakeDelaySec uint32       // Delay before unstake is effective
}

// DepositInfo represents deposit information
type DepositInfo struct {
	Deposit         *uint256.Int // Current deposit
	Staked          bool         // Whether entity is staked
	Stake           *uint256.Int // Amount staked
	UnstakeDelaySec uint32       // Unstake delay
	WithdrawTime    uint64       // When withdrawal is available
}

// =============================================================================
// Errors
// =============================================================================

var (
	// ErrInvalidUserOp is returned for invalid user operations
	ErrInvalidUserOp = errors.New("invalid user operation")

	// ErrFailedOp is returned when operation execution fails
	ErrFailedOp = errors.New("operation failed")

	// ErrInsufficientStake is returned when entity has insufficient stake
	ErrInsufficientStake = errors.New("insufficient stake")

	// ErrSignatureValidationFailed is returned when signature validation fails
	ErrSignatureValidationFailed = errors.New("signature validation failed")

	// ErrAccountNotDeployed is returned when account is not deployed
	ErrAccountNotDeployed = errors.New("account not deployed")

	// ErrPaymasterNotDeployed is returned when paymaster is not deployed
	ErrPaymasterNotDeployed = errors.New("paymaster not deployed")

	// ErrInvalidAccountNonce is returned for invalid nonce
	ErrInvalidAccountNonce = errors.New("invalid account nonce")

	// ErrInvalidPaymasterData is returned for invalid paymaster data
	ErrInvalidPaymasterData = errors.New("invalid paymaster data")

	// ErrExpiredOrNotDue is returned when operation is expired or not yet due
	ErrExpiredOrNotDue = errors.New("expired or not due")

	// ErrAggregatorNotStaked is returned when aggregator is not staked
	ErrAggregatorNotStaked = errors.New("aggregator not staked")
)

// =============================================================================
// Gas Calculation Helpers
// =============================================================================

// CalcPreVerificationGas calculates the pre-verification gas for a UserOperation
func CalcPreVerificationGas(op *UserOperation, baseFee *uint256.Int) uint64 {
	// Base cost
	gas := uint64(PreVerificationGas)

	// Add gas for calldata
	for _, b := range op.CallData {
		if b == 0 {
			gas += 4 // Zero byte
		} else {
			gas += 16 // Non-zero byte
		}
	}

	// Add gas for initCode if present
	for _, b := range op.InitCode {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}

	// Add gas for paymasterAndData if present
	for _, b := range op.PaymasterAndData {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}

	// Add gas for signature
	for _, b := range op.Signature {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}

	return gas
}

// CalcRequiredPrefund calculates the required prefund for a UserOperation
func CalcRequiredPrefund(op *UserOperation) *uint256.Int {
	// requiredPrefund = (callGasLimit + verificationGasLimit + preVerificationGas) * maxFeePerGas
	totalGas := new(uint256.Int).Add(op.CallGasLimit, op.VerificationGasLimit)
	totalGas.Add(totalGas, op.PreVerificationGas)

	return new(uint256.Int).Mul(totalGas, op.MaxFeePerGas)
}

// =============================================================================
// EntryPoint Event Signatures
// =============================================================================

var (
	// UserOperationEvent signature: UserOperationEvent(bytes32,address,address,uint256,bool,uint256,uint256)
	UserOperationEventSig = types.HexToHash("0x49628fd1471006c1482da88028e9ce4dbb080b815c9b0344d39e5a8e6ec1419f")

	// AccountDeployed signature: AccountDeployed(bytes32,address,address,address)
	AccountDeployedSig = types.HexToHash("0xd51a9c61267aa6196961883ecf5ff2da6619c37dac0fa92122513fb32c032d2d")

	// UserOperationRevertReason signature: UserOperationRevertReason(bytes32,address,uint256,bytes)
	UserOperationRevertReasonSig = types.HexToHash("0x1c4fada7374c0a9ee8841fc38afe82932dc0f8e69012e927f061a8bae611a201")

	// PostOpRevertReason signature: PostOpRevertReason(bytes32,address,uint256,bytes)
	PostOpRevertReasonSig = types.HexToHash("0xf62676f440ff169a3a9afdbf812e89e7f95975ee8e5c31214ffdef631c5f4792")

	// BeforeExecution signature: BeforeExecution()
	BeforeExecutionSig = types.HexToHash("0xbb47ee3e183a558b1a2ff0874b079f3fc5478b7454eacf2bfc5af2ff5878f972")

	// Deposited signature: Deposited(address,uint256)
	DepositedSig = types.HexToHash("0x2da466a7b24304f47e87fa2e1e5a81b9831ce54fec19055ce277ca2f39ba42c4")

	// Withdrawn signature: Withdrawn(address,address,uint256)
	WithdrawnSig = types.HexToHash("0xd1c19fbcd4551a5edfb66d43d2e337c04837afda3482b42bdf569a8fccdae5fb")

	// StakeLocked signature: StakeLocked(address,uint256,uint256)
	StakeLockedSig = types.HexToHash("0xa5ae833d0bb1dcd632d98a8b70973e8516812898e47bf730571209f182e2a358")

	// StakeUnlocked signature: StakeUnlocked(address,uint256)
	StakeUnlockedSig = types.HexToHash("0xfa9b3c14cc825c412c9ed81b3ba365a5b459439403f18829e572ed53a4180f0a")

	// StakeWithdrawn signature: StakeWithdrawn(address,address,uint256)
	StakeWithdrawnSig = types.HexToHash("0xb7c918e0e249f999e965cafeb6c664271b3f4317d296461500e71da39f0cbda3")
)

// =============================================================================
// Utility Functions
// =============================================================================

// IsEntryPoint checks if the given address is a known EntryPoint
func IsEntryPoint(addr types.Address) bool {
	return addr == EntryPointV06 || addr == EntryPointV07
}

// IsSenderCreator checks if the given address is the SenderCreator
func IsSenderCreator(addr types.Address) bool {
	return addr == SenderCreator
}

