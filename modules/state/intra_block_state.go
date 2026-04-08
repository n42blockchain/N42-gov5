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

// Package state provides a caching layer atop the Ethereum state trie.
package state

import (
	"fmt"
	"sort"
	"unsafe"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// Compile-time check: IntraBlockState must implement common.StateDB
// This ensures that IntraBlockState has all methods required by the EVM.
var _ common.StateDB = (*IntraBlockState)(nil)

type revision struct {
	id           int
	journalIndex int
}

type StateTracer interface {
	CaptureAccountRead(account types.Address) error
	CaptureAccountWrite(account types.Address) error
}

// SystemAddress - sender address for internal state updates.
var SystemAddress = types.HexToAddress("0xfffffffffffffffffffffffffffffffffffffffe")

// BalanceIncrease represents the increase of balance of an account that did not require
// reading the account first
type BalanceIncrease struct {
	increase    uint256.Int
	transferred bool // Set to true when the corresponding stateObject is created and balance increase is transferred to the stateObject
	count       int  // Number of increases - this needs tracking for proper reversion
}

// IntraBlockState is responsible for caching and managing state changes
// that occur during block's execution.
// NOT THREAD SAFE!
type IntraBlockState struct {
	stateReader StateReader

	// This map holds 'live' objects, which will get modified while processing a state transition.
	stateObjects      map[types.Address]*stateObject
	stateObjectsDirty map[types.Address]struct{}

	nilAccounts map[types.Address]struct{} // Remember non-existent account to avoid reading them again

	// DB error.
	// State objects are used by the consensus core and VM which are
	// unable to deal with database-level errors. Any error that occurs
	// during a database read is memoized here and will eventually be returned
	// by IntraBlockState.Commit.
	savedErr error

	// The refund counter, also used by state transitioning.
	refund uint64

	thash, bhash types.Hash
	txIndex      int
	logs         map[types.Hash][]*block.Log
	logSize      uint

	// Journal of state modifications. This is the backbone of
	// Snapshot and RevertToSnapshot.
	journal        *journal
	validRevisions []revision
	nextRevisionID int
	tracer         StateTracer
	trace          bool
	accessList     *accessList
	balanceInc     map[types.Address]*BalanceIncrease // Map of balance increases (without first reading the account)

	snap    *Snapshot
	codeMap map[types.Hash][]byte
	height  uint64

	// EIP-1153: Transient storage
	transientStorage transientStorage

	// rootComputer is an optional pluggable state root implementation.
	// When nil, the default incremental Keccak hash is used.
	rootComputer RootComputer

	// ltHashRoot stores the LtHash digest root computed during IntermediateRoot().
	// Zero if LtHash is not active.
	ltHashRoot types.Hash
}

// New creates a new IntraBlockState with the given state reader.
func New(stateReader StateReader) *IntraBlockState {
	return &IntraBlockState{
		stateReader:       stateReader,
		stateObjects:      map[types.Address]*stateObject{},
		stateObjectsDirty: map[types.Address]struct{}{},
		nilAccounts:       map[types.Address]struct{}{},
		logs:              map[types.Hash][]*block.Log{},
		journal:           newJournal(),
		accessList:        newAccessList(),
		balanceInc:        map[types.Address]*BalanceIncrease{},
		transientStorage:  newTransientStorage(),
	}
}

// SetRootComputer sets a custom state root implementation (e.g., JMT).
// When set, IntermediateRoot() delegates to this instead of the default Keccak hash.
// If the RootComputer implements LtHashRootComputer, the LtHash digest is
// also updated incrementally and available via LtHashRoot().
func (sdb *IntraBlockState) SetRootComputer(rc RootComputer) {
	sdb.rootComputer = rc
}

func (sdb *IntraBlockState) HasRootComputer() bool {
	return sdb.rootComputer != nil
}

func (sdb *IntraBlockState) GetRootComputer() RootComputer {
	return sdb.rootComputer
}

// LtHashRoot returns the LtHash state digest root, computed during
// IntermediateRoot() if the RootComputer supports LtHash.
// Returns zero hash if LtHash is not active.
func (sdb *IntraBlockState) LtHashRoot() types.Hash {
	return sdb.ltHashRoot
}

func (sdb *IntraBlockState) BeginWriteSnapshot() {
	sdb.snap = NewWritableSnapshot()
}

func (sdb *IntraBlockState) BeginWriteCodes() {
	sdb.codeMap = make(map[types.Hash][]byte)
}

// CodeHashes returns a map of code hashes to code. Must be called at the end of block execution.
func (sdb *IntraBlockState) CodeHashes() map[types.Hash][]byte {
	for addr, stateObject := range sdb.stateObjects {
		_, isDirty := sdb.stateObjectsDirty[addr]
		if isDirty && !stateObject.deleted && !stateObject.selfdestructed && stateObject.code != nil && stateObject.dirtyCode {
			h := types.BytesToHash(stateObject.CodeHash())
			sdb.codeMap[h] = stateObject.code
		}
	}
	return sdb.codeMap
}

func (sdb *IntraBlockState) SetHeight(height uint64) {
	sdb.height = height
}

func (sdb *IntraBlockState) Snap() *Snapshot {
	return sdb.snap
}

func (sdb *IntraBlockState) SetOutHash(hash types.Hash) {
	if sdb.snap == nil || !sdb.snap.CanWrite() {
		return
	}
	sdb.snap.OutHash = hash
}

func (sdb *IntraBlockState) WrittenSnapshot(hash types.Hash) []byte {
	if sdb.snap == nil || !sdb.snap.CanWrite() {
		return nil
	}
	if sdb.snap.Items != nil {
		sort.Sort(sdb.snap.Items)
	}
	sdb.snap.OutHash = hash
	b, err := rlp.EncodeToBytes(sdb.snap)
	if err != nil {
		return nil
	}
	sdb.snap = nil
	return b
}

func (s *IntraBlockState) SetGetOneFun(f1 GetOneFun) {
	if s.snap == nil {
		return
	}
	s.snap.SetGetFun(f1)
}

func (sdb *IntraBlockState) PrepareReadableSnapshot(data []byte) error {
	snap, err := ReadSnapshotData(data)
	if err != nil {
		return err
	}
	sdb.snap = snap
	return nil
}

func (sdb *IntraBlockState) SetSnapshot(snap *Snapshot) {
	snap.accounts = make(map[string]int, len(snap.Items))
	snap.storage = make(map[string]int, len(snap.Items))
	for k, v := range snap.Items {
		if len(v.Key) == types.AddressLength {
			snap.accounts[*(*string)(unsafe.Pointer(&v.Key))] = k
		} else {
			snap.storage[*(*string)(unsafe.Pointer(&v.Key))] = k
		}
	}
	sdb.snap = snap
}

func (sdb *IntraBlockState) SetTracer(tracer StateTracer) {
	sdb.tracer = tracer
}

func (sdb *IntraBlockState) SetTrace(trace bool) {
	sdb.trace = trace
}

// traceAccountRead logs a tracer read event for the given address, if a tracer is set.
func (sdb *IntraBlockState) traceAccountRead(addr types.Address) {
	if sdb.tracer != nil {
		if err := sdb.tracer.CaptureAccountRead(addr); err != nil && sdb.trace {
			log.Trace("CaptureAccountRead", "addr", addr, "err", err)
		}
	}
}

// traceAccountWrite logs a tracer write event for the given address, if a tracer is set.
func (sdb *IntraBlockState) traceAccountWrite(addr types.Address) {
	if sdb.tracer != nil {
		if err := sdb.tracer.CaptureAccountWrite(addr); err != nil && sdb.trace {
			log.Trace("CaptureAccountWrite", "addr", addr, "err", err)
		}
	}
}

// setErrorUnsafe sets error but should be called in methods that already have locks.
func (sdb *IntraBlockState) setErrorUnsafe(err error) {
	if sdb.savedErr == nil {
		sdb.savedErr = err
	}
}

func (sdb *IntraBlockState) Error() error {
	return sdb.savedErr
}

func (sdb *IntraBlockState) SetStateReader(reader StateReader) {
	sdb.stateReader = reader
}

func (sdb *IntraBlockState) GetStateReader() StateReader {
	return sdb.stateReader
}

// Reset clears out all ephemeral state objects from the state db, but keeps
// the underlying state trie to avoid reloading data for the next operations.
func (sdb *IntraBlockState) Reset() {
	sdb.stateObjects = make(map[types.Address]*stateObject)
	sdb.stateObjectsDirty = make(map[types.Address]struct{})
	sdb.nilAccounts = make(map[types.Address]struct{})
	sdb.thash = types.Hash{}
	sdb.bhash = types.Hash{}
	sdb.txIndex = 0
	sdb.logs = make(map[types.Hash][]*block.Log)
	sdb.logSize = 0
	sdb.clearJournalAndRefund()
	sdb.accessList = newAccessList()
	sdb.balanceInc = make(map[types.Address]*BalanceIncrease)
}

func (sdb *IntraBlockState) AddLog(log2 *block.Log) {
	sdb.journal.append(addLogChange{txhash: sdb.thash})
	log2.TxHash = sdb.thash
	log2.BlockHash = sdb.bhash
	log2.TxIndex = uint(sdb.txIndex)
	log2.Index = sdb.logSize
	sdb.logs[sdb.thash] = append(sdb.logs[sdb.thash], log2)
	sdb.logSize++
}

func (sdb *IntraBlockState) GetLogs(hash types.Hash) []*block.Log {
	return sdb.logs[hash]
}

func (sdb *IntraBlockState) Logs() []*block.Log {
	var logs []*block.Log
	for _, lgs := range sdb.logs {
		logs = append(logs, lgs...)
	}
	return logs
}

// GetTransientState gets a value from transient storage (EIP-1153).
func (sdb *IntraBlockState) GetTransientState(addr types.Address, key types.Hash) uint256.Int {
	return sdb.transientStorage.Get(addr, key)
}

// SetTransientState sets a value in transient storage (EIP-1153).
func (sdb *IntraBlockState) SetTransientState(addr types.Address, key types.Hash, value uint256.Int) {
	prev := sdb.transientStorage.Get(addr, key)
	if prev == value {
		return
	}
	sdb.journal.append(transientStorageChange{
		account:  &addr,
		key:      key,
		prevalue: prev,
	})
	sdb.transientStorage.Set(addr, key, value)
}

// AddRefund adds gas to the refund counter
func (sdb *IntraBlockState) AddRefund(gas uint64) {
	sdb.journal.append(refundChange{prev: sdb.refund})
	sdb.refund += gas
}

// SubRefund removes gas from the refund counter.
// This method will set an error if the refund counter goes below zero
func (sdb *IntraBlockState) SubRefund(gas uint64) {
	if gas > sdb.refund {
		sdb.setErrorUnsafe(fmt.Errorf("refund counter below zero: gas=%d, refund=%d", gas, sdb.refund))
		return
	}
	sdb.journal.append(refundChange{prev: sdb.refund})
	sdb.refund -= gas
}

// Exist reports whether the given account address exists in the state.
// Notably this also returns true for suicided accounts.
func (sdb *IntraBlockState) Exist(addr types.Address) bool {
	sdb.traceAccountRead(addr)
	s := sdb.getStateObject(addr)
	return s != nil && !s.deleted
}

// ExistPure checks if an address exists WITHOUT materializing balanceInc
// entries into stateObjects. This avoids side effects (journal entries)
// that can escape EVM snapshot/revert boundaries.
// Used by gas cost calculations which must be side-effect-free.
func (sdb *IntraBlockState) ExistPure(addr types.Address) bool {
	// Check live objects.
	if obj := sdb.stateObjects[addr]; obj != nil {
		return !obj.deleted
	}
	// Check nilAccounts cache (known non-existent).
	if _, ok := sdb.nilAccounts[addr]; ok {
		// Unlike getStateObject, do NOT materialize balanceInc here.
		// A pending balance increment does not constitute existence
		// for gas calculation purposes (geth parity).
		return false
	}
	// Read from DB.
	account, err := sdb.stateReader.ReadAccountData(addr)
	if err != nil {
		sdb.setErrorUnsafe(err)
		return false
	}
	if account == nil {
		sdb.nilAccounts[addr] = struct{}{}
		return false
	}
	return true
}

// Empty returns whether the state object is either non-existent
// or empty according to the EIP161 specification (balance = nonce = code = 0)
func (sdb *IntraBlockState) Empty(addr types.Address) bool {
	sdb.traceAccountRead(addr)
	so := sdb.getStateObject(addr)
	return so == nil || so.deleted || so.empty()
}

// HasNonEmptyStorage returns true if the account has any non-zero storage entries.
// This is used by EIP-7610 style collision detection to prevent deploying code
// at addresses that have pre-existing storage.
// NOTE: This checks in-memory caches and also probes common storage slots (0x00, 0x01)
// to detect pre-existing storage that hasn't been loaded yet.
func (sdb *IntraBlockState) HasNonEmptyStorage(addr types.Address) bool {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return false
	}
	// Check dirtyStorage (current transaction writes)
	for _, value := range stateObject.dirtyStorage {
		if !value.IsZero() {
			return true
		}
	}
	// Check originStorage (values loaded from DB during this block)
	for _, value := range stateObject.originStorage {
		if !value.IsZero() {
			return true
		}
	}
	// Check blockOriginStorage (values from start of block)
	for _, value := range stateObject.blockOriginStorage {
		if !value.IsZero() {
			return true
		}
	}
	// Probe common storage slots to detect pre-existing storage from database
	// This handles the case where storage exists but hasn't been loaded yet.
	// Slots 0x00 and 0x01 are commonly used for mappings and state variables.
	slot0 := types.Hash{}
	slot1 := types.Hash{}
	slot1[31] = 1 // 0x01
	var value uint256.Int
	stateObject.GetCommittedState(&slot0, &value)
	if !value.IsZero() {
		return true
	}
	stateObject.GetCommittedState(&slot1, &value)
	if !value.IsZero() {
		return true
	}
	return false
}

// GetBalance retrieves the balance from the given address or 0 if object not found.
func (sdb *IntraBlockState) GetBalance(addr types.Address) *uint256.Int {
	sdb.traceAccountRead(addr)
	stateObject := sdb.getStateObject(addr)
	if stateObject != nil && !stateObject.deleted {
		return stateObject.Balance()
	}
	return uint256.NewInt(0)
}

// GetNonce returns the nonce for the given address.
func (sdb *IntraBlockState) GetNonce(addr types.Address) uint64 {
	sdb.traceAccountRead(addr)
	stateObject := sdb.getStateObject(addr)
	if stateObject != nil && !stateObject.deleted {
		return stateObject.Nonce()
	}

	return 0
}

// TxIndex returns the current transaction index set by Prepare.
func (sdb *IntraBlockState) TxIndex() int {
	return sdb.txIndex
}

// GetCode returns the contract code for the given address.
func (sdb *IntraBlockState) GetCode(addr types.Address) []byte {
	sdb.traceAccountRead(addr)
	stateObject := sdb.getStateObject(addr)
	if stateObject != nil && !stateObject.deleted {
		if sdb.trace {
			fmt.Printf("GetCode %x, returned %d\n", addr, len(stateObject.Code()))
		}
		code := stateObject.Code()
		if len(code) > 0 && sdb.codeMap != nil {
			sdb.codeMap[types.BytesToHash(stateObject.CodeHash())] = code
		}
		return code
	}
	if sdb.trace {
		fmt.Printf("GetCode %x, returned nil\n", addr)
	}
	return nil
}

// GetCodeSize returns the size of the contract code for the given address.
func (sdb *IntraBlockState) GetCodeSize(addr types.Address) int {
	sdb.traceAccountRead(addr)
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return 0
	}
	if stateObject.code != nil {
		return len(stateObject.code)
	}
	codeSize, err := sdb.stateReader.ReadAccountCodeSize(addr, 0, stateObject.data.CodeHash)
	if err != nil {
		sdb.setErrorUnsafe(err)
	}
	if codeSize > 0 && sdb.codeMap != nil {
		codeHash := types.BytesToHash(stateObject.CodeHash())
		code, _ := sdb.stateReader.ReadAccountCode(addr, 0, codeHash)
		sdb.codeMap[codeHash] = code
	}
	return codeSize
}

// GetCodeHash returns the code hash for the given address.
func (sdb *IntraBlockState) GetCodeHash(addr types.Address) types.Hash {
	sdb.traceAccountRead(addr)
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return types.Hash{}
	}
	return types.BytesToHash(stateObject.CodeHash())
}

// GetState retrieves a value from the given account's storage trie.
func (sdb *IntraBlockState) GetState(addr types.Address, key *types.Hash, value *uint256.Int) {
	stateObject := sdb.getStateObject(addr)
	if stateObject != nil && !stateObject.deleted {
		stateObject.GetState(key, value)
	} else {
		value.Clear()
	}
}

// GetCommittedState retrieves a value from the given account's committed storage trie.
func (sdb *IntraBlockState) GetCommittedState(addr types.Address, key *types.Hash, value *uint256.Int) {
	stateObject := sdb.getStateObject(addr)
	if stateObject != nil && !stateObject.deleted {
		stateObject.GetCommittedState(key, value)
	} else {
		value.Clear()
	}
}

// HasSuicided is a legacy alias for HasSelfdestructed.
// Retained for compatibility with the common.StateDB interface.
func (sdb *IntraBlockState) HasSuicided(addr types.Address) bool {
	return sdb.HasSelfdestructed(addr)
}

// --- Setters ---

// AddBalance adds amount to the account associated with addr.
func (sdb *IntraBlockState) AddBalance(addr types.Address, amount *uint256.Int) {
	if sdb.trace {
		fmt.Printf("AddBalance %x, %d\n", addr, amount)
	}
	sdb.traceAccountWrite(addr)
	// If this account has not been read, add to the balance increment map
	_, needAccount := sdb.stateObjects[addr]
	if !needAccount && addr == ripemd && amount.IsZero() {
		needAccount = true
	}
	if !needAccount {
		sdb.journal.append(balanceIncrease{
			account:  &addr,
			increase: *amount,
		})
		bi, ok := sdb.balanceInc[addr]
		if !ok {
			bi = &BalanceIncrease{}
			sdb.balanceInc[addr] = bi
		}
		bi.increase.Add(&bi.increase, amount)
		bi.count++
		return
	}

	stateObject := sdb.GetOrNewStateObject(addr)
	stateObject.AddBalance(amount)
}

// SubBalance subtracts amount from the account associated with addr.
func (sdb *IntraBlockState) SubBalance(addr types.Address, amount *uint256.Int) {
	if sdb.trace {
		fmt.Printf("SubBalance %x, %d\n", addr, amount)
	}
	sdb.traceAccountWrite(addr)
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SubBalance(amount)
	}
}

// SetBalance sets the balance for the given address.
func (sdb *IntraBlockState) SetBalance(addr types.Address, amount *uint256.Int) {
	sdb.traceAccountWrite(addr)
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetBalance(amount)
	}
}

// SetNonce sets the nonce for the given address.
func (sdb *IntraBlockState) SetNonce(addr types.Address, nonce uint64) {
	sdb.traceAccountWrite(addr)
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetNonce(nonce)
	}
}

// SetCode sets the contract code for the given address.
func (sdb *IntraBlockState) SetCode(addr types.Address, code []byte) {
	sdb.traceAccountWrite(addr)
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetCode(crypto.Keccak256Hash(code), code)
	}
}

// SetState sets a storage value for the given address.
func (sdb *IntraBlockState) SetState(addr types.Address, key *types.Hash, value uint256.Int) {
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetState(key, value)
	}
}

// SetStorage replaces the entire storage for the specified account with given
// storage. This function should only be used for debugging.
func (sdb *IntraBlockState) SetStorage(addr types.Address, storage Storage) {
	stateObject := sdb.GetOrNewStateObject(addr)
	if stateObject != nil {
		stateObject.SetStorage(storage)
	}
}

// SetIncarnation is a no-op. Incarnation has been removed from StateAccount.
func (sdb *IntraBlockState) SetIncarnation(addr types.Address, incarnation uint16) {
}

// GetIncarnation always returns 0. Incarnation has been removed from StateAccount.
func (sdb *IntraBlockState) GetIncarnation(addr types.Address) uint16 {
	return 0
}

// Suicide is a legacy alias for Selfdestruct.
// Retained for compatibility with the common.StateDB interface.
func (sdb *IntraBlockState) Suicide(addr types.Address) bool {
	return sdb.Selfdestruct(addr)
}

func (sdb *IntraBlockState) getStateObject(addr types.Address) (stateObject *stateObject) {
	// Prefer 'live' objects.
	if obj := sdb.stateObjects[addr]; obj != nil {
		return obj
	}

	// Load the object from the database.
	if _, ok := sdb.nilAccounts[addr]; ok {
		if bi, ok := sdb.balanceInc[addr]; ok && !bi.transferred {
			return sdb.createObject(addr, nil)
		}
		return nil
	}
	account, err := sdb.stateReader.ReadAccountData(addr)
	if err != nil {
		sdb.setErrorUnsafe(err)
		return nil
	}
	if account == nil {
		sdb.nilAccounts[addr] = struct{}{}
		if bi, ok := sdb.balanceInc[addr]; ok && !bi.transferred {
			return sdb.createObject(addr, nil)
		}
		return nil
	}

	// snap write
	if sdb.snap != nil && sdb.snap.CanWrite() {
		sdb.snap.AddAccount(addr, account)
	}

	// Insert into the live set.
	obj := newObject(sdb, addr, account, account)
	sdb.setStateObject(addr, obj)
	return obj
}

func (sdb *IntraBlockState) setStateObject(addr types.Address, object *stateObject) {
	if bi, ok := sdb.balanceInc[addr]; ok && !bi.transferred {
		object.data.Balance.Add(&object.data.Balance, &bi.increase)
		bi.transferred = true
		sdb.journal.append(balanceIncreaseTransfer{bi: bi})
	}
	sdb.stateObjects[addr] = object
}

// Retrieve a state object or create a new state object if nil.
func (sdb *IntraBlockState) GetOrNewStateObject(addr types.Address) *stateObject {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		stateObject = sdb.createObject(addr, stateObject /* previous */)
	}
	return stateObject
}

// createObject creates a new state object. If there is an existing account with
// the given address, it is overwritten.
func (sdb *IntraBlockState) createObject(addr types.Address, previous *stateObject) (newobj *stateObject) {
	ac := new(account.StateAccount)
	var original *account.StateAccount
	if previous == nil {
		original = &account.StateAccount{}
	} else {
		original = &previous.original
	}
	newobj = newObject(sdb, addr, ac, original)
	newobj.setNonce(0) // sets the object to dirty
	// Inherit block-level flags from previous object — these survive
	// clearCurrentTxFlags and are needed by CommitBlock's updateAccount.
	if previous != nil {
		if previous.selfdestructedInBlock {
			newobj.selfdestructedInBlock = true
		}
	}
	if previous == nil {
		sdb.journal.append(createObjectChange{account: &addr})
	} else {
		sdb.journal.append(resetObjectChange{account: &addr, prev: previous})
	}
	sdb.setStateObject(addr, newobj)
	return newobj
}

// CreateAccount explicitly creates a state object. If a state object with the address
// already exists the balance is carried over to the new account.
//
// CreateAccount is called during the EVM CREATE operation. The situation might arise that
// a contract does the following:
//
//  1. sends funds to sha(account ++ (nonce + 1))
//  2. tx_create(sha(account ++ nonce)) (note that this gets the address of 1)
//
// Carrying over the balance ensures that N doesn't disappear.
func (sdb *IntraBlockState) CreateAccount(addr types.Address, contractCreation bool) {
	sdb.traceAccountRead(addr)
	sdb.traceAccountWrite(addr)

	previous := sdb.getStateObject(addr)
	// incarnation removed — storage wipe handled separately

	newObj := sdb.createObject(addr, previous)
	if previous != nil {
		newObj.data.Balance.Set(&previous.data.Balance)
		newObj.data.Initialised = true
	}

	if contractCreation {
		newObj.created = true
		newObj.createdInBlock = true
	} else {
		newObj.selfdestructed = false
		// Non-contract CreateAccount: if previous was selfdestructed, we need
		// to wipe its storage. Mark createdInBlock to trigger wipe in CommitBlock,
		// but clear selfdestructedInBlock so the new empty account gets written back.
		if previous != nil && previous.selfdestructedInBlock {
			newObj.createdInBlock = true
		}
		newObj.selfdestructedInBlock = false
	}
}

// Snapshot returns an identifier for the current revision of the state.
func (sdb *IntraBlockState) Snapshot() int {
	id := sdb.nextRevisionID
	sdb.nextRevisionID++
	sdb.validRevisions = append(sdb.validRevisions, revision{id, sdb.journal.length()})
	return id
}

// RevertToSnapshot reverts all state changes made since the given revision.
func (sdb *IntraBlockState) RevertToSnapshot(revid int) {
	// Find the snapshot in the stack of valid snapshots.
	idx := sort.Search(len(sdb.validRevisions), func(i int) bool {
		return sdb.validRevisions[i].id >= revid
	})
	if idx == len(sdb.validRevisions) || sdb.validRevisions[idx].id != revid {
		panic(fmt.Errorf("revision id %v cannot be reverted", revid))
	}
	snapshot := sdb.validRevisions[idx].journalIndex

	// Replay the journal to undo changes and remove invalidated snapshots
	sdb.journal.revert(sdb, snapshot)
	sdb.validRevisions = sdb.validRevisions[:idx]
}

// GetRefund returns the current value of the refund counter.
func (sdb *IntraBlockState) GetRefund() uint64 {
	return sdb.refund
}

type accountWritePolicy struct {
	removeEmptyAccounts                  bool
	preserveAuraSystemAccount            bool
	allowLegacyCreatedSelfdestructReplay bool
}

func newAccountWritePolicy(chainRules *params.Rules) accountWritePolicy {
	if chainRules == nil {
		return accountWritePolicy{}
	}
	return accountWritePolicy{
		removeEmptyAccounts:                  chainRules.IsSpuriousDragon,
		preserveAuraSystemAccount:            chainRules.IsAura,
		allowLegacyCreatedSelfdestructReplay: !chainRules.IsCancun,
	}
}

func (p accountWritePolicy) shouldRemoveEmptyAccount(addr types.Address, stateObject *stateObject) bool {
	return p.removeEmptyAccounts && stateObject.empty() && (!p.preserveAuraSystemAccount || addr != SystemAddress)
}

func (p accountWritePolicy) shouldAllowWriteBack(stateObject *stateObject) bool {
	if stateObject.selfdestructed {
		// Selfdestructed accounts must not be written back.
		// Pre-Cancun allowed created+selfdestructed replay, but this caused
		// phantom empty accounts with incarnation to persist in DB, breaking
		// Exist() parity with geth for DAO-era blocks.
		return false
	}
	return true
}

func updateAccount(policy accountWritePolicy, stateWriter StateWriter, addr types.Address, stateObject *stateObject, isDirty bool) error {
	return updateAccountEx(policy, stateWriter, addr, stateObject, isDirty, false)
}

func updateAccountEx(policy accountWritePolicy, stateWriter StateWriter, addr types.Address, stateObject *stateObject, isDirty bool, useBlockFlags bool) error {
	emptyRemoval := policy.shouldRemoveEmptyAccount(addr, stateObject)
	// useBlockFlags=true (CommitBlock): check selfdestructedInBlock (survives clearCurrentTxFlags).
	// useBlockFlags=false (FinalizeTx): only check selfdestructed (current tx flag).
	shouldDelete := stateObject.selfdestructed || (isDirty && emptyRemoval)
	if useBlockFlags && stateObject.selfdestructedInBlock {
		shouldDelete = true
	}
	recreated := useBlockFlags && stateObject.selfdestructedInBlock && stateObject.createdInBlock
	if shouldDelete && !recreated {
		if err := stateWriter.DeleteAccount(addr, &stateObject.original); err != nil {
			return err
		}
		if err := stateWriter.CreateContract(addr); err != nil {
			return err
		}
		stateObject.deleted = true
	} else if useBlockFlags && (recreated || stateObject.createdInBlock) {
		// Wipe old storage but don't delete — account was recreated.
		if err := stateWriter.CreateContract(addr); err != nil {
			return err
		}
	}
	allowWriteBack := policy.shouldAllowWriteBack(stateObject)
	if useBlockFlags && stateObject.selfdestructedInBlock && !recreated {
		allowWriteBack = false
	}
	if isDirty && allowWriteBack && !emptyRemoval {
		stateObject.deleted = false
		// Write any contract code associated with the state object
		if stateObject.code != nil && stateObject.dirtyCode {
			if err := stateWriter.UpdateAccountCode(addr, 0, stateObject.data.CodeHash, stateObject.code); err != nil {
				return err
			}
		}
		if stateObject.created || stateObject.createdInBlock {
			if err := stateWriter.CreateContract(addr); err != nil {
				return err
			}
		}
		if err := stateObject.updateTrie(stateWriter); err != nil {
			return err
		}
		if err := stateWriter.UpdateAccountData(addr, &stateObject.original, &stateObject.data); err != nil {
			return err
		}
	}
	return nil
}

func printAccount(addr types.Address, stateObject *stateObject, isDirty bool) {
	emptyRemoval := stateObject.empty()
	if stateObject.selfdestructed || (isDirty && emptyRemoval) {
		fmt.Printf("delete: %x\n", addr)
	}
	if isDirty && !stateObject.deleted && !stateObject.selfdestructed && !emptyRemoval {
		// Write any contract code associated with the state object
		if stateObject.code != nil && stateObject.dirtyCode {
			fmt.Printf("UpdateCode: %x,%x\n", addr, stateObject.CodeHash())
		}
		if stateObject.created {
			fmt.Printf("CreateContract: %x\n", addr)
		}
		stateObject.printTrie()
		if stateObject.data.Balance.IsUint64() {
			fmt.Printf("UpdateAccountData: %x, balance=%d, nonce=%d\n", addr, stateObject.data.Balance.Uint64(), stateObject.data.Nonce)
		} else {
			div := uint256.NewInt(1_000_000_000)
			fmt.Printf("UpdateAccountData: %x, balance=%d*%d, nonce=%d\n", addr, uint256.NewInt(0).Div(&stateObject.data.Balance, div).Uint64(), div.Uint64(), stateObject.data.Nonce)
		}
	}
}

// FinalizeTx should be called after every transaction.
func (sdb *IntraBlockState) FinalizeTx(chainRules *params.Rules, stateWriter StateWriter) error {
	policy := newAccountWritePolicy(chainRules)
	for addr, bi := range sdb.balanceInc {
		if !bi.transferred {
			sdb.getStateObject(addr)
		}
	}
	for addr := range sdb.journal.dirties {
		so, exist := sdb.stateObjects[addr]
		if !exist {
			continue
		}

		// Temporarily nil dirtyStorage to prevent updateTrie(noop) from
		// polluting originStorage — this would corrupt cross-tx gas calc
		// and state writes via GetCommittedState.
		savedDirty := so.dirtyStorage
		so.dirtyStorage = make(Storage)
		if err := updateAccount(policy, stateWriter, addr, so, true); err != nil {
			so.dirtyStorage = savedDirty
			return err
		}
		so.dirtyStorage = savedDirty

		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	sdb.clearCurrentTxFlags()
	sdb.clearJournalAndRefund()
	return nil
}

func (sdb *IntraBlockState) SoftFinalise() {
	for addr := range sdb.journal.dirties {
		if _, exist := sdb.stateObjects[addr]; !exist {
			continue
		}
		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	sdb.clearCurrentTxFlags()
	sdb.clearJournalAndRefund()
}

// CommitBlock finalizes the state by removing the self destructed objects
// and clears the journal as well as the refunds.
func (sdb *IntraBlockState) CommitBlock(chainRules *params.Rules, stateWriter StateWriter) error {
	for addr, bi := range sdb.balanceInc {
		if !bi.transferred {
			sdb.getStateObject(addr)
		}
	}
	return sdb.MakeWriteSet(chainRules, stateWriter)
}

func (sdb *IntraBlockState) BalanceIncreaseSet() map[types.Address]uint256.Int {
	s := map[types.Address]uint256.Int{}
	for addr, bi := range sdb.balanceInc {
		if !bi.transferred {
			s[addr] = bi.increase
		}
	}
	return s
}

func (sdb *IntraBlockState) MakeWriteSet(chainRules *params.Rules, stateWriter StateWriter) error {
	policy := newAccountWritePolicy(chainRules)
	for addr := range sdb.journal.dirties {
		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	for addr, stateObject := range sdb.stateObjects {
		_, isDirty := sdb.stateObjectsDirty[addr]
		if err := updateAccountEx(policy, stateWriter, addr, stateObject, isDirty, true); err != nil {
			return err
		}
	}
	// Invalidate journal because reverting across transactions is not allowed.
	sdb.clearJournalAndRefund()
	return nil
}

func (sdb *IntraBlockState) Print() {
	for addr, stateObject := range sdb.stateObjects {
		_, isDirty := sdb.stateObjectsDirty[addr]
		_, isDirty2 := sdb.journal.dirties[addr]

		printAccount(addr, stateObject, isDirty || isDirty2)
	}
}

// Prepare sets the current transaction hash and index and block hash which is
// used when the EVM emits new state logs.
func (sdb *IntraBlockState) Prepare(thash, bhash types.Hash, ti int) {
	sdb.thash = thash
	sdb.bhash = bhash
	sdb.txIndex = ti
	sdb.accessList = newAccessList()
}

// clearJournalAndRefund resets per-transaction ephemeral state.
func (sdb *IntraBlockState) clearJournalAndRefund() {
	sdb.journal = newJournal()
	sdb.validRevisions = sdb.validRevisions[:0]
	sdb.refund = 0
	sdb.transientStorage = newTransientStorage()
}

// clearCurrentTxFlags resets per-transaction account lifecycle markers once the
// current transaction has been finalized.
func (sdb *IntraBlockState) clearCurrentTxFlags() {
	for addr := range sdb.journal.dirties {
		so, ok := sdb.stateObjects[addr]
		if !ok || so.deleted {
			continue
		}
		so.created = false
		so.selfdestructed = false
	}
}

// PrepareAccessList handles the preparatory steps for executing a state transition with
// regards to both EIP-2929 and EIP-2930:
//
// - Add sender to access list (2929)
// - Add destination to access list (2929)
// - Add precompiles to access list (2929)
// - Add the contents of the optional tx access list (2930)
//
// This method should only be called if Yolov3/Berlin/2929+2930 is applicable at the current number.
func (sdb *IntraBlockState) PrepareAccessList(sender types.Address, dst *types.Address, precompiles []types.Address, list transaction.AccessList) {
	sdb.AddAddressToAccessList(sender)
	if dst != nil {
		sdb.AddAddressToAccessList(*dst)
	}
	for _, addr := range precompiles {
		sdb.AddAddressToAccessList(addr)
	}
	for _, el := range list {
		sdb.AddAddressToAccessList(el.Address)
		for _, key := range el.StorageKeys {
			sdb.AddSlotToAccessList(el.Address, key)
		}
	}
}

// AddAddressToAccessList adds the given address to the access list.
func (sdb *IntraBlockState) AddAddressToAccessList(addr types.Address) {
	if sdb.accessList.AddAddress(addr) {
		sdb.journal.append(accessListAddAccountChange{&addr})
	}
}

// AddSlotToAccessList adds the given (address, slot)-tuple to the access list.
func (sdb *IntraBlockState) AddSlotToAccessList(addr types.Address, slot types.Hash) {
	addrMod, slotMod := sdb.accessList.AddSlot(addr, slot)
	if addrMod {
		sdb.journal.append(accessListAddAccountChange{&addr})
	}
	if slotMod {
		sdb.journal.append(accessListAddSlotChange{
			address: &addr,
			slot:    &slot,
		})
	}
}

// AddressInAccessList returns true if the given address is in the access list.
func (sdb *IntraBlockState) AddressInAccessList(addr types.Address) bool {
	return sdb.accessList.ContainsAddress(addr)
}

// SlotInAccessList returns true if the given (address, slot)-tuple is in the access list.
func (sdb *IntraBlockState) SlotInAccessList(addr types.Address, slot types.Hash) (addressPresent bool, slotPresent bool) {
	return sdb.accessList.Contains(addr, slot)
}

// GenerateRootHash computes a root hash from all dirty state objects.
func (s *IntraBlockState) GenerateRootHash() types.Hash {
	if len(s.stateObjectsDirty) == 0 {
		return hash.NilHash
	}

	sortedAddrs := make(types.Addresses, 0, len(s.stateObjectsDirty))
	for addr := range s.stateObjectsDirty {
		sortedAddrs = append(sortedAddrs, addr)
	}
	sort.Sort(sortedAddrs)

	sha := hash.HasherPool.Get().(crypto.KeccakState)
	defer hash.HasherPool.Put(sha)
	sha.Reset()

	for _, address := range sortedAddrs {
		obj := s.getStateObject(address)
		if obj == nil {
			continue
		}
		data := obj.data
		if err := rlp.Encode(sha, []interface{}{
			data.Balance,
			data.Nonce,
			data.Initialised,
			data.CodeHash,
			data.Root,
		}); err != nil {
			panic("can't encode: " + err.Error())
		}
	}

	var root types.Hash
	if _, err := sha.Read(root[:]); err != nil {
		panic("can't read root hash: " + err.Error())
	}
	return root
}

// IntermediateRoot returns the current root hash.
// If a RootComputer is set (e.g., JMT), it delegates to that;
// otherwise falls back to the legacy incremental Keccak hash.
func (s *IntraBlockState) IntermediateRoot() types.Hash {
	if s.rootComputer != nil {
		return s.computeRootViaComputer()
	}
	return s.GenerateRootHash()
}

// computeRootViaComputer collects dirty accounts/storage and delegates
// to the pluggable RootComputer. If the computer supports LtHash
// (LtHashRootComputer interface), original state is also collected for
// incremental digest computation.
func (s *IntraBlockState) computeRootViaComputer() types.Hash {
	// Materialize pending balanceInc entries so they appear in stateObjectsDirty.
	for addr, bi := range s.balanceInc {
		if !bi.transferred {
			s.getStateObject(addr)
			s.stateObjectsDirty[addr] = struct{}{}
		}
	}
	// Merge journal.dirties — Finalize (block rewards) and post-block system
	// calls write to journal but there's no FinalizeTx after them.
	for addr := range s.journal.dirties {
		s.stateObjectsDirty[addr] = struct{}{}
	}

	accounts := make(map[types.Address]*account.StateAccount, len(s.stateObjectsDirty))
	storage := make(map[types.Address]map[types.Hash]*uint256.Int)

	// Check if LtHash is supported — if so, collect originals.
	ltRC, ltEnabled := s.rootComputer.(LtHashRootComputer)
	var originals map[types.Address]*account.StateAccount
	var originalStorage map[types.Address]map[types.Hash]*uint256.Int
	if ltEnabled {
		originals = make(map[types.Address]*account.StateAccount, len(s.stateObjectsDirty))
		originalStorage = make(map[types.Address]map[types.Hash]*uint256.Int)
	}

	for addr := range s.stateObjectsDirty {
		obj := s.getStateObject(addr)
		// Selfdestructed accounts must be treated as deleted for hashed state,
		// even though obj.deleted is only set later in CommitBlock/MakeWriteSet.
		// Without incarnation, old storage is not auto-isolated — must be explicitly deleted.
		if obj == nil || obj.deleted || obj.selfdestructed {
			accounts[addr] = nil
			// Collect storage slots for deletion so HashOnlyComputer removes them
			// from HashedStorage. Without this, stale slots accumulate.
			if obj != nil && len(obj.blockOriginStorage) > 0 {
				slots := make(map[types.Hash]*uint256.Int, len(obj.blockOriginStorage))
				for key := range obj.blockOriginStorage {
					slots[key] = new(uint256.Int) // zero = deleted
				}
				storage[addr] = slots
				if ltEnabled {
					origSlots := make(map[types.Hash]*uint256.Int, len(obj.blockOriginStorage))
					for key, origVal := range obj.blockOriginStorage {
						origSlots[key] = new(uint256.Int).Set(&origVal)
					}
					originalStorage[addr] = origSlots
				}
			}
			if ltEnabled {
				orig := new(account.StateAccount)
				if obj != nil {
					orig.Copy(&obj.original)
				}
				originals[addr] = orig
			}
			continue
		}
		acct := new(account.StateAccount)
		acct.Copy(&obj.data)
		accounts[addr] = acct

		if ltEnabled {
			orig := new(account.StateAccount)
			orig.Copy(&obj.original)
			originals[addr] = orig
		}

		if len(obj.dirtyStorage) == 0 {
			continue
		}
		slots := make(map[types.Hash]*uint256.Int, len(obj.dirtyStorage))
		for key, value := range obj.dirtyStorage {
			slots[key] = new(uint256.Int).Set(&value)
		}
		storage[addr] = slots

		if ltEnabled && len(obj.dirtyStorage) > 0 {
			origSlots := make(map[types.Hash]*uint256.Int, len(obj.dirtyStorage))
			for key := range obj.dirtyStorage {
				if origVal, ok := obj.blockOriginStorage[key]; ok {
					origSlots[key] = new(uint256.Int).Set(&origVal)
				} else {
					origSlots[key] = new(uint256.Int) // zero = didn't exist
				}
			}
			originalStorage[addr] = origSlots
		}
	}

	// Use LtHash-aware path if available.
	if ltEnabled {
		jmtRoot, ltRoot, err := ltRC.ComputeRootWithOriginals(accounts, originals, storage, originalStorage)
		if err != nil {
			panic("JMT+LtHash root computation failed: " + err.Error())
		}
		s.ltHashRoot = ltRoot
		return jmtRoot
	}

	root, err := s.rootComputer.ComputeRoot(accounts, storage)
	if err != nil {
		panic("JMT root computation failed: " + err.Error())
	}
	return root
}

// DirtyAccountData returns dirty accounts and their storage for leaf journal.
// Returns: accounts map (addr→encoded or nil=deleted), storage map (addr→slot→value).
func (sdb *IntraBlockState) DirtyAccountData() (
	accounts map[types.Address][]byte,
	storage map[types.Address]map[types.Hash][]byte,
) {
	accounts = make(map[types.Address][]byte, len(sdb.stateObjectsDirty))
	storage = make(map[types.Address]map[types.Hash][]byte)

	for addr := range sdb.stateObjectsDirty {
		obj := sdb.getStateObject(addr)
		if obj == nil || obj.deleted {
			accounts[addr] = nil
			continue
		}
		accounts[addr] = obj.data.MarshalV2()

		if len(obj.dirtyStorage) > 0 {
			slots := make(map[types.Hash][]byte, len(obj.dirtyStorage))
			for slot, value := range obj.dirtyStorage {
				if value.IsZero() {
					slots[slot] = nil
				} else {
					b := value.Bytes32()
					start := 0
					for start < 31 && b[start] == 0 {
						start++
					}
					slots[slot] = b[start:]
				}
			}
			storage[addr] = slots
		}
	}
	return
}

func (sdb *IntraBlockState) HasSelfdestructed(addr types.Address) bool {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return false
	}
	return stateObject.selfdestructed
}

// Selfdestruct marks the given account as suicided.
// This clears the account balance.
//
// The account's state object is still available until the state is committed,
// getStateObject will return a non-nil account after Suicide.
func (sdb *IntraBlockState) Selfdestruct(addr types.Address) bool {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return false
	}
	sdb.journal.append(selfdestructChange{
		account:     &addr,
		prev:        stateObject.selfdestructed,
		prevbalance: *stateObject.Balance(),
	})
	stateObject.markSelfdestructed()
	stateObject.data.Balance.Clear()
	return true
}

// Selfdestruct6780 implements EIP-6780 SELFDESTRUCT behavior for Cancun+.
// It only deletes the account (code, storage, nonce) if it was created in the same transaction.
// The balance transfer to the beneficiary is handled by the caller (opSelfdestruct) via AddBalance.
// This function handles the "SubBalance" part by clearing the caller's balance.
// If the account was NOT created in the same transaction, only the balance is transferred -
// the account keeps its code, storage, and nonce.
func (sdb *IntraBlockState) Selfdestruct6780(addr types.Address, beneficiary types.Address) {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted {
		return
	}

	if stateObject.created {
		sdb.journal.append(selfdestructChange{
			account:     &addr,
			prev:        stateObject.selfdestructed,
			prevbalance: *stateObject.Balance(),
		})
		stateObject.markSelfdestructed()
		stateObject.data.Balance.Clear()
		return
	}

	if addr == beneficiary {
		// Self-to-self case: the caller (opSelfdestruct) has already done
		// AddBalance(beneficiary, originalBalance), doubling the balance to 2*B.
		// To achieve the correct no-op semantics (balance stays at B), we subtract
		// half of the current balance, i.e. 2*B - (2*B/2) = B.
		// This works because 2*B is always even, so integer division is exact.
		currentBalance := stateObject.Balance()
		if currentBalance.IsZero() {
			return
		}
		halfBalance := new(uint256.Int).Div(currentBalance, uint256.NewInt(2))
		sdb.journal.append(balanceChange{
			account: &addr,
			prev:    *currentBalance,
		})
		stateObject.data.Balance.Sub(currentBalance, halfBalance)
		return
	}

	prevBalance := *stateObject.Balance()
	if prevBalance.IsZero() {
		return
	}
	sdb.journal.append(balanceChange{
		account: &addr,
		prev:    prevBalance,
	})
	stateObject.data.Balance.Clear()
}

// WasCreatedInCurrentTx returns whether the account was created in the current transaction.
func (sdb *IntraBlockState) WasCreatedInCurrentTx(addr types.Address) bool {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil {
		return false
	}
	return stateObject.created
}

// BeforeStateRoot computes a hash of the used state. Must be called after all transactions execute.
func (sdb *IntraBlockState) BeforeStateRoot() (hashValue types.Hash, err error) {
	if sdb.snap == nil {
		return types.Hash{}, nil
	}
	sort.Sort(sdb.snap.Items)

	codeHashes := sdb.CodeHashes()
	hashCodes := make(HashCodes, 0, len(codeHashes))
	for codeHash, code := range codeHashes {
		hashCodes = append(hashCodes, &HashCode{Hash: codeHash, Code: code})
	}
	sort.Sort(hashCodes)

	hasher := sha3.NewLegacyKeccak256()
	if err := EncodeBeforeState(hasher, sdb.snap.Items, hashCodes); err != nil {
		return types.Hash{}, err
	}
	keccakHasher, ok := hasher.(crypto.KeccakState)
	if !ok {
		return types.Hash{}, fmt.Errorf("unexpected hasher type %T", hasher)
	}
	if _, err := keccakHasher.Read(hashValue[:]); err != nil {
		return types.Hash{}, err
	}
	return hashValue, nil
}
