// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// PlainStateBuffer: write buffer plus lock-free read cache for hot EVM.
// Maintains dirty account/storage/code maps plus contractWipes and
// wipedStorage sets for MDBX storage purges on flush. Read paths use
// sync.Map-backed readAccounts/readStorage/readCode caches so hot EVM
// lookups avoid locks entirely; accountCacheEntry wraps nil-on-absent
// cached accounts, storageEntry carries raw slot bytes and atomic
// hits/misses counters expose cache effectiveness to metrics.

package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

var deletedSentinel = []byte{}

type storageEntry struct {
	value []byte
}

// accountCacheEntry wraps a cached account. The sync.Map stores interface{},
// so we use a pointer type. nil *accountCacheEntry = key exists but absent.
type accountCacheEntry struct {
	acct *account.StateAccount
}

// CacheBudget configures byte budgets for the three read-cache LRUs.
// Defaults are tuned for a 5M-25M-block read-heavy workload on a 128 GB
// machine where PlainState reads are the dominant cost: each EVM block
// issues ~1000 SLOAD/account fetches and a 1pp hit-rate improvement
// translates to a measurable wall-clock saving over a full chain replay.
type CacheBudget struct {
	AccountBytes int64 // LRU byte budget for accounts (default 4 GB)
	StorageBytes int64 // LRU byte budget for storage slots (default 8 GB)
	CodeBytes    int64 // LRU byte budget for contract bytecode (default 1 GB)
}

// DefaultCacheBudget returns the recommended sizes for the executor's
// hot read-cache tier on a 128 GB host.
//
// Sizing rationale (calibrated against the 11M-block reference run that
// shows blk/s ~71 with cacheHit%=88%, ~1300 SLOADs per block flowing into
// MDBX as misses):
//
//   - Storage gets the bulk of the budget. Hot data analysis shows the
//     storage long tail is wide (73% of slots are 1-write, then a heavy
//     11K+ write tier of DeFi pools), and the read-heavy 5M-25M block
//     range is dominated by per-block SLOAD bursts that touch a wider
//     working set than the account side.
//   - Accounts get a smaller slice — Top 5% of accounts cover most
//     traffic, and account values are larger per entry (V2 ~30 bytes
//     each), so a moderate budget covers the active set.
//   - Code is small: only ~50K unique deployed contracts ever appear in
//     a typical hot block, and cached bytecode is reusable across calls.
//
// Total ~5.5 GB (~4% of RAM on a 128 GB host). With S3-FIFO admission
// (see s3fifo.go) the account and storage tiers are scan-resistant —
// the per-commit-interval RefreshLRU dump (~770K one-touch writes) no
// longer pollutes the main LRU body, so a much smaller budget hits
// the same or higher rates as the previous 28.5 GB pure-LRU setup.
//
// Sizing rationale (block 4.96M, post-Constantinople DeFi era):
//   - Account hot working set ~500K entries (precompiles + top tokens
//     + active wallets) → 1 GB gives ~7M-entry headroom for 99%+
//   - Storage hot working set ~10M slots (DeFi pool reserves, hot
//     allowances, oracle prices) → 4 GB gives 22M-entry headroom for
//     95-97% with S3-FIFO scan resistance
//   - Code: 50K unique deployed contracts × ~5KB avg = 250 MB; 512 MB
//     keeps the existing 100% hit rate with comfortable margin
//
// Code stays on byteLRU because there is no scan problem (every code
// blob is hash-addressed and reused across many calls).
func DefaultCacheBudget() CacheBudget {
	return CacheBudget{
		AccountBytes: 1 << 30,  // 1 GB  S3-FIFO
		StorageBytes: 12 << 30, // 12 GB S3-FIFO
		CodeBytes:    512 << 20, // 512 MB byteLRU
	}
}

// Per-entry overhead estimates for byte accounting. Real Go map +
// container/list overhead is highly implementation-dependent; these are
// generous round numbers chosen to keep us well below the byte budget
// even as runtime data structures bloat.
const (
	cacheOverheadPerEntry = 96 // map header + list.Element + pointer slop
	storageCompositeKeyLen = 52
	addrKeyLen             = 20
	hashKeyLen             = 32
)

// BufferSnapshot is an immutable view of a write buffer at handoff time.
// SnapshotForFlush moves the active maps into a snapshot, leaving the
// PlainStateBuffer with fresh empty maps. The snapshot is read-only
// thereafter; the background flusher iterates it to write to MDBX, and
// the reader path falls through to it for reads of keys that were dirty
// at handoff time but whose MDBX commit hasn't completed yet.
//
// Concurrent access pattern:
//   - Single producer: SnapshotForFlush (executor goroutine)
//   - Single mutator: ApplyTo (background flusher goroutine, READ-ONLY on
//     the maps; only iterates and copies values to MDBX)
//   - Multiple readers: BufferedPlainStateReader checks the snapshot
//     after the active buffer
//
// Maps are not deeply copied; the snapshot OWNS the original maps and
// the active PlainStateBuffer gets new empty ones. This makes the
// handoff O(1).
type BufferSnapshot struct {
	accounts      map[types.Address][]byte
	storage       map[types.Address]map[types.Hash]storageEntry
	code          map[types.Hash][]byte
	contractCode  map[string][]byte
	contractWipes []types.Address
	wipedStorage  map[types.Address]struct{}
}

// PlainStateBuffer holds write buffer + bounded LRU read caches.
type PlainStateBuffer struct {
	// Write buffer (single-writer, executor only).
	accounts       map[types.Address][]byte
	storage        map[types.Address]map[types.Hash]storageEntry
	code           map[types.Hash][]byte
	contractCode   map[string][]byte
	incarnationMap map[types.Address][]byte
	contractWipes  []types.Address            // addresses needing MDBX storage wipe on flush
	wipedStorage   map[types.Address]struct{} // addresses whose storage was wiped — reads bypass cache/MDBX

	// inFlight is the snapshot currently being written to MDBX by the
	// background flusher (if any). Reader path: active buffer →
	// inFlight.Load() → LRU → MDBX. The pointer is replaced atomically
	// at every handoff; the previous snapshot is dropped (and GC'd) once
	// the next handoff installs a fresher one and the main thread has
	// rotated to a new MDBX tx whose snapshot includes the previous
	// background commit.
	inFlight atomic.Pointer[BufferSnapshot]

	// Read cache: per-kind byte budget so a hot storage workload can't
	// starve the account cache. The composite storage key is
	// addr(20) || slot(32) = 52 bytes; we keep storage as a flat map
	// (no per-address sub-map) to make eviction account for individual
	// slots rather than whole-address groups.
	//
	// Account/storage use S3-FIFO (scan-resistant 3-queue eviction)
	// because RefreshLRUForSnapshot dumps ~770K one-touch writes per
	// commit interval — pure LRU treated those as fresh hot entries
	// and evicted real hot data. Code stays on byteLRU: there is no
	// scan problem (code is hash-addressed, reused, immutable).
	readAccounts *s3FIFO[types.Address]
	readStorage  *s3FIFO[[storageCompositeKeyLen]byte]
	readCode     *byteLRU[types.Hash]

	hits   atomic.Uint64
	misses atomic.Uint64

	// Per-tier hit/miss counters. Used by the executor's progress log to
	// break down where the misses are concentrated (account/storage/code)
	// so the operator can decide which LRU budget to grow.
	acctHits, acctMisses atomic.Uint64
	stoHits, stoMisses   atomic.Uint64
	codeHits, codeMisses atomic.Uint64
}

// NewPlainStateBuffer creates a buffer with the default cache budget.
// For a custom budget use NewPlainStateBufferWithBudget.
func NewPlainStateBuffer() *PlainStateBuffer {
	return NewPlainStateBufferWithBudget(DefaultCacheBudget())
}

// NewPlainStateBufferWithBudget creates a buffer with the given LRU
// byte budgets. Pass zero in any field to fall back to the default for
// that tier.
func NewPlainStateBufferWithBudget(b CacheBudget) *PlainStateBuffer {
	def := DefaultCacheBudget()
	if b.AccountBytes <= 0 {
		b.AccountBytes = def.AccountBytes
	}
	if b.StorageBytes <= 0 {
		b.StorageBytes = def.StorageBytes
	}
	if b.CodeBytes <= 0 {
		b.CodeBytes = def.CodeBytes
	}
	return &PlainStateBuffer{
		accounts:       make(map[types.Address][]byte, 4096),
		storage:        make(map[types.Address]map[types.Hash]storageEntry, 4096),
		code:           make(map[types.Hash][]byte, 64),
		contractCode:   make(map[string][]byte, 64),
		incarnationMap: make(map[types.Address][]byte),
		wipedStorage:   make(map[types.Address]struct{}),
		readAccounts:   newS3FIFO[types.Address](b.AccountBytes),
		readStorage:    newS3FIFO[[storageCompositeKeyLen]byte](b.StorageBytes),
		readCode:       newByteLRU[types.Hash](b.CodeBytes),
	}
}

func (b *PlainStateBuffer) CacheStats() (hits, misses uint64) {
	return b.hits.Load(), b.misses.Load()
}

// TierStats returns per-tier (hits, misses) for accounts, storage, and code.
// Hits include all 3 buffer layers (active, in-flight, LRU); misses are
// MDBX fall-throughs.
func (b *PlainStateBuffer) TierStats() (acctH, acctM, stoH, stoM, codeH, codeM uint64) {
	return b.acctHits.Load(), b.acctMisses.Load(),
		b.stoHits.Load(), b.stoMisses.Load(),
		b.codeHits.Load(), b.codeMisses.Load()
}

// LRUStats reports per-tier byte usage and entry counts.
func (b *PlainStateBuffer) LRUStats() (acctBytes, stoBytes, codeBytes int64, acctEntries, stoEntries, codeEntries int) {
	_, _, ab, ae := b.readAccounts.Stats()
	_, _, sb, se := b.readStorage.Stats()
	_, _, cb, ce := b.readCode.Stats()
	return ab, sb, cb, ae, se, ce
}

// CacheAccount populates the read cache (used by prefetcher).
// nil acct = "key exists, account is absent in PlainState"; the cache
// caches negative results too so a repeated lookup of a non-existent
// address doesn't keep hitting MDBX.
func (b *PlainStateBuffer) CacheAccount(address types.Address, acct *account.StateAccount) {
	cost := addrKeyLen + cacheOverheadPerEntry
	var v []byte
	if acct != nil {
		// Encode v2 form so subsequent reads can decode without
		// holding the original *StateAccount.
		v = acct.MarshalV2()
		cost += len(v)
	}
	b.readAccounts.Put(address, v, cost)
}

// CacheStorage populates the storage read cache (used by prefetcher).
// A nil/empty value caches the "slot is zero/absent" answer.
func (b *PlainStateBuffer) CacheStorage(address types.Address, key types.Hash, value []byte) {
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	cost := storageCompositeKeyLen + len(value) + cacheOverheadPerEntry
	b.readStorage.Put(ck, value, cost)
}

// SnapshotForFlush moves the active write buffer maps into a fresh
// BufferSnapshot, resets the active buffer to empty maps sized from the
// previous interval's actuals, AND installs the snapshot as in-flight
// so concurrent reader goroutines can still find dirty values during
// the background commit window.
//
// O(1): the maps are moved by pointer, not deep-copied.
//
// The snapshot is automatically installed as the in-flight pointer; the
// caller is responsible for calling snap.ApplyTo(tx) (sync or via the
// background flusher) and InvalidateLRUForSnapshot AFTER MDBX commit.
// At shutdown, ClearInFlight drops the pointer once the final flush is
// committed.
func (b *PlainStateBuffer) SnapshotForFlush() *BufferSnapshot {
	snap := &BufferSnapshot{
		accounts:      b.accounts,
		storage:       b.storage,
		code:          b.code,
		contractCode:  b.contractCode,
		contractWipes: b.contractWipes,
		wipedStorage:  b.wipedStorage,
	}
	// Pre-size the next interval's maps from this interval's actuals so
	// we don't pay 8+ rehash cycles per commit-interval at the 11M-block
	// scale (bufAccs ~900K, bufStos addrs ~50K).
	nextAcctCap := nextMapCap(len(b.accounts), 4096)
	nextStoAddrCap := nextMapCap(len(b.storage), 4096)
	nextCodeCap := nextMapCap(len(b.code), 64)
	nextCCodeCap := nextMapCap(len(b.contractCode), 64)
	b.accounts = make(map[types.Address][]byte, nextAcctCap)
	b.storage = make(map[types.Address]map[types.Hash]storageEntry, nextStoAddrCap)
	b.code = make(map[types.Hash][]byte, nextCodeCap)
	b.contractCode = make(map[string][]byte, nextCCodeCap)
	b.contractWipes = nil
	b.wipedStorage = make(map[types.Address]struct{})

	b.inFlight.Store(snap)
	return snap
}

// nextMapCap returns a capacity hint that is the previous size rounded
// up to the next pow2, with a floor. Avoids 8+ rehash cycles when the
// active map fills back to its previous size.
func nextMapCap(prev, floor int) int {
	want := prev * 2
	if want < floor {
		return floor
	}
	return want
}

// ClearInFlight drops the in-flight pointer (used at shutdown after the
// last bg flush has committed).
func (b *PlainStateBuffer) ClearInFlight() {
	b.inFlight.Store(nil)
}

// ApplyTo writes the snapshot's contents to MDBX in the same order and
// format as the legacy synchronous flush path. Sort-then-Put preserves
// MDBX B+-tree locality; storage wipes happen between accounts and
// storage so freshly-CREATEd contract slots aren't deleted by the
// subsequent address-prefix wipe.
//
// The snapshot maps are read-only after this call; the caller must not
// mutate them.
func (snap *BufferSnapshot) ApplyTo(tx kv.RwTx) error {
	// 1. Accounts: sort by address.
	acctAddrs := make([]types.Address, 0, len(snap.accounts))
	for addr := range snap.accounts {
		acctAddrs = append(acctAddrs, addr)
	}
	sort.Slice(acctAddrs, func(i, j int) bool {
		return bytes.Compare(acctAddrs[i][:], acctAddrs[j][:]) < 0
	})
	for _, addr := range acctAddrs {
		v := snap.accounts[addr]
		if len(v) == 0 {
			if err := tx.Delete(modules.Account, addr[:]); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Account, addr[:], v); err != nil {
				return err
			}
		}
	}

	// 2. Wipe storage for SELFDESTRUCT'd contracts.
	for _, addr := range snap.contractWipes {
		prefix := addr[:]
		cursor, err := tx.Cursor(modules.Storage)
		if err != nil {
			return err
		}
		var keysToDelete [][]byte
		for k, _, err := cursor.Seek(prefix); k != nil; k, _, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				return err
			}
			if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
				break
			}
			keysToDelete = append(keysToDelete, append([]byte{}, k...))
		}
		cursor.Close()
		for _, k := range keysToDelete {
			if err := tx.Delete(modules.Storage, k); err != nil {
				return err
			}
		}
	}

	// 3. Storage: sorted composite keys.
	type stoKV struct {
		key   []byte
		value []byte
	}
	stoCount := 0
	for _, slots := range snap.storage {
		stoCount += len(slots)
	}
	stoEntries := make([]stoKV, 0, stoCount)
	for addr, slots := range snap.storage {
		for hash, entry := range slots {
			compositeKey := modules.PlainGenerateCompositeStorageKey(addr[:], hash[:])
			stoEntries = append(stoEntries, stoKV{key: compositeKey, value: entry.value})
		}
	}
	sort.Slice(stoEntries, func(i, j int) bool {
		return bytes.Compare(stoEntries[i].key, stoEntries[j].key) < 0
	})
	for _, kv := range stoEntries {
		if len(kv.value) == 0 {
			if err := tx.Delete(modules.Storage, kv.key); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Storage, kv.key, kv.value); err != nil {
				return err
			}
		}
	}

	// 4. Code + ContractCode.
	codeHashes := make([]types.Hash, 0, len(snap.code))
	for hash := range snap.code {
		codeHashes = append(codeHashes, hash)
	}
	sort.Slice(codeHashes, func(i, j int) bool {
		return bytes.Compare(codeHashes[i][:], codeHashes[j][:]) < 0
	})
	for _, hash := range codeHashes {
		if err := tx.Put(modules.Code, hash[:], snap.code[hash]); err != nil {
			return err
		}
	}

	ccKeys := make([]string, 0, len(snap.contractCode))
	for prefix := range snap.contractCode {
		ccKeys = append(ccKeys, prefix)
	}
	sort.Strings(ccKeys)
	for _, prefix := range ccKeys {
		if err := tx.Put(modules.PlainContractCode, []byte(prefix), snap.contractCode[prefix]); err != nil {
			return err
		}
	}
	return nil
}

// RefreshLRUForSnapshot pre-populates the read cache with the snapshot's
// just-flushed values. Called from the background flusher AFTER the
// MDBX commit. Concurrent main-thread reads against the LRU are safe
// (LRU has its own mutex).
//
// Why Put instead of Delete: the previous design Deleted every dirty
// key on the theory that the LRU value might be stale after the commit.
// But the snapshot's value IS what just landed in MDBX, so it's
// authoritative — we can update the LRU instead of evicting it.
// Eviction forces the next read to round-trip to MDBX; refresh keeps
// the entire just-flushed working set hot. At the 11M-block scale this
// is ~800K keys per commit interval that would otherwise become cold
// misses on the next interval's first access.
//
// Wiped contracts (SELFDESTRUCT) are represented as per-slot tombstones
// in snap.storage with empty value, so they get refreshed as negative
// cache entries — exactly what we want.
func (b *PlainStateBuffer) RefreshLRUForSnapshot(snap *BufferSnapshot) {
	acctKeys := make([]types.Address, 0, len(snap.accounts))
	acctVals := make([][]byte, 0, len(snap.accounts))
	acctCosts := make([]int, 0, len(snap.accounts))
	for addr, v := range snap.accounts {
		acctKeys = append(acctKeys, addr)
		acctVals = append(acctVals, v)
		acctCosts = append(acctCosts, addrKeyLen+len(v)+cacheOverheadPerEntry)
	}
	b.readAccounts.PutBatch(acctKeys, acctVals, acctCosts)

	stoCount := 0
	for _, slots := range snap.storage {
		stoCount += len(slots)
	}
	stoKeys := make([][storageCompositeKeyLen]byte, 0, stoCount)
	stoVals := make([][]byte, 0, stoCount)
	stoCosts := make([]int, 0, stoCount)
	for addr, slots := range snap.storage {
		for slot, entry := range slots {
			var ck [storageCompositeKeyLen]byte
			copy(ck[:20], addr[:])
			copy(ck[20:], slot[:])
			stoKeys = append(stoKeys, ck)
			// Tombstone (deletedSentinel) → cache as nil so the LRU
			// returns the negative answer on subsequent reads instead
			// of falling through to MDBX where the row may still exist
			// briefly post-commit.
			if len(entry.value) == 0 {
				stoVals = append(stoVals, nil)
				stoCosts = append(stoCosts, storageCompositeKeyLen+cacheOverheadPerEntry)
			} else {
				stoVals = append(stoVals, entry.value)
				stoCosts = append(stoCosts, storageCompositeKeyLen+len(entry.value)+cacheOverheadPerEntry)
			}
		}
	}
	b.readStorage.PutBatch(stoKeys, stoVals, stoCosts)

	codeKeys := make([]types.Hash, 0, len(snap.code))
	codeVals := make([][]byte, 0, len(snap.code))
	codeCosts := make([]int, 0, len(snap.code))
	for h, code := range snap.code {
		codeKeys = append(codeKeys, h)
		codeVals = append(codeVals, code)
		codeCosts = append(codeCosts, hashKeyLen+len(code)+cacheOverheadPerEntry)
	}
	b.readCode.PutBatch(codeKeys, codeVals, codeCosts)
}

// Stats reports current write-buffer cardinalities (excluding any
// in-flight snapshot still being committed).
func (snap *BufferSnapshot) Stats() (accounts, storage int) {
	for _, slots := range snap.storage {
		storage += len(slots)
	}
	return len(snap.accounts), storage
}

// ResetReadCache drops all LRU contents. Used by paths that open a
// fresh MDBX read tx and need to invalidate snapshot-bound cached
// values (e.g. tests, recovery flows).
func (b *PlainStateBuffer) ResetReadCache() {
	b.readAccounts.Reset()
	b.readStorage.Reset()
	b.readCode.Reset()
}

func (b *PlainStateBuffer) ContractWipes() []types.Address { return b.contractWipes }

func (b *PlainStateBuffer) Stats() (accounts, storage int) {
	for _, slots := range b.storage {
		storage += len(slots)
	}
	return len(b.accounts), storage
}

// -----------------------------------------------------------------------
// BufferedPlainStateReader: write buffer → read cache → MDBX
// -----------------------------------------------------------------------

type BufferedPlainStateReader struct {
	buf *PlainStateBuffer
	db  kv.Getter
}

func NewBufferedPlainStateReader(buf *PlainStateBuffer, db kv.Getter) *BufferedPlainStateReader {
	return &BufferedPlainStateReader{buf: buf, db: db}
}

func (r *BufferedPlainStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	// 1. Active write buffer.
	if enc, ok := r.buf.accounts[address]; ok {
		r.buf.hits.Add(1)
		r.buf.acctHits.Add(1)
		if len(enc) == 0 {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(enc); err != nil {
			return nil, err
		}
		return &a, nil
	}
	// 1b. In-flight snapshot (background flush in progress; data may
	// not yet be visible to our MDBX tx snapshot).
	if snap := r.buf.inFlight.Load(); snap != nil {
		if enc, ok := snap.accounts[address]; ok {
			r.buf.hits.Add(1)
			r.buf.acctHits.Add(1)
			if len(enc) == 0 {
				return nil, nil
			}
			var a account.StateAccount
			if err := a.DecodeForStorage(enc); err != nil {
				return nil, err
			}
			return &a, nil
		}
	}
	// 2. LRU read cache.
	if v, present := r.buf.readAccounts.Get(address); present {
		r.buf.hits.Add(1)
		r.buf.acctHits.Add(1)
		if len(v) == 0 {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			return nil, err
		}
		return &a, nil
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	r.buf.acctMisses.Add(1)
	enc, err := r.db.GetOne(modules.Account, address[:])
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		// Cache the negative result so repeated lookups of an absent
		// address don't keep hitting MDBX.
		r.buf.readAccounts.Put(address, nil, addrKeyLen+cacheOverheadPerEntry)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readAccounts.Put(address, cached, addrKeyLen+len(cached)+cacheOverheadPerEntry)
	var a account.StateAccount
	if err = a.DecodeForStorage(cached); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *BufferedPlainStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	// 1. Active write buffer.
	if slots, ok := r.buf.storage[address]; ok {
		if entry, ok2 := slots[*key]; ok2 {
			r.buf.hits.Add(1)
			r.buf.stoHits.Add(1)
			if len(entry.value) == 0 {
				return nil, nil
			}
			return entry.value, nil
		}
	}
	// 1b. If storage was wiped (SELFDESTRUCT/CREATE within this batch),
	// slots not in the write buffer are gone — stale cache/MDBX values
	// from before the wipe must not be returned.
	if len(r.buf.wipedStorage) > 0 {
		if _, wiped := r.buf.wipedStorage[address]; wiped {
			return nil, nil
		}
	}
	// 1c. In-flight snapshot (background flush in progress).
	if snap := r.buf.inFlight.Load(); snap != nil {
		if slots, ok := snap.storage[address]; ok {
			if entry, ok2 := slots[*key]; ok2 {
				r.buf.hits.Add(1)
				r.buf.stoHits.Add(1)
				if len(entry.value) == 0 {
					return nil, nil
				}
				return entry.value, nil
			}
		}
		// Wiped contracts in the in-flight snapshot: any slot not
		// listed in snap.storage is gone (the wipe already executed).
		if _, wiped := snap.wipedStorage[address]; wiped {
			return nil, nil
		}
	}
	// 2. LRU read cache (composite key).
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	if v, present := r.buf.readStorage.Get(ck); present {
		r.buf.hits.Add(1)
		r.buf.stoHits.Add(1)
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	r.buf.stoMisses.Add(1)
	compositeKey := modules.PlainGenerateCompositeStorageKey(address[:], key[:])
	enc, err := r.db.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		r.buf.readStorage.Put(ck, nil, storageCompositeKeyLen+cacheOverheadPerEntry)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readStorage.Put(ck, cached, storageCompositeKeyLen+len(cached)+cacheOverheadPerEntry)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	if code, ok := r.buf.code[codeHash]; ok {
		r.buf.hits.Add(1)
		r.buf.codeHits.Add(1)
		return code, nil
	}
	if snap := r.buf.inFlight.Load(); snap != nil {
		if code, ok := snap.code[codeHash]; ok {
			r.buf.hits.Add(1)
			r.buf.codeHits.Add(1)
			return code, nil
		}
	}
	if v, present := r.buf.readCode.Get(codeHash); present {
		r.buf.hits.Add(1)
		r.buf.codeHits.Add(1)
		return v, nil
	}
	r.buf.misses.Add(1)
	r.buf.codeMisses.Add(1)
	code, err := r.db.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		return nil, nil
	}
	cached := make([]byte, len(code))
	copy(cached, code)
	r.buf.readCode.Put(codeHash, cached, hashKeyLen+len(cached)+cacheOverheadPerEntry)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, incarnation, codeHash)
	return len(code), err
}

func (r *BufferedPlainStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	if b, ok := r.buf.incarnationMap[address]; ok {
		return binary.BigEndian.Uint16(b), nil
	}
	b, err := r.db.GetOne(modules.IncarnationMap, address[:])
	if err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	return binary.BigEndian.Uint16(b), nil
}

// -----------------------------------------------------------------------
// BufferedPlainStateWriter writes to the buffer (not MDBX).
// -----------------------------------------------------------------------

type BufferedPlainStateWriter struct {
	buf *PlainStateBuffer
	csw *ChangeSetWriter
}

// NewBufferedPlainStateWriter constructs a writer for per-block use.
// The tx parameter is kv.Tx (read-only) because per-block execution
// only reads from MDBX (e.g. cursor scans in collectPreWipeSlots).
// All writes go to the in-memory buffer; the buffer is later flushed
// to MDBX by the executor's commit-interval flush path (sync or async).
func NewBufferedPlainStateWriter(buf *PlainStateBuffer, changeSetsDB kv.Tx, blockNumber uint64) *BufferedPlainStateWriter {
	return &BufferedPlainStateWriter{
		buf: buf,
		csw: NewChangeSetWriterPlain(changeSetsDB, blockNumber),
	}
}

func NewBufferedPlainStateWriterNoHistory(buf *PlainStateBuffer) *BufferedPlainStateWriter {
	return &BufferedPlainStateWriter{buf: buf}
}

func (w *BufferedPlainStateWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountData(address, original, acct); err != nil {
			return err
		}
	}
	if original != nil && original.Equals(acct) {
		return nil
	}
	w.buf.accounts[address] = acct.MarshalV2()
	return nil
}

func (w *BufferedPlainStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountCode(address, incarnation, codeHash, code); err != nil {
			return err
		}
	}
	w.buf.code[codeHash] = code
	w.buf.contractCode[string(modules.PlainGenerateStoragePrefix(address[:]))] = codeHash[:]
	return nil
}

func (w *BufferedPlainStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.DeleteAccount(address, original); err != nil {
			return err
		}
	}
	w.buf.accounts[address] = deletedSentinel
	// IncarnationMap removed — incarnation no longer used
	return nil
}

func (w *BufferedPlainStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if w.csw != nil {
		if err := w.csw.WriteAccountStorage(address, incarnation, key, original, value); err != nil {
			return err
		}
	}
	if *original == *value {
		return nil
	}
	slots := w.buf.storage[address]
	if slots == nil {
		slots = make(map[types.Hash]storageEntry, 8)
		w.buf.storage[address] = slots
	}
	v := value.Bytes()
	if len(v) == 0 {
		slots[*key] = storageEntry{value: deletedSentinel}
	} else {
		slots[*key] = storageEntry{value: v}
	}
	return nil
}

func (w *BufferedPlainStateWriter) CreateContract(address types.Address) error {
	// Before the wipe, collect pre-destruction slot values from the layered
	// state (buffer over MDBX) and record them in the storage changeset, so
	// backward unwind can restore them. Mirrors reth's write_state_reverts
	// wiped-storage enumeration path. First-wins in csw preserves
	// block-origin values already written by earlier SSTORE calls.
	var wipedSlots map[types.Hash][]byte
	if w.csw != nil {
		var err error
		wipedSlots, err = w.collectPreWipeSlots(address)
		if err != nil {
			return fmt.Errorf("collect pre-wipe slots for %x: %w", address, err)
		}
		w.csw.recordStorageWipe(address, wipedSlots)
		if err := w.csw.CreateContract(address); err != nil {
			return err
		}
	}
	// Clear buffered storage for this address.
	delete(w.buf.storage, address)
	// Invalidate every cached slot for this address. The flat composite-key
	// LRU has no per-address sub-map, so we evict each known slot individually.
	// Sources of "known slots": (a) the wipedSlots collected above (covers
	// MDBX + write buffer), (b) any slot still in the write buffer (rare:
	// touched after collectPreWipeSlots but before this point).
	var compositeKey [storageCompositeKeyLen]byte
	copy(compositeKey[:20], address[:])
	for slot := range wipedSlots {
		copy(compositeKey[20:], slot[:])
		w.buf.readStorage.Delete(compositeKey)
	}
	// Mark as wiped so ReadAccountStorage returns nil for slots not in buffer.
	w.buf.wipedStorage[address] = struct{}{}
	// Record address for MDBX storage wipe during FlushToMDBX.
	w.buf.contractWipes = append(w.buf.contractWipes, address)
	return nil
}

// collectPreWipeSlots returns the current visible (buffer ∪ in-flight
// snapshot ∪ MDBX) storage slots for an address, excluding slots whose
// current visible value is empty/deleted. Layer precedence:
// active buffer > in-flight snapshot > MDBX.
//
// The in-flight snapshot layer is critical with async PlainStateBuffer
// flushing: w.csw.db is a kv.Tx (RoTx) whose snapshot is taken AFTER
// the previous bg flush committed but BEFORE the current handoff
// commits. So MDBX(roTx) misses the entire previous commit interval's
// writes. Without merging the in-flight snapshot here, a SELFDESTRUCT
// of a contract whose slots were SSTORE'd in the previous interval
// would skip those slots in the wipe enumeration → storcs would be
// missing tombstones → forward replay produces a wrong state root.
func (w *BufferedPlainStateWriter) collectPreWipeSlots(address types.Address) (map[types.Hash][]byte, error) {
	// snap is captured once for the entire function so the MDBX-shadowing
	// loop sees the same snapshot we enumerated, even if the bg flush
	// completes mid-call and clears w.buf.inFlight.
	snap := w.buf.inFlight.Load()
	bufSlots, hasBuf := w.buf.storage[address]

	hint := len(bufSlots)
	if snap != nil {
		hint += len(snap.storage[address])
	}
	out := make(map[types.Hash][]byte, hint)

	// 1. Slots live in the write buffer — these shadow everything.
	if hasBuf {
		for slot, entry := range bufSlots {
			if len(entry.value) == 0 {
				// Deleted/empty in buffer: skip. csw already has the
				// pre-delete value from the earlier WriteAccountStorage call
				// that produced this buffer entry.
				continue
			}
			out[slot] = entry.value
		}
	}

	// 2. In-flight snapshot — bg flush in progress. These slots are NOT
	// yet visible to MDBX(w.csw.db) but logically exist. Active buffer
	// shadows; the snapshot is otherwise additive.
	var snapSlots map[types.Hash]storageEntry
	if snap != nil {
		if s, ok := snap.storage[address]; ok {
			snapSlots = s
			for slot, entry := range s {
				if hasBuf {
					if _, shadowed := bufSlots[slot]; shadowed {
						continue
					}
				}
				if len(entry.value) == 0 {
					// Deleted in the in-flight interval: skip — the
					// per-slot tombstone for that interval already
					// went to storcs at its boundary block.
					continue
				}
				out[slot] = entry.value
			}
		}
		// If the in-flight snapshot wiped the address, MDBX may still
		// hold orphan slots (the bg goroutine's wipe phase will run
		// inside its tx, but our RoTx still sees them). They are
		// logically gone — skip the MDBX scan for this address.
		if _, wiped := snap.wipedStorage[address]; wiped {
			return out, nil
		}
	}

	// 3. Slots in MDBX that are NOT shadowed by buffer or in-flight.
	if w.csw.db != nil {
		cursor, err := w.csw.db.Cursor(modules.Storage)
		if err != nil {
			return nil, err
		}
		prefix := address[:]
		for k, v, err := cursor.Seek(prefix); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				return nil, err
			}
			if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
				break
			}
			if len(k) != 52 || len(v) == 0 {
				continue
			}
			var slot types.Hash
			copy(slot[:], k[20:52])
			// Buffer shadows MDBX.
			if hasBuf {
				if _, shadowed := bufSlots[slot]; shadowed {
					continue
				}
			}
			// In-flight snapshot shadows MDBX.
			if snapSlots != nil {
				if _, shadowed := snapSlots[slot]; shadowed {
					continue
				}
			}
			out[slot] = v
		}
		cursor.Close()
	}

	return out, nil
}

func (w *BufferedPlainStateWriter) WriteChangeSets() error {
	if w.csw != nil {
		return w.csw.WriteChangeSets()
	}
	return nil
}

func (w *BufferedPlainStateWriter) WriteHistory() error {
	if w.csw != nil {
		return w.csw.WriteHistory()
	}
	return nil
}

func (w *BufferedPlainStateWriter) ChangeSetWriter() *ChangeSetWriter {
	return w.csw
}

var (
	_ StateReader          = (*BufferedPlainStateReader)(nil)
	_ WriterWithChangeSets = (*BufferedPlainStateWriter)(nil)
)
