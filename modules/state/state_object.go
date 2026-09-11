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
//
// stateObject: in-memory representation of a single Ethereum account.
// Code and Storage are convenience types for raw contract code and
// per-slot uint256 values. emptyCodeHash and emptyCodeHashH cache the
// Keccak256 of empty code to compare against StateAccount.CodeHash.
// stateObject carries the account snapshot plus dirty storage, code
// and destructed flags that IntraBlockState drives during tx execution.

package state

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/log"
)

var (
	emptyCodeHash  = utils.Keccak256(nil)
	emptyCodeHashH = types.BytesToHash(emptyCodeHash)
)

type Code []byte

func (c Code) String() string {
	return string(c)
}

type Storage map[types.Hash]uint256.Int

func (s Storage) String() string {
	var str string
	for key, value := range s {
		str += fmt.Sprintf("%X : %X\n", key, value)
	}
	return str
}

func (s Storage) Copy() Storage {
	cpy := make(Storage, len(s))
	for key, value := range s {
		cpy[key] = value
	}
	return cpy
}

// stateObject represents an Ethereum account which is being modified.
//
// The usage pattern is as follows:
// First you need to obtain a state object.
// Account values can be accessed and modified through the object.
type stateObject struct {
	address  types.Address
	data     account.StateAccount
	original account.StateAccount
	db       *IntraBlockState

	// Write caches.
	code Code // contract bytecode, set when code is loaded

	// storage is the one map that used to be three (originStorage,
	// blockOriginStorage, dirtyStorage). Each slot carries all three views and
	// an epoch; see slotEntry. dirtyKeys lists, in first-write order, every
	// slot written this block — the old dirtyStorage key set — so writers can
	// iterate it without a map walk.
	storage   map[types.Hash]slotEntry
	dirtyKeys []types.Hash
	// blockOriginStorage keeps each slot's value at the start of the block for
	// the writer's (original, value) pair and the wipe/LtHash fallbacks. It is
	// populated only when a block write set is wanted; validation-only replay
	// (discardBlockChanges) never allocates it, which keeps slotEntry to the
	// two views it actually reads.
	blockOriginStorage Storage
	fakeStorage        Storage // Fake storage which constructed by caller for debugging purpose.

	// sawNonZeroCommitted records whether any committed read in this block
	// returned a non-zero value. HasNonEmptyStorage needs that fact: a later
	// write in the same block can zero the slot and, once its epoch passes,
	// the committed view follows the write, so the block-start fact survives
	// only here (and in origin, which is not kept under discardBlockChanges).
	sawNonZeroCommitted bool

	// Cache flags.
	// When an object is marked suicided it will be delete from the trie
	// during the "update" phase of the state transition.
	dirtyCode      bool // true if the code was updated
	selfdestructed bool
	deleted        bool // true if account was deleted during the lifetime of this object
	created        bool // true if this object represents a newly created contract
}

// slotEntry is one storage slot's state for the current block.
//
// The three maps this replaces answered three questions: dirtyStorage — was
// the slot written this block and what is its latest value; originStorage —
// what was the value at the start of the current transaction; and
// blockOriginStorage — what was the value at the start of the block. Every
// SSTORE gas computation asked the first two, and FinalizeTx copied the
// whole dirty map into the origin map after every transaction (1.6% of all
// replay CPU, 68 GiB of map growth per 200k blocks, and a top contributor
// to allocator-lock contention at 256 workers).
//
// One entry answers all three with one lookup. epoch records the storage
// epoch (IntraBlockState.storageEpoch, bumped once per FinalizeTx) of the
// last write: while epoch == current, committed holds the value at the start
// of this transaction and cur the latest write; once the epoch moves on, cur
// IS the committed value and nothing needs copying. known marks a committed
// value as available (read from the DB or established by a write);
// The block-start value lives in blockOriginStorage, kept only when a write set is wanted.
type slotEntry struct {
	cur       uint256.Int
	committed uint256.Int
	epoch     uint32
	known     bool
}

// storageRecycleMax bounds the map a pooled stateObject keeps. clear() costs
// O(bucket capacity), not O(len), and Go maps never shrink: an object that once
// held a DEX pool's ten thousand slots would pay a ten-thousand-bucket sweep on
// every block it is recycled into, forever. Small maps are cleared in place so
// their backing arrays keep serving; big ones are dropped to the GC and rebuilt
// lazily by newObject.
const storageRecycleMax = 64

// currentEpoch is the storage epoch the owning state is executing in.
func (so *stateObject) currentEpoch() uint32 {
	if so.db == nil {
		return 1
	}
	return so.db.storageEpoch
}

// dirtyValue returns the latest written value of a block-dirty slot. Only
// meaningful for keys in dirtyKeys.
func (so *stateObject) dirtyValue(key types.Hash) uint256.Int {
	return so.storage[key].cur
}

// blockOrigin returns the value the slot had at the start of the block, if it
// was captured (the old blockOriginStorage lookup).
func (so *stateObject) blockOrigin(key types.Hash) (uint256.Int, bool) {
	v, ok := so.blockOriginStorage[key]
	return v, ok
}

// blockOriginLen counts captured block-start values.
func (so *stateObject) blockOriginLen() int { return len(so.blockOriginStorage) }

// eachBlockOrigin visits every captured block-start value.
func (so *stateObject) eachBlockOrigin(fn func(key types.Hash, origin uint256.Int)) {
	for k, v := range so.blockOriginStorage {
		fn(k, v)
	}
}

// committedKnown reports whether the slot's committed value is cached (the
// old originStorage presence test).
func (so *stateObject) committedKnown(key types.Hash) bool {
	return so.storage[key].known
}

// empty returns whether the account is considered empty.
func (so *stateObject) empty() bool {
	return so.data.Nonce == 0 && so.data.Balance.IsZero() && bytes.Equal(so.data.CodeHash[:], emptyCodeHash)
}

// stateObjectPool recycles stateObject structs (and their three
// Storage maps) across blocks. EVM hot blocks touch ~5K-10K addrs,
// each one a fresh stateObject + 3 fresh maps without pooling — top
// allocator at 3.6B/12h. Reset on each Get clears the maps in place
// so the backing arrays survive.
// stateObjectPooling reports whether newObject may recycle a struct through
// stateObjectPool. It is a diagnostic lever, not a tuning knob: set
// N42_STATE_OBJECT_POOL=off (or 0/false/disable) to make every newObject a
// fresh allocation.
//
// It exists to decide one question a race detector structurally cannot answer.
// A recycled object that some field is not reset on carries the previous
// account's value into the next one -- no concurrent access, so nothing to
// report -- and the symptom only appears once the pool has churned enough for
// the wrong struct to come back. That is the shape of the intermittent
// "nonce too high" failure seen on a full archive replay at 256 workers: it
// needs a run from genesis to reach, it does not reproduce on the same block
// range in isolation, and -race over 60k blocks found only an unrelated
// metrics gauge.
//
// Running the failing configuration with pooling off splits the hypothesis in
// one pass: if the failure stops, the fault is in newObject/putStateObject's
// reset coverage; if it persists, the whole class is excluded.
var stateObjectPooling = func() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("N42_STATE_OBJECT_POOL"))) {
	case "off", "0", "false", "no", "disable", "disabled":
		return false
	default:
		return true
	}
}()

// StateObjectPoolingEnabled reports the pooling mode so a process can record
// which one it ran under. A diagnostic run that cannot be told apart from an
// ordinary one afterwards is worth very little.
func StateObjectPoolingEnabled() bool { return stateObjectPooling }

var stateObjectPool = sync.Pool{
	New: func() interface{} { return freshStateObject() },
}

// freshStateObject builds a never-used state object. Both the pool's New and
// the pooling-disabled path go through it so the two modes cannot drift: a
// struct that differed between them would make the pooling experiment measure
// the switch instead of the hypothesis.
//
// No size hint: most accounts touch 0 slots, so lazy bucket alloc avoids
// ~600B/object of empty-map overhead retained in the pool.
func freshStateObject() *stateObject {
	return &stateObject{
		storage: make(map[types.Hash]slotEntry),
	}
}

// putStateObject returns so to the pool. Must be called only when
// the IntraBlockState that owned so is being reset / discarded.
func putStateObject(so *stateObject) {
	if so == nil {
		return
	}
	if !stateObjectPooling {
		// Dropped on the floor for the collector. Deliberately skipping the
		// clearing below too: with no reuse it buys nothing, and leaving it out
		// keeps the disabled mode from masking a missing reset.
		return
	}
	// Drop references that could pin large memory.
	so.db = nil
	so.code = nil
	so.fakeStorage = nil
	if len(so.storage) > storageRecycleMax {
		so.storage = nil
	} else {
		clear(so.storage)
	}
	if len(so.blockOriginStorage) > storageRecycleMax {
		so.blockOriginStorage = nil
	} else {
		clear(so.blockOriginStorage)
	}
	so.dirtyKeys = so.dirtyKeys[:0]
	so.sawNonZeroCommitted = false
	stateObjectPool.Put(so)
}

// newObject creates a state object, reusing a pooled struct when one
// is available.
func newObject(db *IntraBlockState, address types.Address, data, original *account.StateAccount) *stateObject {
	var so *stateObject
	if stateObjectPooling {
		so = stateObjectPool.Get().(*stateObject)
	} else {
		so = freshStateObject()
	}
	if so.storage == nil {
		so.storage = make(map[types.Hash]slotEntry)
	}
	so.dirtyKeys = so.dirtyKeys[:0]
	so.db = db
	so.address = address
	so.code = nil
	so.fakeStorage = nil
	so.dirtyCode = false
	so.selfdestructed = false
	so.deleted = false
	so.created = false
	so.data = account.StateAccount{}
	so.original = account.StateAccount{}
	so.data.Copy(data)
	if !so.data.Initialised {
		so.data.Balance.SetUint64(0)
		so.data.Initialised = true
	}
	if so.data.CodeHash == (types.Hash{}) {
		so.data.CodeHash = emptyCodeHashH
	}
	// Empty Root is left as zero hash; storage trie is initialized lazily.
	so.original.Copy(original)
	return so
}

// EncodeRLP implements rlp.Encoder.
func (so *stateObject) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, so.data)
}

// setError remembers the first non-nil error it is called with.
func (so *stateObject) setError(err error) {
	if so.db.savedErr == nil {
		so.db.savedErr = err
	}
}

func (so *stateObject) markSelfdestructed() {
	so.selfdestructed = true
}

func (so *stateObject) touch() {
	so.db.journal.push(touchChange{
		account: &so.address,
	}.record())
	if so.address == ripemd {
		// Explicitly put it in the dirty-cache, which is otherwise generated from
		// flattened journals.
		so.db.journal.dirty(so.address)
	}
}

// GetState returns a value from account storage.
func (so *stateObject) GetState(key *types.Hash, out *uint256.Int) {
	// If the fake storage is set, only lookup the state here(in the debugging mode)
	if so.fakeStorage != nil {
		*out = so.fakeStorage[*key]
		return
	}
	if e, ok := so.storage[*key]; ok && e.epoch != 0 {
		*out = e.cur // written this block: latest value
		return
	}
	// Otherwise return the entry's original value
	so.GetCommittedState(key, out)
}

// GetCommittedState retrieves a value from the committed account storage trie.
func (so *stateObject) GetCommittedState(key *types.Hash, out *uint256.Int) {
	// If the fake storage is set, only lookup the state here(in the debugging mode)
	if so.fakeStorage != nil {
		*out = so.fakeStorage[*key]
		return
	}
	// If we have the original value cached, return that. A slot written in
	// an earlier transaction is committed at its last write whether or not
	// its pre-write value was ever read — the old per-tx dirty->origin copy
	// established that unconditionally, so a write in a past epoch counts as
	// known here.
	if e, ok := so.storage[*key]; ok {
		if e.epoch == so.currentEpoch() {
			if e.known {
				*out = e.committed // written this tx: value at tx start
				return
			}
		} else if e.known || e.epoch != 0 {
			*out = e.cur // last write was an earlier tx, or a DB read
			return
		}
	}
	if so.created {
		out.Clear()
		return
	}
	// If this address was wiped by a PRIOR transaction in the current
	// block, the buffered/MDBX storage from earlier blocks is stale.
	// Live storageWipes is NOT consulted here: pre-Cancun keeps a self-
	// destructed contract readable for the rest of the same tx, so the
	// wipe only takes effect across the tx boundary (FinalizeTx promotes
	// storageWipes into priorTxWipes). The len-guard short-circuits the
	// SLOAD hot path on the common case where nothing has been wiped.
	if len(so.db.priorTxWipes) > 0 {
		if _, wiped := so.db.priorTxWipes[so.address]; wiped {
			out.Clear()
			so.cacheCommittedState(key, out)
			return
		}
	}

	if so.db != nil && so.db.snap != nil && !so.db.snap.CanWrite() {
		// Load from DB in case it is missing.
		enc, err := so.db.snap.ReadAccountStorage(so.address, key)
		if err != nil {
			so.setError(err)
			out.Clear()
			return
		}
		if enc != nil {
			out.SetBytes(enc)
		} else {
			out.Clear()
		}
		so.cacheCommittedState(key, out)
		return
	}
	// Load from DB in case it is missing.
	enc, err := so.db.stateReader.ReadAccountStorage(so.address, key)
	if err != nil {
		so.setError(err)
		out.Clear()
		return
	}
	if enc != nil {
		if so.db.snap != nil && so.db.snap.CanWrite() {
			so.db.snap.AddStorage(so.address, key, enc)
		}
		out.SetBytes(enc)
	} else {
		out.Clear()
	}
	so.cacheCommittedState(key, out)
}

// cacheCommittedState always retains the value needed by later transactions
// in this block. blockOriginStorage is a second copy used only when producing
// the final block write set/root, so validation-only callers can omit it.
func (so *stateObject) cacheCommittedState(key *types.Hash, value *uint256.Int) {
	e := so.storage[*key]
	if e.epoch == so.currentEpoch() && e.epoch != 0 {
		// Written this tx before its committed value was read (only the
		// revert path can do that): the write stays in cur.
		e.committed = *value
	} else {
		e.cur = *value
	}
	e.known = true
	if !value.IsZero() {
		so.sawNonZeroCommitted = true
	}
	so.storage[*key] = e
	if so.db == nil || !so.db.discardBlockChanges {
		if so.blockOriginStorage == nil {
			so.blockOriginStorage = make(Storage)
		}
		if _, have := so.blockOriginStorage[*key]; !have {
			so.blockOriginStorage[*key] = *value
		}
	}
}

// SetState updates a value in account storage.
func (so *stateObject) SetState(key *types.Hash, value uint256.Int) {
	// If the fake storage is set, put the temporary state update here.
	if so.fakeStorage != nil {
		so.db.journal.push(fakeStorageChange{
			account:  &so.address,
			key:      *key,
			prevalue: so.fakeStorage[*key],
		}.record())
		so.fakeStorage[*key] = value
		return
	}
	// If the new value is the same as old, don't set
	var prev uint256.Int
	so.GetState(key, &prev)
	if prev == value {
		return
	}
	// New value is different, update and journal the change
	so.db.journal.push(storageChange{
		account:  &so.address,
		key:      *key,
		prevalue: prev,
	}.record())
	so.setState(key, value)
}

// SetStorage replaces the entire state storage with the given one.
//
// After this function is called, all original state will be ignored and state
// lookup only happens in the fake state storage.
//
// Note this function should only be used for debugging purpose.
func (so *stateObject) SetStorage(storage Storage) {
	// Allocate fake storage if it's nil.
	if so.fakeStorage == nil {
		so.fakeStorage = make(Storage)
	}
	for key, value := range storage {
		so.fakeStorage[key] = value
	}
	// Don't bother journal since this function should only be used for
	// debugging and the `fake` storage won't be committed to database.
}

func (so *stateObject) setState(key *types.Hash, value uint256.Int) {
	e := so.storage[*key]
	epoch := so.currentEpoch()
	if e.epoch == 0 {
		so.dirtyKeys = append(so.dirtyKeys, *key)
	}
	if e.epoch != epoch {
		// First write in this tx: what cur holds now is the value at tx start
		// — a cached read, or the last write of an earlier tx (which the old
		// per-tx copy would have promoted). A write with neither leaves the
		// committed value unknown, exactly as originStorage stayed empty.
		if e.known || e.epoch != 0 {
			e.committed = e.cur
			e.known = true
		}
		e.epoch = epoch
	}
	e.cur = value
	so.storage[*key] = e
}

// updateTrie writes cached storage modifications into the object's storage trie.
//
// Args passed to WriteAccountStorage by value (was: by pointer); the
// pointer form forced &range-vars to escape to the heap on every
// iteration through the StateWriter interface. By-value passing copies
// 32 B × 3 args per call — far cheaper than three heap allocs + GC
// scan that would otherwise happen on every dirty slot.
func (so *stateObject) updateTrie(stateWriter StateWriter) error {
	// Promotion of this tx's writes into the committed view is no longer a
	// copy: IntraBlockState.FinalizeTx advances storageEpoch after this, and
	// GetCommittedState reads cur for any entry whose epoch is in the past.
	// What remains is reporting every block-dirty slot to the writer, exactly
	// as before.
	for _, key := range so.dirtyKeys {
		e := so.storage[key]
		original := so.blockOriginStorage[key] // zero when never captured, as before
		if err := stateWriter.WriteAccountStorage(so.address, key, original, e.cur); err != nil {
			return err
		}
	}
	return nil
}

func (so *stateObject) printTrie() {
	for _, key := range so.dirtyKeys {
		value := so.storage[key].cur
		log.Trace("WriteAccountStorage", "address", so.address, "key", key, "value", value.Hex())
	}
}

// AddBalance adds amount to so's balance.
// It is used to add funds to the destination account of a transfer.
func (so *stateObject) AddBalance(amount *uint256.Int) {
	// EIP-161: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	if amount.IsZero() {
		if so.empty() {
			so.touch()
		}
		return
	}
	// setBalance copies the value, so the sum can live on the stack: this
	// path ran once per value transfer and was 42 GiB of allocation per 200k
	// dense blocks together with SubBalance.
	var sum uint256.Int
	sum.Add(so.Balance(), amount)
	so.SetBalance(&sum)
}

// SubBalance removes amount from so's balance.
// It is used to remove funds from the origin account of a transfer.
func (so *stateObject) SubBalance(amount *uint256.Int) {
	if amount.IsZero() {
		return
	}
	var diff uint256.Int
	diff.Sub(so.Balance(), amount)
	so.SetBalance(&diff)
}

func (so *stateObject) SetBalance(amount *uint256.Int) {
	so.db.journal.push(balanceChange{
		account: &so.address,
		prev:    so.data.Balance,
	}.record())
	so.setBalance(amount)
}

func (so *stateObject) setBalance(amount *uint256.Int) {
	so.data.Balance.Set(amount)
	so.data.Initialised = true
}

// ReturnGas is a no-op retained for interface compatibility.
func (so *stateObject) ReturnGas(_ *big.Int) {}

// Address returns the address of the contract/account.
func (so *stateObject) Address() types.Address {
	return so.address
}

// Code returns the contract code associated with this object, if any.
func (so *stateObject) Code() []byte {
	if so.code != nil {
		return so.code
	}
	if bytes.Equal(so.CodeHash(), emptyCodeHash) {
		return nil
	}
	code, err := so.db.stateReader.ReadAccountCode(so.Address(), types.BytesToHash(so.CodeHash()))
	if err != nil {
		so.setError(fmt.Errorf("can't load code hash %x: %w", so.CodeHash(), err))
	}
	so.code = code
	return code
}

func (so *stateObject) SetCode(codeHash types.Hash, code []byte) {
	prevcode := so.Code()
	so.db.journal.push(codeChange{
		account:  &so.address,
		prevhash: so.data.CodeHash,
		prevcode: prevcode,
	}.record())
	so.setCode(codeHash, code)
}

func (so *stateObject) setCode(codeHash types.Hash, code []byte) {
	so.code = code
	so.data.CodeHash = codeHash
	so.dirtyCode = true
}

func (so *stateObject) SetNonce(nonce uint64) {
	so.db.journal.push(nonceChange{
		account: &so.address,
		prev:    so.data.Nonce,
	}.record())
	so.setNonce(nonce)
}

func (so *stateObject) setNonce(nonce uint64) {
	so.data.Nonce = nonce
}

func (so *stateObject) CodeHash() []byte {
	return so.data.CodeHash[:]
}

func (so *stateObject) Balance() *uint256.Int {
	return &so.data.Balance
}

func (so *stateObject) Nonce() uint64 {
	return so.data.Nonce
}

// Value returns zero. This method exists to satisfy the vm.ContractRef interface.
func (so *stateObject) Value() *big.Int {
	return big.NewInt(0)
}
