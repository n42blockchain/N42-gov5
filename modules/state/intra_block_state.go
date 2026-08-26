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
	"bytes"
	"errors"
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

// sortedAddresses returns the keys of the given map in lexicographic order.
// Used anywhere map iteration feeds into operations that touch the state
// reader (e.g., getStateObject, updateAccountWithWipe → updateTrie), so the
// observed read sequence — and therefore the block witness stream — stays
// deterministic across runs. Without sorting, Go's randomized map iteration
// produces witness bytes that differ between runs of the same block, which
// breaks witness-based replay and bit-for-bit reproducibility.
func sortedAddresses[V any](m map[types.Address]V) []types.Address {
	addrs := make([]types.Address, 0, len(m))
	for addr := range m {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})
	return addrs
}

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

	// discardBlockChanges disables bookkeeping that is needed only to emit a
	// block-level write set. Witness validation still executes every state
	// transition and keeps originStorage/dirtyStorage for correct intra-block
	// reads, but it never calls CommitBlock, so retaining a second copy of every
	// touched storage slot in blockOriginStorage is pure allocation overhead.
	// The flag is configuration, not block state, and is therefore preserved by
	// Reset in the same way as rootComputer.
	discardBlockChanges bool

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

	// balanceReadHook, when non-nil, observes every EXPLICIT balance read
	// (GetBalance — the entry point of the BALANCE/SELFBALANCE opcodes and
	// the EVM's CanTransfer). It deliberately does NOT fire for the
	// finalize-time balanceInc materialization (which goes through
	// getStateObject), so a lazy-coinbase parallel executor can use it to
	// detect precisely the txs that OBSERVE the coinbase balance — the only
	// case where deferring the tip credit changes semantics. Nil = zero cost.
	balanceReadHook func(types.Address)

	snap    *Snapshot
	codeMap map[types.Hash][]byte
	height  uint64

	// EIP-1153: Transient storage
	transientStorage transientStorage

	// storageWipes tracks addresses that need storage wiped during CommitBlock.
	// Set by Selfdestruct and contract CreateAccount. Not affected by revert
	// (unlike stateObject flags which are journaled).
	storageWipes map[types.Address]struct{}

	// priorTxWipes is the subset of storageWipes that was committed by an
	// earlier transaction in this block (via FinalizeTx). GetCommittedState
	// consults this to short-circuit reads of contracts that were destroyed
	// + (optionally) recreated in a previous tx. We do NOT use the live
	// storageWipes for the read short-circuit because pre-Cancun semantics
	// require a self-destructed contract to remain readable for the rest of
	// the same tx that destroyed it.
	priorTxWipes map[types.Address]struct{}

	// rootComputer is an optional pluggable state root implementation.
	// When nil, the default incremental Keccak hash is used.
	rootComputer RootComputer

	// ltHashRoot stores the LtHash digest root computed during IntermediateRoot().
	// Zero if LtHash is not active.
	ltHashRoot types.Hash

	// lastRootAccounts/lastRootStorage snapshot the exact dirty set the most
	// recent computeRootViaComputer call fed to the root computer, so the
	// leader path can replay the sealed block onto the live tree byte-exactly
	// (see LastRootDirtySet).
	lastRootAccounts map[types.Address]*account.StateAccount
	lastRootStorage  map[types.Address]map[types.Hash]*uint256.Int

	// wipedStorageSlots holds, per wiped address, the COMPLETE pre-block storage
	// slot set (slot → original value) captured at storage-wipe registration time
	// (Selfdestruct / contract CreateAccount), via a StorageEnumerator reader.
	// computeRootViaComputer consults it to remove the full storage of a deleted
	// or metamorphically-recreated account from the hashed state (JMT/MPT/BMT),
	// rather than only the slots touched in the current block (which leaves stale
	// leaves and a wrong state root). Only populated when a RootComputer is set
	// and the reader supports enumeration; the legacy Keccak path is unaffected.
	wipedStorageSlots map[types.Address]map[types.Hash]uint256.Int
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
		storageWipes:      map[types.Address]struct{}{},
		priorTxWipes:      map[types.Address]struct{}{},
		wipedStorageSlots: map[types.Address]map[types.Hash]uint256.Int{},
	}
}

// captureWipedSlots records the complete pre-block storage of addr so a later
// IntermediateRoot can delete every slot from the hashed state. It is called
// when a storage wipe is registered (Selfdestruct or contract CreateAccount),
// which happens during EVM execution — before any wipe is flushed to the DB —
// so the StorageEnumerator scan still observes the full slot set. First capture
// wins (a metamorphic destroy+recreate registers twice; the first scan already
// holds the authoritative pre-block storage). No-op without a RootComputer (the
// legacy Keccak path isolates storage differently) or without an enumerable
// reader (a witness-backed minimal client, which falls back to touched slots).
func (sdb *IntraBlockState) captureWipedSlots(addr types.Address) {
	if sdb.rootComputer == nil {
		return
	}
	if _, done := sdb.wipedStorageSlots[addr]; done {
		return
	}
	enum, ok := sdb.stateReader.(StorageEnumerator)
	if !ok {
		return
	}
	slots := make(map[types.Hash]uint256.Int)
	_ = enum.ForEachStorage(addr, func(slot types.Hash, value []byte) bool {
		var v uint256.Int
		v.SetBytes(value)
		slots[slot] = v
		return true
	})
	sdb.wipedStorageSlots[addr] = slots
}

// activeWipedSlots returns the captured pre-block storage of addr ONLY when the
// address is still flagged for wiping in storageWipes. captureWipedSlots is not
// journaled, so a SELFDESTRUCT/CREATE that gets reverted leaves a stale capture
// behind; storageWipes IS journal-reverted (storageWipeAddChange), so gating on
// it ensures a reverted wipe can never trigger a spurious slot deletion at root
// time. Returns nil when there is no active capture.
func (sdb *IntraBlockState) activeWipedSlots(addr types.Address) map[types.Hash]uint256.Int {
	if _, stillWiped := sdb.storageWipes[addr]; !stillWiped {
		return nil
	}
	return sdb.wipedStorageSlots[addr]
}

// mayHaveCachedStorage reports whether wiping addr's storage can leave a
// stale entry behind in an EXTERNAL read cache keyed by addr|slot.
//
// This is a hint for cache-backed writers only, never a consensus input, and
// it is conservative by construction — anything it cannot prove is reported
// true:
//
//   - no wipe capture for addr (no RootComputer, or a reader that cannot
//     enumerate storage) -> unknown -> true
//   - a non-empty capture -> the address really holds persisted storage -> true
//   - an empty capture -> the address holds no persisted storage, so nothing
//     read-through could have cached for it; slots written earlier in THIS
//     block remain possible, which the writer tracks on its own side.
//
// It exists because CreateContract fires on every plain CREATE/CREATE2 (see
// the stateObject.created branch below), not only on the rare metamorphic
// recreate — so a cache-backed writer that invalidates unconditionally throws
// its whole cross-block cache away several times per block.
func (sdb *IntraBlockState) mayHaveCachedStorage(addr types.Address) bool {
	slots, captured := sdb.wipedStorageSlots[addr]
	if !captured {
		return true
	}
	return len(slots) > 0
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

// DirtyAddresses returns the set of addresses whose state was modified during
// the current block execution. Intended for diagnostic dumps; the underlying
// stateObjectsDirty map is preserved (this method only iterates).
func (sdb *IntraBlockState) DirtyAddresses() []types.Address {
	out := make([]types.Address, 0, len(sdb.stateObjectsDirty))
	for addr := range sdb.stateObjectsDirty {
		out = append(out, addr)
	}
	return out
}

// DirtyStorageSlots returns the slot keys this block wrote on the given
// address (post-tx, pre-commit). Intended for diagnostic dumps.
func (sdb *IntraBlockState) DirtyStorageSlots(addr types.Address) []types.Hash {
	obj, ok := sdb.stateObjects[addr]
	if !ok || obj == nil {
		return nil
	}
	out := make([]types.Hash, 0, len(obj.dirtyStorage))
	for k := range obj.dirtyStorage {
		out = append(out, k)
	}
	return out
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

// SetDiscardBlockChanges selects validation-only state handling. Callers that
// may invoke CommitBlock, IntermediateRoot, or otherwise consume a block write
// set must leave this disabled.
func (sdb *IntraBlockState) SetDiscardBlockChanges(discard bool) {
	sdb.discardBlockChanges = discard
}

// Reset clears every per-block field so the IBS can be reused across
// blocks (witness-replay holds one IBS per worker). The trie itself is
// not reloaded — only ephemeral state. SELFDESTRUCT trackers, the
// saved-error sticky bit, the codeMap snapshot, and any stale snap /
// tracer pointers would silently corrupt the next block if left over.
// rootComputer is intentionally preserved (it's a config dependency
// set once via SetRootComputer, not block-scoped).
//
// Maps that get range-iterated downstream (notably the ones consumed
// by sortedAddresses in FinalizeTx — balanceInc, journal.dirties — and
// the stateObjects map iterated here) are reallocated rather than
// clear()ed. Go's clear() leaves the bucket array sized to the
// historical high-water mark; on a 24M-block replay a single heavy
// block (e.g. a 5K-touch AMM block) inflates buckets that subsequent
// iterations must scan in full, even when len() drops to ~50. pprof
// showed matchFull at 27% of CPU under the clear()-only Reset.
func (sdb *IntraBlockState) Reset() {
	// Return stateObjects to the pool before discarding the map.
	// Their three Storage maps are cleared and re-bound on next Get.
	for _, so := range sdb.stateObjects {
		putStateObject(so)
	}
	sdb.stateObjects = make(map[types.Address]*stateObject)
	sdb.stateObjectsDirty = make(map[types.Address]struct{})
	sdb.nilAccounts = make(map[types.Address]struct{})
	clear(sdb.storageWipes)
	clear(sdb.priorTxWipes)
	clear(sdb.wipedStorageSlots)
	sdb.savedErr = nil
	sdb.codeMap = nil
	sdb.snap = nil
	sdb.tracer = nil
	sdb.trace = false
	sdb.height = 0
	sdb.ltHashRoot = types.Hash{}
	sdb.thash = types.Hash{}
	sdb.bhash = types.Hash{}
	sdb.txIndex = 0
	sdb.nextRevisionID = 0
	sdb.logs = make(map[types.Hash][]*block.Log)
	sdb.logSize = 0
	sdb.clearJournalAndRefund()
	if sdb.accessList == nil {
		sdb.accessList = newAccessList()
	} else {
		sdb.accessList.Reset()
	}
	sdb.balanceInc = make(map[types.Address]*BalanceIncrease)
}

func (sdb *IntraBlockState) AddLog(log2 *block.Log) {
	sdb.journal.push(addLogChange{txhash: sdb.thash}.record())
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
	sdb.journal.push(transientStorageChange{
		account:  &addr,
		key:      key,
		prevalue: prev,
	}.record())
	sdb.transientStorage.Set(addr, key, value)
}

// AddRefund adds gas to the refund counter
func (sdb *IntraBlockState) AddRefund(gas uint64) {
	sdb.journal.push(refundChange{prev: sdb.refund}.record())
	sdb.refund += gas
}

// SubRefund removes gas from the refund counter.
// This method will set an error if the refund counter goes below zero
func (sdb *IntraBlockState) SubRefund(gas uint64) {
	if gas > sdb.refund {
		sdb.setErrorUnsafe(fmt.Errorf("refund counter below zero: gas=%d, refund=%d", gas, sdb.refund))
		return
	}
	sdb.journal.push(refundChange{prev: sdb.refund}.record())
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
	// A pending balance increment DOES constitute existence: geth's
	// AddBalance materializes an object, so its Exist answers true for such
	// accounts. Answering false diverged on pre-SpuriousDragon new-account
	// gas (e.g. CALL to an address just credited by SELFDESTRUCT in the same
	// tx). Unlike getStateObject this must NOT materialize the object — the
	// journal entry would escape the EVM snapshot/revert boundary.
	//
	// Checked BEFORE the nilAccounts/DB branches, not inside nilAccounts: the
	// first call for a never-read address falls straight through to the DB
	// read, and answering false there while the second call (now served by
	// nilAccounts) answers true made the gas charge depend on how many times
	// the address had been probed.
	if inc, has := sdb.balanceInc[addr]; has && inc.count > 0 {
		return true
	}
	// Check nilAccounts cache (known non-existent).
	if _, ok := sdb.nilAccounts[addr]; ok {
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
	// The emptiness verdict depends on the BALANCE component (so.empty reads
	// balance.IsZero), so this is an implicit balance observation — fire the
	// hook so lazy-coinbase detects a tx that branches on Empty(coinbase)
	// (EIP-161 new-account CALL surcharge, SELFDESTRUCT-to-coinbase) and
	// re-runs the block non-lazy. Conservative: fires even when balance is
	// non-zero (cost is a redundant non-lazy re-run, never a wrong root).
	if sdb.balanceReadHook != nil {
		sdb.balanceReadHook(addr)
	}
	sdb.traceAccountRead(addr)
	so := sdb.getStateObject(addr)
	return so == nil || so.deleted || so.empty()
}

// HasNonEmptyStorage returns true if the account has any non-zero storage entries.
// This is used by EIP-7610 style collision detection to prevent deploying code
// at addresses that have pre-existing storage.
//
// Persisted storage is detected by ENUMERATION when the reader supports it
// (StorageEnumerator, the same capability captureWipedSlots relies on),
// which is the geth-equivalent answer ("is the storage root empty"). The
// slot-0/0x01 probe below is only a last-resort fallback for readers that
// cannot enumerate (e.g. a witness-backed minimal client): it can miss
// storage living at other slots, so such a reader may under-detect a
// collision. Callers on consensus paths run with an enumerating reader.
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
	// Persisted storage: enumerate when possible — this is the complete
	// answer, equivalent to geth's empty-storage-root test. Stop at the
	// first non-zero slot.
	if enum, ok := sdb.stateReader.(StorageEnumerator); ok {
		// A pending wipe means the persisted rows are logically gone
		// already (SELFDESTRUCT/CREATE earlier in this block); only the
		// in-memory values checked above count then.
		if _, wiped := sdb.storageWipes[addr]; wiped {
			return false
		}
		found := false
		err := enum.ForEachStorage(addr, func(slot types.Hash, value []byte) bool {
			for _, b := range value {
				if b != 0 {
					found = true
					return false // stop enumerating
				}
			}
			return true
		})
		switch {
		case err == nil:
			return found
		case errors.Is(err, ErrNoStorageEnumeration):
			// The reader declares the capability but cannot scan right now.
			// Fall through to the probe rather than accept a silent empty
			// enumeration as "no storage".
		default:
			// A real scan failure must NOT be resolved by guessing: either
			// answer would be a consensus decision made on missing data
			// (false lets a colliding CREATE through, true reverts a valid
			// deploy). Fail the block instead.
			sdb.setErrorUnsafe(err)
			return found
		}
	}

	// Fallback for non-enumerating readers: probe the two slots most
	// likely to be occupied. Incomplete by construction (see doc above).
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

// SetBalanceReadHook installs (or clears, with nil) the explicit-balance-read
// observer. See the field doc.
func (sdb *IntraBlockState) SetBalanceReadHook(h func(types.Address)) {
	sdb.balanceReadHook = h
}

// GetBalance retrieves the balance from the given address or 0 if object not found.
func (sdb *IntraBlockState) GetBalance(addr types.Address) *uint256.Int {
	if sdb.balanceReadHook != nil {
		sdb.balanceReadHook(addr)
	}
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
	codeSize, err := sdb.stateReader.ReadAccountCodeSize(addr, stateObject.data.CodeHash)
	if err != nil {
		sdb.setErrorUnsafe(err)
	}
	if codeSize > 0 && sdb.codeMap != nil {
		codeHash := types.BytesToHash(stateObject.CodeHash())
		code, _ := sdb.stateReader.ReadAccountCode(addr, codeHash)
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
		sdb.journal.push(balanceIncrease{
			account:  &addr,
			increase: *amount,
		}.record())
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

// Suicide is a legacy alias for Selfdestruct.
// Retained for compatibility with the common.StateDB interface.
func (sdb *IntraBlockState) Suicide(addr types.Address) bool {
	return sdb.Selfdestruct(addr)
}

func (sdb *IntraBlockState) getStateObject(addr types.Address) (stateObject *stateObject) {
	// Prefer 'live' objects.
	if obj := sdb.stateObjects[addr]; obj != nil {
		// A live object can still owe a pending increase: reverting a
		// balanceIncreaseTransfer un-folds the amount and clears the flag
		// while leaving the object live. Without re-folding here the
		// increase is lost for good — FinalizeTx/CommitBlock materialize
		// pending increases THROUGH this function, so an early return
		// would silently drop e.g. SELFDESTRUCT proceeds parked for an
		// untouched beneficiary that a later reverted call happened to read.
		sdb.foldBalanceIncrease(addr, obj)
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

// foldBalanceIncrease applies a still-pending balance increase onto object
// and journals the fold so it can be undone. Idempotent: the transferred
// flag makes a second call a no-op, and the flag is only ever cleared by
// balanceIncreaseTransfer.revert, which also un-folds the amount.
func (sdb *IntraBlockState) foldBalanceIncrease(addr types.Address, object *stateObject) {
	// Every getStateObject lands here, and balanceInc is empty for the vast
	// majority of transactions — but a Go map lookup hashes the 20-byte key
	// before it discovers the map is empty. On the dense-replay profile that
	// wasted lookup was 0.7% of all CPU.
	if len(sdb.balanceInc) == 0 {
		return
	}
	bi, ok := sdb.balanceInc[addr]
	if !ok || bi.transferred {
		return
	}
	object.data.Balance.Add(&object.data.Balance, &bi.increase)
	bi.transferred = true
	a := addr
	sdb.journal.push(balanceIncreaseTransfer{account: &a, bi: bi}.record())
}

func (sdb *IntraBlockState) setStateObject(addr types.Address, object *stateObject) {
	sdb.foldBalanceIncrease(addr, object)
	sdb.stateObjects[addr] = object
	delete(sdb.nilAccounts, addr)
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
	if previous == nil {
		sdb.journal.push(createObjectChange{account: &addr}.record())
	} else {
		sdb.journal.push(resetObjectChange{account: &addr, prev: previous}.record())
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

	newObj := sdb.createObject(addr, previous)
	if previous != nil {
		newObj.data.Balance.Set(&previous.data.Balance)
		newObj.data.Initialised = true
	}

	if contractCreation {
		newObj.created = true
		if _, already := sdb.storageWipes[addr]; !already {
			sdb.journal.push(storageWipeAddChange{account: &addr}.record())
			sdb.storageWipes[addr] = struct{}{}
		}
		// Capture the full pre-block storage before it is wiped, so the hashed
		// state root removes every old slot (CREATE2-over-existing / metamorphic).
		sdb.captureWipedSlots(addr)
	} else {
		newObj.selfdestructed = false
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
	removeEmptyAccounts       bool
	preserveAuraSystemAccount bool
}

func newAccountWritePolicy(chainRules *params.Rules) accountWritePolicy {
	if chainRules == nil {
		return accountWritePolicy{}
	}
	return accountWritePolicy{
		removeEmptyAccounts:       chainRules.IsSpuriousDragon,
		preserveAuraSystemAccount: chainRules.IsAura,
	}
}

func (p accountWritePolicy) shouldRemoveEmptyAccount(addr types.Address, stateObject *stateObject) bool {
	return p.removeEmptyAccounts && stateObject.empty() && (!p.preserveAuraSystemAccount || addr != SystemAddress)
}

func (p accountWritePolicy) shouldAllowWriteBack(stateObject *stateObject) bool {
	if stateObject.selfdestructed {
		return false
	}
	return true
}

func updateAccount(policy accountWritePolicy, stateWriter StateWriter, addr types.Address, stateObject *stateObject, isDirty bool) error {
	return updateAccountWithWipe(policy, stateWriter, addr, stateObject, isDirty, false)
}

// createContract funnels every storage-wipe path through one place so the
// optional cache hint always travels with the CreateContract it belongs to.
// Writers that do not implement HintedContractCreator are unaffected.
func createContract(stateWriter StateWriter, addr types.Address, stateObject *stateObject) error {
	h, ok := stateWriter.(HintedContractCreator)
	if !ok {
		return stateWriter.CreateContract(addr)
	}
	mayCache := true
	if stateObject != nil && stateObject.db != nil {
		mayCache = stateObject.db.mayHaveCachedStorage(addr)
	}
	return h.CreateContractHinted(addr, mayCache)
}
func updateAccountWithWipe(policy accountWritePolicy, stateWriter StateWriter, addr types.Address, stateObject *stateObject, isDirty bool, needsWipe bool) error {
	emptyRemoval := policy.shouldRemoveEmptyAccount(addr, stateObject)
	shouldDelete := stateObject.selfdestructed || (isDirty && emptyRemoval)
	if shouldDelete {
		if err := stateWriter.DeleteAccount(addr, &stateObject.original); err != nil {
			return err
		}
		if err := createContract(stateWriter, addr, stateObject); err != nil {
			return err
		}
		stateObject.deleted = true
	} else if needsWipe {
		// Wipe old storage but don't delete — account was recreated after SELFDESTRUCT.
		if err := createContract(stateWriter, addr, stateObject); err != nil {
			return err
		}
	}
	allowWriteBack := policy.shouldAllowWriteBack(stateObject)
	if isDirty && allowWriteBack && !emptyRemoval {
		stateObject.deleted = false
		// Write any contract code associated with the state object
		if stateObject.code != nil && stateObject.dirtyCode {
			if err := stateWriter.UpdateAccountCode(addr, stateObject.data.CodeHash, stateObject.code); err != nil {
				return err
			}
		}
		if stateObject.created {
			if err := createContract(stateWriter, addr, stateObject); err != nil {
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
	// Sort addresses before iteration. getStateObject falls through to
	// sdb.stateReader.ReadAccountData when the object isn't cached, and
	// the reader here is the block-witness recorder (ethel). Map-order
	// iteration would produce witness byte streams that differ between
	// runs of the same block.
	for _, addr := range sortedAddresses(sdb.balanceInc) {
		if bi := sdb.balanceInc[addr]; !bi.transferred {
			sdb.getStateObject(addr)
		}
	}
	for _, addr := range sortedAddresses(sdb.journal.dirties) {
		so, exist := sdb.stateObjects[addr]
		if !exist {
			continue
		}

		// Call updateAccount with noop writer. This sets deleted flag and
		// runs updateTrie(noop) which updates originStorage — both are
		// necessary for correct cross-tx EVM behavior.
		// Storage wipe is handled by storageWipes map in MakeWriteSet,
		// not by stateObject flags, so no flag inheritance issues.
		if err := updateAccount(policy, stateWriter, addr, so, true); err != nil {
			return err
		}

		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	sdb.promoteWipes()
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
	sdb.promoteWipes()
	sdb.clearCurrentTxFlags()
	sdb.clearJournalAndRefund()
}

// promoteWipes lifts wipes that happened in the just-finished tx into
// priorTxWipes so the next tx in this block sees those addresses as
// empty (pre-Cancun "destroyed at end of tx"). Live storageWipes still
// drives the end-of-block CreateContract pass in MakeWriteSet.
func (sdb *IntraBlockState) promoteWipes() {
	for addr := range sdb.storageWipes {
		sdb.priorTxWipes[addr] = struct{}{}
	}
}

// CommitBlock finalizes the state by removing the self destructed objects
// and clears the journal as well as the refunds.
func (sdb *IntraBlockState) CommitBlock(chainRules *params.Rules, stateWriter StateWriter) error {
	// Sorted iteration: see note on FinalizeTx — getStateObject can read
	// through the block-witness recorder, so iteration order is visible
	// in the witness stream.
	for _, addr := range sortedAddresses(sdb.balanceInc) {
		if bi := sdb.balanceInc[addr]; !bi.transferred {
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
	// The first loop only populates the stateObjectsDirty set and doesn't
	// read state; iteration order here is irrelevant. The second loop
	// calls updateAccountWithWipe → stateObject.updateTrie(stateWriter),
	// which can trigger storage reads through the block-witness recorder,
	// so its order MUST be deterministic.
	for addr := range sdb.journal.dirties {
		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	for _, addr := range sortedAddresses(sdb.stateObjects) {
		stateObject := sdb.stateObjects[addr]
		_, isDirty := sdb.stateObjectsDirty[addr]
		_, needsWipe := sdb.storageWipes[addr]
		if err := updateAccountWithWipe(policy, stateWriter, addr, stateObject, isDirty, needsWipe); err != nil {
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
// used when the EVM emits new state logs. The access list is reset in place
// so its slotmaps can be recycled across txs (see accessList.Reset for why).
func (sdb *IntraBlockState) Prepare(thash, bhash types.Hash, ti int) {
	sdb.thash = thash
	sdb.bhash = bhash
	sdb.txIndex = ti
	if sdb.accessList == nil {
		sdb.accessList = newAccessList()
	} else {
		sdb.accessList.Reset()
	}
}

// clearJournalAndRefund resets per-transaction ephemeral state.
// Reuses existing journal and transientStorage backing maps to
// avoid ~2000 map allocations per block (1000 tx × 2 maps).
func (sdb *IntraBlockState) clearJournalAndRefund() {
	sdb.journal.reset()
	sdb.validRevisions = sdb.validRevisions[:0]
	sdb.refund = 0
	sdb.transientStorage.reset()
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
	// Precompiles are warm by fiat for the whole transaction and cannot be
	// reverted out, so they are recorded as a static set rather than as
	// journaled map entries. Only an address the static set cannot hold
	// takes the ordinary path.
	for _, addr := range sdb.accessList.SetPrecompiles(precompiles) {
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
		sdb.journal.push(accessListAddAccountChange{address: addr}.record())
	}
}

// AddSlotToAccessList adds the given (address, slot)-tuple to the access list.
func (sdb *IntraBlockState) AddSlotToAccessList(addr types.Address, slot types.Hash) {
	addrMod, slotMod := sdb.accessList.AddSlot(addr, slot)
	if addrMod {
		sdb.journal.push(accessListAddAccountChange{address: addr}.record())
	}
	if slotMod {
		sdb.journal.push(accessListAddSlotChange{address: addr, slot: slot}.record())
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
	// Materialize pending balanceInc entries so they appear in
	// stateObjectsDirty. Iterate in sorted order: getStateObject faults
	// reads through the state reader, and the block-witness recorder
	// observes read order — an unsorted walk here (this runs BEFORE the
	// sorted CommitBlock loop, in IntermediateRoot) made the witness byte
	// stream nondeterministic run-to-run, breaking ethel replay / mobile
	// verification reproducibility.
	for _, addr := range sortedAddresses(s.balanceInc) {
		if bi := s.balanceInc[addr]; bi != nil && !bi.transferred {
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

	for _, addr := range sortedAddresses(s.stateObjectsDirty) {
		obj := s.getStateObject(addr)
		// Selfdestructed accounts must be treated as deleted for hashed state,
		// even though obj.deleted is only set later in CommitBlock/MakeWriteSet.
		// Without incarnation, old storage is not auto-isolated — must be explicitly deleted.
		if obj == nil || obj.deleted || obj.selfdestructed {
			accounts[addr] = nil
			// Collect storage slots for deletion so the RootComputer removes them
			// from the hashed state. Prefer the COMPLETE pre-block slot set captured
			// at wipe time (wipedStorageSlots) — deleting only obj.blockOriginStorage
			// (slots touched this block) leaves stale leaves and a wrong root.
			// Fall back to blockOriginStorage when no full capture exists (reader
			// without StorageEnumerator, e.g. a witness-backed minimal client).
			if wiped := s.activeWipedSlots(addr); len(wiped) > 0 {
				slots := make(map[types.Hash]*uint256.Int, len(wiped))
				for key := range wiped {
					slots[key] = new(uint256.Int) // zero = deleted
				}
				// captureWipedSlots scanned the reader's storage at
				// selfdestruct time. On a real-writer-per-tx path
				// (replay-v2), a slot an earlier tx in THIS block already
				// zeroed was deleted from the reader before the capture ran,
				// so it is missing here — union dirtyStorage's zeroed keys so
				// the RootComputer still emits their delete op (over-deleting
				// an absent key is a no-op).
				var unioned []types.Hash
				if obj != nil {
					for key := range obj.dirtyStorage {
						if _, ok := slots[key]; !ok {
							slots[key] = new(uint256.Int)
							unioned = append(unioned, key)
						}
					}
				}
				storage[addr] = slots
				if ltEnabled {
					origSlots := make(map[types.Hash]*uint256.Int, len(slots))
					for key, origVal := range wiped {
						ov := origVal
						origSlots[key] = new(uint256.Int).Set(&ov)
					}
					// The union above added slots the capture missed; without a
					// matching original the LtHash computer sees old=nil,new=0
					// (both-zero -> no digest change) and keeps the pre-block
					// element for exactly the slots the tree just deleted. The
					// tree root would be fixed and the digest would not, which
					// defeats the cross-check the LtHash engine exists for.
					for _, key := range unioned {
						if ov, ok := obj.blockOriginStorage[key]; ok {
							v := ov
							origSlots[key] = new(uint256.Int).Set(&v)
						}
					}
					originalStorage[addr] = origSlots
				}
			} else if obj != nil && len(obj.blockOriginStorage) > 0 {
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

		// Metamorphic case: this address was destroyed+recreated (or CREATE2'd over
		// an existing contract) this block but ends the block alive. The old storage
		// must still be wiped from the hashed state, including slots not touched
		// after recreation: seed every captured pre-block slot to zero, then overlay
		// the new dirtyStorage values.
		wiped := s.activeWipedSlots(addr)
		if len(wiped) == 0 && len(obj.dirtyStorage) == 0 {
			continue
		}
		slots := make(map[types.Hash]*uint256.Int, len(wiped)+len(obj.dirtyStorage))
		for key := range wiped {
			slots[key] = new(uint256.Int) // zero = delete old slot
		}
		for key, value := range obj.dirtyStorage {
			v := value
			slots[key] = new(uint256.Int).Set(&v)
		}
		if len(slots) == 0 {
			continue
		}
		storage[addr] = slots

		if ltEnabled {
			origSlots := make(map[types.Hash]*uint256.Int, len(slots))
			for key, origVal := range wiped {
				ov := origVal
				origSlots[key] = new(uint256.Int).Set(&ov)
			}
			for key := range obj.dirtyStorage {
				if origVal, ok := obj.blockOriginStorage[key]; ok {
					origSlots[key] = new(uint256.Int).Set(&origVal)
				} else if _, seeded := origSlots[key]; !seeded {
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
			panic("state root computation failed (commitment+LtHash): " + err.Error())
		}
		s.ltHashRoot = ltRoot
		s.lastRootAccounts, s.lastRootStorage = accounts, storage
		return jmtRoot
	}

	root, err := s.rootComputer.ComputeRoot(accounts, storage)
	if err != nil {
		// rootComputer is whichever engine this chain commits with (MPT for
		// eth-el, QMDB for the native chain, …) — not necessarily JMT, which is
		// what this message used to say back when JMT was the only one. Naming a
		// specific engine here sends the reader looking for a misconfiguration
		// that isn't there; the error text below says what actually failed.
		panic("state root computation failed: " + err.Error())
	}
	// Snapshot the EXACT dirty set this root was computed from. The miner
	// computes the sealed root on an isolated computer; the same block must
	// later be applied to the live tree, and for an append-only commitment
	// (QMDB) that application has to use the byte-identical op set — a fresh
	// collection is NOT guaranteed to be identical (the collection itself
	// faults objects and merges journal dirties), and any extra no-op entry
	// still consumes an append slot and shifts the root.
	s.lastRootAccounts, s.lastRootStorage = accounts, storage
	return root
}

// LastRootDirtySet returns the account/storage dirty set the most recent
// computeRootViaComputer call fed to the root computer (nil before any call).
// Used to replay the leader's sealed block onto the live tree byte-exactly.
func (s *IntraBlockState) LastRootDirtySet() (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
	return s.lastRootAccounts, s.lastRootStorage
}

// DirtyAccountData returns dirty accounts and their storage for leaf journal.
// Returns: accounts map (addr→encoded or nil=deleted), storage map (addr→slot→value).
func (sdb *IntraBlockState) DirtyAccountData() (
	accounts map[types.Address][]byte,
	storage map[types.Address]map[types.Hash][]byte,
) {
	accounts = make(map[types.Address][]byte, len(sdb.stateObjectsDirty))
	storage = make(map[types.Address]map[types.Hash][]byte)

	// Sorted, like FinalizeTx/CommitBlock/computeRootViaComputer: getStateObject
	// faults reads through the state reader, and the block-witness recorder
	// observes read order.
	for _, addr := range sortedAddresses(sdb.stateObjectsDirty) {
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
	sdb.journal.push(selfdestructChange{
		account:     &addr,
		prev:        stateObject.selfdestructed,
		prevbalance: *stateObject.Balance(),
	}.record())
	stateObject.markSelfdestructed()
	stateObject.data.Balance.Clear()
	if _, already := sdb.storageWipes[addr]; !already {
		sdb.journal.push(storageWipeAddChange{account: &addr}.record())
		sdb.storageWipes[addr] = struct{}{}
	}
	// Capture the full pre-block storage before the wipe so the hashed state
	// root removes every old slot, not just the ones touched this block.
	sdb.captureWipedSlots(addr)
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
		sdb.journal.push(selfdestructChange{
			account:     &addr,
			prev:        stateObject.selfdestructed,
			prevbalance: *stateObject.Balance(),
		}.record())
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
		sdb.journal.push(balanceChange{
			account: &addr,
			prev:    *currentBalance,
		}.record())
		stateObject.data.Balance.Sub(currentBalance, halfBalance)
		return
	}

	prevBalance := *stateObject.Balance()
	if prevBalance.IsZero() {
		return
	}
	sdb.journal.push(balanceChange{
		account: &addr,
		prev:    prevBalance,
	}.record())
	stateObject.data.Balance.Clear()
}

// Selfdestruct8246 implements EIP-8246 (Amsterdam) SELFDESTRUCT with the
// account itself as beneficiary. For a same-transaction-created account
// the creation is destroyed (code/storage/nonce gone, as under EIP-6780)
// but — unlike 6780 — the balance survives: the account is immediately
// recreated as a fresh, balance-only account via the standard
// recreate-after-selfdestruct path (journal-safe, revert-safe). For a
// pre-existing account this is a full no-op: 6780 already preserves
// code/storage, and with no beneficiary transfer the balance stays put.
func (sdb *IntraBlockState) Selfdestruct8246(addr types.Address) {
	stateObject := sdb.getStateObject(addr)
	if stateObject == nil || stateObject.deleted || !stateObject.created {
		return
	}
	balance := *stateObject.Balance()
	sdb.journal.push(selfdestructChange{
		account:     &addr,
		prev:        stateObject.selfdestructed,
		prevbalance: balance,
	}.record())
	stateObject.markSelfdestructed()
	stateObject.data.Balance.Clear()
	if balance.IsZero() {
		return
	}
	sdb.CreateAccount(addr, false)
	sdb.AddBalance(addr, &balance)
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
