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

// Package state provides interfaces and implementations for blockchain state management.
//
// Core Interfaces:
//   - StateReader: Read-only access to account data, storage, and code
//   - StateWriter: Write access for state modifications
//   - WriterWithChangeSets: StateWriter with change tracking
//   - StateReaderWriter: Combined read/write interface
//
// Implementations:
//   - PlainStateReader: Reads from un-hashed "plain state" storage
//   - PlainStateWriter: Writes to plain state with optional change tracking
//   - HistoryStateReader: Reads historical state at specific block numbers
//   - IntraBlockState: Full state management during block execution
//
// Usage:
//
//	The internal/vm package should use StateReader/StateWriter interfaces
//	(via evmtypes.IntraBlockState) rather than concrete implementations,
//	enabling testability and flexibility.
package state

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// StateReader provides read-only access to blockchain state.
// This interface is used by the EVM and other components that need
// to query account data, storage, and code without modifying state.
//
// Thread Safety: Implementations must be safe for concurrent reads.
// Error Handling: nil return with nil error means the data doesn't exist.
//
// Reth-style note: methods take only (address, slot|codeHash) — no
// incarnation parameter. Phase D removed the IncarnationMap table; Phase E
// removed the dead parameter from these signatures. Storage keys are flat
// (addr||slot, 52B) and bytecode is content-addressed in modules.Code.
type StateReader interface {
	// ReadAccountData returns the account state for the given address.
	// Returns nil, nil if the account doesn't exist.
	ReadAccountData(address types.Address) (*account.StateAccount, error)

	// ReadAccountStorage reads a storage slot from an account.
	// Returns nil, nil if the storage slot is empty or account doesn't exist.
	ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error)

	// ReadAccountCode returns the contract code for an account.
	// Returns nil, nil if the account has no code (EOA or empty codeHash).
	ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error)

	// ReadAccountCodeSize returns the size of the contract code.
	// Returns 0, nil if the account has no code.
	ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error)
}

// StorageEnumerator is an optional capability of a StateReader that can
// enumerate every persisted storage slot of an account (a full prefix scan
// of the plain Storage table). IntraBlockState uses it, at storage-wipe
// registration time (Selfdestruct / contract CreateAccount), to capture the
// COMPLETE pre-block slot set of an account so the pluggable RootComputer
// (JMT/MPT/BMT) can delete all of them from the hashed state — not merely
// the slots that happened to be touched in the current block.
//
// Readers that cannot enumerate (e.g. a pure witness-backed minimal-client
// reader) simply do not implement this interface; the caller then falls back
// to the touched-slot set (obj.blockOriginStorage).
//
// f is called once per slot with the raw stored value bytes; returning false
// stops the iteration early. The scan must reflect state as of the start of
// the block (pre-wipe), which holds because IntraBlockState invokes it during
// EVM execution, before FinalizeTx/CommitBlock flushes any wipe to the DB.
type StorageEnumerator interface {
	ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error
}

// ErrNoStorageEnumeration is returned by a ForEachStorage implementation that
// declares the interface but cannot actually scan right now — typically a
// reader wrapping a bare kv.Getter (no cursors) or delegating to an inner
// reader that is not an enumerator.
//
// It MUST be returned instead of a nil error with zero callbacks: a silent
// empty enumeration is indistinguishable from "this account has no storage",
// which turned the EIP-7610 collision check into a silent no-collision answer
// and skipped the probe fallback that used to cover exactly this case.
var ErrNoStorageEnumeration = errors.New("state: reader cannot enumerate storage")

// StateWriter provides write access to blockchain state.
// This interface is used during block execution to modify state.
//
// Thread Safety: Implementations are NOT required to be thread-safe.
// Callers must ensure proper synchronization.
type StateWriter interface {
	// UpdateAccountData updates the account state.
	// original is the previous state (may be nil for new accounts).
	UpdateAccountData(address types.Address, original, account *account.StateAccount) error

	// UpdateAccountCode stores contract code.
	UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error

	// DeleteAccount removes an account from the state.
	DeleteAccount(address types.Address, original *account.StateAccount) error

	// WriteAccountStorage writes a storage slot.
	// original and value are the old and new values respectively.
	//
	// All four args are passed by value (was: pointer for key/orig/value)
	// because the interface dispatch made the compiler escape any
	// stack-local args via & to the heap — pprof showed this as ~13 % of
	// all heap allocs on long replays. types.Hash and uint256.Int are
	// each 32 B; the 96 B argument copy is far cheaper than three heap
	// allocations + GC scan.
	WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error

	// CreateContract marks an address as a contract (legacy hook used by
	// SELFDESTRUCT to trigger storage-wipe enumeration in the changeset
	// writer; no incarnation is bumped).
	CreateContract(address types.Address) error
}

// WriterWithChangeSets extends StateWriter with change tracking.
// This is used during block execution when we need to record
// all state changes for history/pruning purposes.
type WriterWithChangeSets interface {
	StateWriter

	// WriteChangeSets persists the accumulated change sets to storage.
	WriteChangeSets() error

	// WriteHistory persists historical data (for state history queries).
	WriteHistory() error
}

// StateReaderWriter combines StateReader and StateWriter interfaces.
// Use this when both read and write access is needed.
type StateReaderWriter interface {
	StateReader
	StateWriter
}

// RootComputer computes the state root hash for a set of dirty accounts
// and storage slots. The default implementation uses incremental Keccak;
// when JMT is enabled, a JMT-based implementation replaces it.
type RootComputer interface {
	// ComputeRoot computes the state root from dirty accounts and storage.
	// accounts maps address → current account state (nil means deleted).
	// storage maps address → (slot → value) for dirty storage slots.
	ComputeRoot(accounts map[types.Address]*account.StateAccount, storage map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error)
}

// LtHashRootComputer extends RootComputer with original-data awareness
// for LtHash incremental digest computation.
type LtHashRootComputer interface {
	RootComputer
	ComputeRootWithOriginals(
		accounts map[types.Address]*account.StateAccount,
		originals map[types.Address]*account.StateAccount,
		storage map[types.Address]map[types.Hash]*uint256.Int,
		originalStorage map[types.Address]map[types.Hash]*uint256.Int,
	) (jmtRoot types.Hash, ltHashRoot types.Hash, err error)
}

// Compile-time interface implementation checks
var (
	_ StateReader          = (*PlainStateReader)(nil)
	_ StateReader          = (*HistoryStateReader)(nil)
	_ WriterWithChangeSets = (*PlainStateWriter)(nil)
	_ StateWriter          = (*NoopWriter)(nil)
)
