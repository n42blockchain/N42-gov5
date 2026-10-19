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

// Package parallel implements Block-STM optimistic parallel transaction execution.
//
// Block-STM executes all transactions in a block concurrently, each writing
// to a multi-version data store (MVS). After execution, transactions are
// validated in order: if tx[i] read a value that was later written by tx[j<i],
// tx[i] must re-execute. This repeats until all transactions are consistent.
package parallel

import (
	"encoding/binary"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// LocationKey uniquely identifies a state location that can be read or written.
// It represents either an account-level property or a storage slot.
type LocationKey struct {
	Address types.Address
	Field   LocationField
	Slot    types.Hash // only used for FieldStorage
}

// LocationField identifies which part of an account is being accessed.
type LocationField uint8

const (
	FieldBalance     LocationField = 0
	FieldNonce       LocationField = 1
	FieldCode        LocationField = 2
	FieldCodeHash    LocationField = 3
	FieldStorage     LocationField = 4
	FieldSuicide     LocationField = 5
	FieldExist       LocationField = 6
	FieldCodeSize    LocationField = 7
	FieldIncarnation LocationField = 8
	// FieldStorageWipe is a per-address marker (no slot) recorded whenever an
	// address's whole storage is wiped — SELFDESTRUCT, CREATE-on-existing, or
	// fresh CREATE. It shadows storage reads of that address so a concurrent /
	// later tx never sees stale pre-wipe slot data. Mirrors the mvKeyTagWipe
	// poison-write in modules/state's MV adapter (the production parallel path).
	FieldStorageWipe LocationField = 9
)

// versionedValue is a single write to a location at a given transaction index.
type versionedValue struct {
	txIndex     int
	incarnation uint32 // incarnation of the writing tx
	value       []byte // serialized value; nil means "deleted"
	// isDelta makes this version a balance increment of delta over the
	// composition of the versions before it (value is then unused). The
	// delta is held by value: a pointer cost an allocation per credit.
	isDelta bool
	delta   uint256.Int
}

// mvEntry holds all versions for one location, sorted by txIndex ascending.
// key and hash are set before the entry is published in its shard's table
// and never change.
type mvEntry struct {
	mu       sync.RWMutex
	versions []versionedValue
	key      LocationKey
	hash     uint64
	// fulls counts the versions that are full writes (not deltas). It
	// changes under mu and is read without it: a reader that sees zero
	// knows no transaction has stored a full value here -- only deltas,
	// or nothing -- without the lock or a scan of the versions.
	fulls atomic.Int32
	// base is the block's base-cache entry of the account (FieldBalance
	// keys), once a read through this entry looked it up (fast2): later
	// base reads of the account skip the cache's lookup. The base state does
	// not change during the block, so the link never goes stale.
	base atomic.Pointer[baseEntry]
}

// noFullWrites reports whether no transaction has stored a full value at
// the entry (it has only balance deltas, or no versions); a nil entry has
// none. Read without the entry's lock, so it may miss a concurrent write,
// which validation catches like any read that raced a write.
func (e *mvEntry) noFullWrites() bool {
	return e == nil || e.fulls.Load() == 0
}

// MVS (Multi-Version Store) is the central data structure for Block-STM.
// It maintains a versioned view of all state locations, keyed by (address, field, slot).
// Each transaction writes to its own "version", and reads find the most recent
// write from a preceding transaction.
type MVS struct {
	shards [mvsShards]mvsShard
	// va, when set, holds the version lists (version_arena.go): an
	// executor's store, reused across blocks. Nil: lists grow by append.
	va *versionArena
}

// mvsShards splits the location index so inserts of new locations rarely
// meet. Lookups take no lock at all: each shard is an insert-only
// open-addressing table read with atomic loads. The shard RWMutex it
// replaces cost every lookup an atomic add on the reader count's cache line
// (two shards shared a line), 13% of the 163k-transfer exec wave in the P3b
// profile, and its writers blocked the readers queued behind them.
const mvsShards = 256

// mvsShardSlots is a shard table's initial size (a power of two).
const mvsShardSlots = 64

type mvsShard struct {
	mu  sync.Mutex // serialises inserts and table growth; lookups skip it
	tab atomic.Pointer[mvTable]
	n   int // entries in tab, under mu
	_   [40]byte
}

// mvTable is an insert-only linear-probing table. A slot, once set, never
// changes; growth builds a new table under the shard's mutex and publishes
// it whole. A lookup that loaded the old table may miss a location inserted
// after the swap, which orders the read before that write -- as a read that
// took the lock first would have been; Block-STM validation catches it like
// any read that raced a lower transaction's write.
type mvTable struct {
	mask  uint64
	slots []mvSlot
}

// mvSlot is one table slot: the entry and its key's hash. The hash sits in
// the slot so a probe passes the slots of other keys without loading their
// entries -- each of those loads was a cache miss, and at the table's load
// factor a lookup probes about two slots, an absent key's about three. It
// is stored before the entry, so a reader that sees the entry sees it.
type mvSlot struct {
	hash atomic.Uint64
	e    atomic.Pointer[mvEntry]
}

// Load returns the slot's entry (nil when free).
func (s *mvSlot) Load() *mvEntry { return s.e.Load() }

func newMVTable(size int) *mvTable {
	return &mvTable{mask: uint64(size - 1), slots: make([]mvSlot, size)}
}

// find returns the entry for key, or nil.
func (t *mvTable) find(key *LocationKey, h uint64) *mvEntry {
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		s := &t.slots[i]
		e := s.e.Load()
		if e == nil {
			return nil
		}
		if s.hash.Load() == h && e.key == *key {
			return e
		}
	}
}

// place stores e in its first free slot (the caller holds the shard mutex
// and has checked e's key is absent).
func (t *mvTable) place(e *mvEntry) {
	for i := e.hash & t.mask; ; i = (i + 1) & t.mask {
		if s := &t.slots[i]; s.e.Load() == nil {
			s.hash.Store(e.hash)
			s.e.Store(e)
			return
		}
	}
}

// each calls fn for every entry in the table.
func (t *mvTable) each(fn func(e *mvEntry)) {
	for i := range t.slots {
		if e := t.slots[i].Load(); e != nil {
			fn(e)
		}
	}
}

// NewMVS creates a new empty multi-version store.
func NewMVS() *MVS {
	m := &MVS{}
	for i := range m.shards {
		m.shards[i].tab.Store(newMVTable(mvsShardSlots))
	}
	return m
}

// locationHash hashes every byte of a location key. The top byte picks the
// shard, the low bits the table slot.
func locationHash(key *LocationKey) uint64 {
	a := &key.Address
	h := binary.LittleEndian.Uint64(a[0:8])*0x9E3779B97F4A7C15 ^
		binary.LittleEndian.Uint64(a[8:16])*0xC2B2AE3D27D4EB4F ^
		uint64(binary.LittleEndian.Uint32(a[16:20]))*0x165667B19E3779F9 ^
		uint64(key.Field)*0xD6E8FEB86659FD93
	s := &key.Slot
	h ^= binary.LittleEndian.Uint64(s[0:8])*0xFF51AFD7ED558CCD ^
		binary.LittleEndian.Uint64(s[8:16])*0xC4CEB9FE1A85EC53 ^
		binary.LittleEndian.Uint64(s[16:24])*0x94D049BB133111EB ^
		binary.LittleEndian.Uint64(s[24:32])*0xBF58476D1CE4E5B9
	h ^= h >> 31
	h *= 0x9E3779B97F4A7C15
	h ^= h >> 29
	return h
}

// shard picks the shard for a key hash.
func (m *MVS) shard(h uint64) *mvsShard {
	return &m.shards[h>>56]
}

// lookup returns the entry for key, or nil, without taking a lock.
func (m *MVS) lookup(key *LocationKey) *mvEntry {
	h := locationHash(key)
	return m.shard(h).tab.Load().find(key, h)
}

// getOrCreateEntry returns the mvEntry for a key, creating it if needed.
func (m *MVS) getOrCreateEntry(key LocationKey) *mvEntry {
	h := locationHash(&key)
	sh := m.shard(h)
	if e := sh.tab.Load().find(&key, h); e != nil {
		return e
	}

	sh.mu.Lock()
	defer sh.mu.Unlock()
	// Double-check after acquiring the mutex: the table may have grown.
	t := sh.tab.Load()
	if e := t.find(&key, h); e != nil {
		return e
	}
	if (sh.n+1)*4 > len(t.slots)*3 {
		g := newMVTable(len(t.slots) * 2)
		t.each(g.place)
		sh.tab.Store(g)
		t = g
	}
	e := &mvEntry{key: key, hash: h}
	t.place(e)
	sh.n++
	return e
}

// before returns how many versions precede txIndex (e.mu held). Short lists,
// the usual case, are scanned from the end: a transaction mostly reads a
// location whose writers all precede it, so the scan stops at once, and it
// skips sort.Search's closure calls.
func (e *mvEntry) before(txIndex int) int {
	n := len(e.versions)
	if oneLookup && n > 16 && e.versions[n-1].txIndex < txIndex {
		// Every version precedes txIndex: the common case at execution
		// time for a long sender chain, whose binary search was a cache
		// miss per probe.
		return n
	}
	if n <= 16 {
		for n > 0 && e.versions[n-1].txIndex >= txIndex {
			n--
		}
		return n
	}
	return e.search(txIndex)
}

// beforeAt is before(txIndex) with a guess at, the count a recent call
// returned (negative: none). The versions are sorted by strictly ascending
// txIndex, so at is before(txIndex) exactly when every version below it
// precedes txIndex and the one at it (if any) does not; the guess is
// checked against that, and a stale one falls back to the search
// (e.mu held).
func (e *mvEntry) beforeAt(txIndex, at int) int {
	if at >= 0 && at <= len(e.versions) && (at == 0 || e.versions[at-1].txIndex < txIndex) &&
		(at == len(e.versions) || e.versions[at].txIndex >= txIndex) {
		return at
	}
	return e.before(txIndex)
}

// search is sort.Search for the first version at or after txIndex, without
// the closure call per probe (e.mu held).
func (e *mvEntry) search(txIndex int) int {
	lo, hi := 0, len(e.versions)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if e.versions[m].txIndex < txIndex {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// Write records that transaction txIndex (at given incarnation) wrote value to the given location.
// If txIndex already has a write here, it is overwritten (for re-execution).
// The value is copied.
func (m *MVS) Write(key LocationKey, txIndex int, incarnation uint32, value []byte) {
	var vc []byte
	if value != nil {
		vc = make([]byte, len(value))
		copy(vc, value)
	}
	m.writeOwned(key, txIndex, incarnation, vc)
}

// writeOwned is Write adopting value without a copy: the caller hands over
// a slice nobody mutates afterwards (the executor's write descriptors,
// which the state writer encodes fresh per write).
func (m *MVS) writeOwned(key LocationKey, txIndex int, incarnation uint32, value []byte) {
	m.writeOwnedHint(m.getOrCreateEntry(key), txIndex, incarnation, value, 0, -1)
}

// writeOwnedHint is writeOwned on the location's entry e that sizes an
// empty version list for hint versions (the writes the caller expects at
// this location in the block). at guesses txIndex's slot (slotAt).
func (m *MVS) writeOwnedHint(e *mvEntry, txIndex int, incarnation uint32, value []byte, hint, at int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if hint > 1 && cap(e.versions) == 0 && m.va == nil {
		e.versions = make([]versionedValue, 0, hint)
	}

	idx := e.slotAt(txIndex, at)
	if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
		// Overwrite existing.
		v := &e.versions[idx]
		if v.isDelta {
			e.fulls.Add(1)
		}
		v.value = value
		v.incarnation = incarnation
		v.isDelta = false
		v.delta.Clear()
	} else {
		// Insert at sorted position.
		m.growVersions(e, hint)
		copy(e.versions[idx+1:], e.versions[idx:])
		e.versions[idx] = versionedValue{txIndex: txIndex, incarnation: incarnation, value: value}
		e.fulls.Add(1)
	}
}

// slot returns the index of txIndex's version, or where it is inserted
// (e.mu held). A worker writes in index order, so the usual slot is the end.
func (e *mvEntry) slot(txIndex int) int {
	n := len(e.versions)
	if n == 0 || e.versions[n-1].txIndex < txIndex {
		return n
	}
	return e.search(txIndex)
}

// slotAt is slot with a guess at (negative: none): the slot is the count of
// versions before txIndex, so a read's beforeAt count serves as the guess
// for the same transaction's write (e.mu held).
func (e *mvEntry) slotAt(txIndex, at int) int {
	if at >= 0 {
		n := len(e.versions)
		if n == 0 || e.versions[n-1].txIndex < txIndex {
			return n
		}
		return e.beforeAt(txIndex, at)
	}
	return e.slot(txIndex)
}

// WriteDelta records that transaction txIndex added delta to the account at
// key. It replaces any earlier write of txIndex at the key.
func (m *MVS) WriteDelta(key LocationKey, txIndex int, incarnation uint32, delta *uint256.Int) {
	m.writeDelta(m.getOrCreateEntry(key), txIndex, incarnation, delta, -1)
}

// writeDelta is WriteDelta on the location's entry e; at guesses txIndex's
// slot (slotAt).
func (m *MVS) writeDelta(e *mvEntry, txIndex int, incarnation uint32, delta *uint256.Int, at int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	idx := e.slotAt(txIndex, at)
	if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
		v := &e.versions[idx]
		if !v.isDelta {
			e.fulls.Add(-1)
		}
		v.value = nil
		v.incarnation = incarnation
		v.isDelta = true
		v.delta.Set(delta)
	} else {
		want := 0
		if fast2 && cap(e.versions) == 0 {
			// A credited account is usually credited again in the block:
			// start its list at deltaListCap instead of growing it from one.
			if m.va == nil {
				e.versions = make([]versionedValue, 0, deltaListCap)
			}
			want = deltaListCap
		}
		m.growVersions(e, want)
		copy(e.versions[idx+1:], e.versions[idx:])
		e.versions[idx] = versionedValue{txIndex: txIndex, incarnation: incarnation, isDelta: true, delta: *delta}
	}
}

// deltaListCap is the capacity of a version list started by a balance
// delta (fast2): the bench's recipients take ~7 credits a block, and the
// list grew 1, 2, 4, 8 -- four allocations and three copies.
const deltaListCap = 4

// ReadAccount returns the latest full write before txIndex (value, writer,
// incarnation, found) and the sum of the balance deltas written between
// that full write (or the start) and txIndex, nil when there are none.
func (m *MVS) ReadAccount(key LocationKey, txIndex int) (full []byte, fullTx int, fullInc uint32, found bool, delta *uint256.Int) {
	var d uint256.Int
	full, fullTx, fullInc, found, hasDelta := m.readAccountInto(&key, txIndex, &d)
	if hasDelta {
		delta = &d
	}
	return full, fullTx, fullInc, found, delta
}

// readAccountInto is ReadAccount summing the deltas into the caller's
// delta (overwritten; meaningful only when hasDelta), so the hot readers
// allocate nothing.
func (m *MVS) readAccountInto(key *LocationKey, txIndex int, delta *uint256.Int) (full []byte, fullTx int, fullInc uint32, found, hasDelta bool) {
	full, fullTx, fullInc, found, hasDelta, _ = m.lookup(key).readAccount(txIndex, -1, delta)
	return full, fullTx, fullInc, found, hasDelta
}

// readAccount is readAccountInto on a looked-up entry; a nil entry has no
// versions. at guesses the count of versions before txIndex (beforeAt;
// negative: none), and the count found is returned as n (0 for a nil
// entry).
func (e *mvEntry) readAccount(txIndex, at int, delta *uint256.Int) (full []byte, fullTx int, fullInc uint32, found, hasDelta bool, n int) {
	if e == nil {
		return nil, 0, 0, false, false, 0
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	n = e.beforeAt(txIndex, at)
	for i := n - 1; i >= 0; i-- {
		v := &e.versions[i]
		if v.isDelta {
			if !hasDelta {
				delta.Set(&v.delta)
				hasDelta = true
			} else {
				delta.Add(delta, &v.delta)
			}
			continue
		}
		return v.value, v.txIndex, v.incarnation, true, hasDelta, n
	}
	return nil, 0, 0, false, hasDelta, n
}

// Read returns the value written by the latest transaction before txIndex.
// Account keys with deltas must use ReadAccount; here a delta version is
// reported as its composition over the full write before it.
// Returns (value, writerTxIndex, writerIncarnation, true) if found,
// or (nil, 0, 0, false) if no preceding transaction has written to this location.
func (m *MVS) Read(key LocationKey, txIndex int) ([]byte, int, uint32, bool) {
	e := m.lookup(&key)
	if e == nil {
		return nil, 0, 0, false
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Find the latest version with txIndex < callerTxIndex.
	idx := e.before(txIndex)

	if idx == 0 {
		return nil, 0, 0, false
	}

	v := &e.versions[idx-1]
	if !v.isDelta {
		return v.value, v.txIndex, v.incarnation, true
	}
	var delta uint256.Int
	for i := idx - 1; i >= 0; i-- {
		w := &e.versions[i]
		if w.isDelta {
			delta.Add(&delta, &w.delta)
			continue
		}
		return composeAccount(w.value, &delta), w.txIndex, w.incarnation, true
	}
	return composeAccount(nil, &delta), -1, 0, true
}

// Delete removes all writes by txIndex from the store (used before re-execution).
func (m *MVS) Delete(key LocationKey, txIndex int) {
	e := m.lookup(&key)
	if e == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.removeLocked(txIndex)
}

// removeLocked removes txIndex's version, if any (e.mu held).
func (e *mvEntry) removeLocked(txIndex int) {
	idx := sort.Search(len(e.versions), func(i int) bool {
		return e.versions[i].txIndex >= txIndex
	})
	if idx < len(e.versions) && e.versions[idx].txIndex == txIndex {
		if !e.versions[idx].isDelta {
			e.fulls.Add(-1)
		}
		e.versions = append(e.versions[:idx], e.versions[idx+1:]...)
	}
}

// ApplyAll iterates over all MVS entries and calls fn with the final
// (highest txIndex < numTxs) value for each location. This is used after
// parallel execution to replay the validated state to the real StateWriter.
//
// An account with a full write gets the composition of that write and the
// deltas after it; an account with only deltas gets a nil value and their
// sum as delta.
func (m *MVS) ApplyAll(numTxs int, fn func(key LocationKey, value []byte, delta *uint256.Int) error) error {
	return m.ApplyShards(numTxs, 0, MVSShards, fn)
}

// MVSShards is the number of index shards ApplyShards ranges over.
const MVSShards = mvsShards

// ApplyShards is ApplyAll over the shards [lo, hi): disjoint ranges visit
// disjoint locations, so they can run concurrently (each with its own fn),
// and the ranges in order visit every location in ApplyAll's order.
func (m *MVS) ApplyShards(numTxs, lo, hi int, fn func(key LocationKey, value []byte, delta *uint256.Int) error) error {
	for i := lo; i < hi; i++ {
		t := m.shards[i].tab.Load()
		for j := range t.slots {
			e := t.slots[j].Load()
			if e == nil {
				continue
			}
			e.mu.RLock()
			// Find the latest version with txIndex < numTxs.
			idx := sort.Search(len(e.versions), func(i int) bool {
				return e.versions[i].txIndex >= numTxs
			})
			if idx == 0 {
				e.mu.RUnlock()
				continue
			}
			var delta *uint256.Int
			var val []byte
			fullFound := false
			for i := idx - 1; i >= 0; i-- {
				v := &e.versions[i]
				if v.isDelta {
					if delta == nil {
						delta = new(uint256.Int)
					}
					delta.Add(delta, &v.delta)
					continue
				}
				// Copy value while holding the lock to avoid data race.
				if v.value != nil {
					val = make([]byte, len(v.value))
					copy(val, v.value)
				}
				fullFound = true
				break
			}
			e.mu.RUnlock()
			if fullFound {
				if err := fn(e.key, composeAccount(val, delta), nil); err != nil {
					return err
				}
			} else if err := fn(e.key, nil, delta); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteAll removes all writes by txIndex across all locations.
func (m *MVS) DeleteAll(txIndex int) {
	for i := range m.shards {
		m.shards[i].tab.Load().each(func(e *mvEntry) {
			e.mu.Lock()
			e.removeLocked(txIndex)
			e.mu.Unlock()
		})
	}
}
