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
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// VMInterpreter is the interface for EVM used by the interpreter.
// This provides access to chain rules, state, and gas management.
type VMInterpreter interface {
	// VMCaller provides call/create operations
	VMCaller

	// ChainRules returns the active chain rules
	ChainRules() *params.Rules

	// ChainConfig returns the chain configuration
	ChainConfig() *params.ChainConfig

	// IntraBlockState returns the state accessor
	IntraBlockState() evmtypes.IntraBlockState

	// Context returns the block context
	Context() evmtypes.BlockContext

	// TxContext returns the transaction context
	TxContext() evmtypes.TxContext

	// Config returns the VM configuration
	Config() Config

	// SetCallGasTemp sets the call gas temp
	SetCallGasTemp(gas uint64)

	// CallGasTemp returns the call gas temp
	CallGasTemp() uint64

	// Cancelled returns true if the VM operation was cancelled
	Cancelled() bool

	// Reset resets the VM with a new transaction context
	Reset(txCtx evmtypes.TxContext, ibs evmtypes.IntraBlockState)
}

// VMInterface is an alias for VMInterpreter used by tracers.
// This maintains backward compatibility with existing tracer code.
type VMInterface = VMInterpreter

// VMCaller is the interface for EVM execution engine call operations.
// This interface enables:
//   - Dependency injection for testing
//   - Future VM implementations (e.g., optimized VMs, alternative interpreters)
//   - Instrumentation and tracing without modifying core EVM
//
// Architecture:
//
//	┌──────────────┐     ┌──────────────┐
//	│  blockchain  │     │   tracers    │
//	└──────┬───────┘     └──────┬───────┘
//	       │                    │
//	       ▼                    ▼
//	┌──────────────────────────────────┐
//	│          VMCaller Interface      │
//	├──────────────────────────────────┤
//	│  Call(), Create(), StaticCall()  │
//	│  DelegateCall(), CallCode()      │
//	└──────────────┬───────────────────┘
//	               │ implements
//	    ┌──────────┴──────────┐
//	    ▼                     ▼
//	┌──────────┐       ┌──────────────┐
//	│   EVM    │       │InstrumentedVM│
//	└──────────┘       └──────────────┘
type VMCaller interface {
	// Call executes a contract call.
	// Parameters:
	//   - caller: The account initiating the call
	//   - addr: The contract address to call
	//   - input: The call data (function selector + arguments)
	//   - gas: Gas limit for the call
	//   - value: Ether value to transfer
	//   - bailout: If true, don't revert on insufficient balance (used for gas bailout)
	// Returns:
	//   - ret: Return data from the contract
	//   - leftOverGas: Unused gas
	//   - err: Error if execution failed
	Call(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int, bailout bool) (ret []byte, leftOverGas uint64, err error)

	// CallCode executes a contract's code in the caller's context.
	// Similar to DELEGATECALL but with caller's address as msg.sender.
	CallCode(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int) (ret []byte, leftOverGas uint64, err error)

	// DelegateCall executes a contract's code with the caller's storage and context.
	// msg.sender and msg.value are inherited from the caller.
	DelegateCall(caller ContractRef, addr types.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error)

	// StaticCall executes a read-only contract call.
	// Any state modification will cause the call to fail.
	StaticCall(caller ContractRef, addr types.Address, input []byte, gas uint64) (ret []byte, leftOverGas uint64, err error)

	// Create deploys a new contract.
	// Parameters:
	//   - caller: The account deploying the contract
	//   - code: The contract deployment bytecode (init code)
	//   - gas: Gas limit for deployment
	//   - endowment: Ether value to transfer to the new contract
	// Returns:
	//   - ret: Runtime bytecode (after init code execution)
	//   - contractAddr: The deployed contract's address
	//   - leftOverGas: Unused gas
	//   - err: Error if deployment failed
	Create(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int) (ret []byte, contractAddr types.Address, leftOverGas uint64, err error)

	// Create2 deploys a new contract using CREATE2 opcode.
	// The address is deterministic based on sender, salt, and init code hash.
	Create2(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int, salt *uint256.Int) (ret []byte, contractAddr types.Address, leftOverGas uint64, err error)
}

// VMContext provides read-only access to EVM execution context.
// Use this interface when you only need to query VM state.
type VMContext interface {
	// Context returns the block context
	Context() evmtypes.BlockContext

	// TxContext returns the transaction context
	TxContext() evmtypes.TxContext

	// ChainConfig returns the chain configuration
	ChainConfig() *params.ChainConfig

	// ChainRules returns the active chain rules
	ChainRules() *params.Rules

	// IntraBlockState returns the state accessor
	IntraBlockState() evmtypes.IntraBlockState
}

// VMExecutor combines VM execution with context access.
// This is the full interface for EVM operations.
type VMExecutor interface {
	VMCaller
	VMContext
}

// VMResetter allows resetting VM state between transactions.
type VMResetter interface {
	// Reset resets the VM with a new transaction context
	Reset(txCtx evmtypes.TxContext, ibs evmtypes.IntraBlockState)

	// ResetBetweenBlocks resets the VM for a new block
	ResetBetweenBlocks(blockCtx evmtypes.BlockContext, txCtx evmtypes.TxContext, ibs evmtypes.IntraBlockState, vmConfig Config, chainRules *params.Rules)
}

// VMCanceller allows canceling VM execution.
type VMCanceller interface {
	// Cancel cancels any running EVM operation
	Cancel()

	// Cancelled returns true if Cancel has been called
	Cancelled() bool
}

// FullVM is the complete EVM interface combining all capabilities.
type FullVM interface {
	VMExecutor
	VMResetter
	VMCanceller
}

// =============================================================================
// Compile-time interface compliance checks
// =============================================================================

var (
	_ VMCaller    = (*EVM)(nil)
	_ VMContext   = (*EVM)(nil)
	_ VMExecutor  = (*EVM)(nil)
	_ VMResetter  = (*EVM)(nil)
	_ VMCanceller = (*EVM)(nil)
	_ FullVM      = (*EVM)(nil)
)
