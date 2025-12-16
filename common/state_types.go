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

package common

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// StateDB is the common layer interface for EVM state database operations.
// The actual implementation (IntraBlockState) is in modules/state.
//
// This interface defines all methods needed by the EVM during block execution.
// It is designed to be the single source of truth for state interface definition,
// with internal/vm/evmtypes.IntraBlockState as a type alias for backward compatibility.
//
// Thread Safety: Implementations are NOT required to be thread-safe.
// Callers must ensure proper synchronization.
type StateDB interface {
	// ========== Account Management ==========

	// CreateAccount creates a new account at the given address.
	// contractCreation indicates if this is for a contract deployment.
	CreateAccount(addr types.Address, contractCreation bool)

	// Exist reports whether the given account exists in state.
	// Notably this should also return true for self-destructed accounts.
	Exist(addr types.Address) bool

	// Empty returns whether the given account is empty.
	// Empty is defined according to EIP-161 (balance = nonce = code = 0).
	Empty(addr types.Address) bool

	// ========== Balance Operations ==========

	// SubBalance subtracts amount from the account balance.
	SubBalance(addr types.Address, amount *uint256.Int)

	// AddBalance adds amount to the account balance.
	AddBalance(addr types.Address, amount *uint256.Int)

	// GetBalance returns the balance of the given address.
	GetBalance(addr types.Address) *uint256.Int

	// ========== Nonce Operations ==========

	// GetNonce returns the nonce of the given address.
	GetNonce(addr types.Address) uint64

	// SetNonce sets the nonce of the given address.
	SetNonce(addr types.Address, nonce uint64)

	// ========== Code Operations ==========

	// GetCodeHash returns the code hash of the given address.
	GetCodeHash(addr types.Address) types.Hash

	// GetCode returns the code of the given address.
	GetCode(addr types.Address) []byte

	// SetCode sets the code of the given address.
	SetCode(addr types.Address, code []byte)

	// GetCodeSize returns the size of the code at the given address.
	GetCodeSize(addr types.Address) int

	// ========== Refund Operations ==========

	// AddRefund adds gas to the refund counter.
	AddRefund(gas uint64)

	// SubRefund removes gas from the refund counter.
	// This method will panic if the refund counter goes below zero.
	SubRefund(gas uint64)

	// GetRefund returns the current value of the refund counter.
	GetRefund() uint64

	// ========== Storage Operations ==========

	// GetCommittedState retrieves a value from the given account's committed storage.
	GetCommittedState(addr types.Address, key *types.Hash, outValue *uint256.Int)

	// GetState retrieves a value from the given account's storage.
	GetState(addr types.Address, key *types.Hash, outValue *uint256.Int)

	// SetState sets a value in the given account's storage.
	SetState(addr types.Address, key *types.Hash, value uint256.Int)

	// ========== Self-destruct Operations ==========

	// Selfdestruct marks the given account as self-destructed.
	// This clears the account balance and marks it for deletion at the end of the transaction.
	Selfdestruct(addr types.Address) bool

	// HasSelfdestructed returns whether the account has been self-destructed.
	HasSelfdestructed(addr types.Address) bool

	// ========== Access List (EIP-2930) ==========

	// PrepareAccessList prepares the access list for a transaction.
	PrepareAccessList(sender types.Address, dest *types.Address, precompiles []types.Address, txAccesses transaction.AccessList)

	// AddressInAccessList returns whether the address is in the access list.
	AddressInAccessList(addr types.Address) bool

	// SlotInAccessList returns whether the (address, slot) pair is in the access list.
	SlotInAccessList(addr types.Address, slot types.Hash) (addressOk bool, slotOk bool)

	// AddAddressToAccessList adds the given address to the access list.
	// This operation is safe to perform even if the feature/fork is not active yet.
	AddAddressToAccessList(addr types.Address)

	// AddSlotToAccessList adds the given (address, slot) to the access list.
	// This operation is safe to perform even if the feature/fork is not active yet.
	AddSlotToAccessList(addr types.Address, slot types.Hash)

	// ========== Snapshot/Revert ==========

	// Snapshot returns an identifier for the current revision of the state.
	Snapshot() int

	// RevertToSnapshot reverts all state changes made since the given revision.
	RevertToSnapshot(revisionID int)

	// ========== Logging ==========

	// AddLog adds a log entry to the state.
	AddLog(log *block.Log)

	// ========== Transient Storage (EIP-1153) ==========

	// GetTransientState gets a value from transient storage.
	// Transient storage is cleared at the end of each transaction.
	GetTransientState(addr types.Address, key types.Hash) uint256.Int

	// SetTransientState sets a value in transient storage.
	// Transient storage is cleared at the end of each transaction.
	SetTransientState(addr types.Address, key types.Hash, value uint256.Int)
}
