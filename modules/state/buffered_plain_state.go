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
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

var deletedSentinel = []byte{}

// Contract-delete diagnostic logger. Writes one line per contract
// (non-empty CodeHash) being SELFDESTRUCT'd to ./deletes.log in the
// process's working directory. Volume is bounded — Ethereum mainnet
// sees a few hundred contract destructions per day. The file is
// append-mode so multiple ethexec runs accumulate, and stdout stays
// clean (avoiding the screen-flood that an unconditional fmt.Printf
// would cause). Grep deletes.log after a gas-mismatch to find the
// block + tx that destroyed the offending contract.
var (
	deletesLogOnce sync.Once
	deletesLogBuf  *bufio.Writer
	deletesLogFile *os.File
	deletesLogMu   sync.Mutex
)

func ensureDeletesLog() {
	deletesLogOnce.Do(func() {
		f, err := os.OpenFile("deletes.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		deletesLogFile = f
		// 64 KiB buffer — flushed manually via FlushDeletesLog at flush
		// boundaries. bufio.Writer is not concurrency-safe, hence the
		// mutex around every Write.
		deletesLogBuf = bufio.NewWriterSize(f, 64*1024)
	})
}

func recordContractDelete(blk uint64, addr types.Address, nonce uint64, codeHash types.Hash) {
	ensureDeletesLog()
	if deletesLogBuf == nil {
		return
	}
	deletesLogMu.Lock()
	defer deletesLogMu.Unlock()
	fmt.Fprintf(deletesLogBuf, "DELETE_CONTRACT blk=%d addr=%x nonce=%d codeHash=%x\n",
		blk, addr[:], nonce, codeHash[:])
}

// FlushDeletesLog forces the buffered diagnostic file to disk. Safe to
// call from any goroutine; no-op if logging was never initialized.
// Hooked into the executor's commit-interval flush path so a crash
// loses at most one interval's worth of entries.
func FlushDeletesLog() {
	deletesLogMu.Lock()
	defer deletesLogMu.Unlock()
	if deletesLogBuf != nil {
		_ = deletesLogBuf.Flush()
	}
	if deletesLogFile != nil {
		_ = deletesLogFile.Sync()
	}
}

// trackedAddr, if non-nil, causes the BufferedPlainState* write/flush path
// to emit targeted log lines whenever the tracked address is touched.
// Intended for debugging SELFDESTRUCT + metamorphic CREATE2 bugs without
// drowning in per-block noise.
var trackedAddr atomic.Pointer[types.Address]

// SetTrackedAddr installs the given address (20B) as the targeted trace
// subject. Passing nil disables tracing. Safe to call from any goroutine.
func SetTrackedAddr(addr *types.Address) {
	trackedAddr.Store(addr)
}

func isTracked(a types.Address) bool {
	p := trackedAddr.Load()
	if p == nil {
		return false
	}
	return *p == a
}

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
//   - active wallets) → 1 GB gives ~7M-entry headroom for 99%+
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
		AccountBytes: 2 << 30,  // 2 GB  S3-FIFO
		StorageBytes: 16 << 30, // 16 GB S3-FIFO — 128GB machine, covers ~270M slots
		CodeBytes:    1 << 30,  // 1 GB  byteLRU
	}
}

// Per-entry overhead estimates for byte accounting. Real Go map +
// container/list overhead is highly implementation-dependent; these are
// generous round numbers chosen to keep us well below the byte budget
// even as runtime data structures bloat.
const (
	cacheOverheadPerEntry  = 96 // map header + list.Element + pointer slop
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
	contractWipes []types.Address
	wipedStorage  map[types.Address]struct{}
}

// PlainStateBuffer holds write buffer + bounded LRU read caches.
type PlainStateBuffer struct {
	// Write buffer (single-writer, executor only).
	accounts      map[types.Address][]byte
	storage       map[types.Address]map[types.Hash]storageEntry
	code          map[types.Hash][]byte
	contractWipes []types.Address            // addresses needing MDBX storage wipe on flush
	wipedStorage  map[types.Address]struct{} // addresses whose storage was wiped — reads bypass cache/MDBX

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

	// flushEpoch increments once per fully-committed flush. Combined with
	// wipedAtEpoch this lets the async prefetcher reject Cache*IfAbsent
	// writes whose pinned RoTx pre-dates a SELFDESTRUCT-bearing flush —
	// the case where the LRU prefix-sweep already cleared the slot and
	// the prefetcher's stale value would slip into the gap. See
	// CacheStorageIfAbsentEpoch for the full reasoning.
	flushEpoch   atomic.Uint64
	wipedAtEpoch sync.Map // map[types.Address]uint64
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
		accounts:     make(map[types.Address][]byte, 4096),
		storage:      make(map[types.Address]map[types.Hash]storageEntry, 4096),
		code:         make(map[types.Hash][]byte, 64),
		wipedStorage: make(map[types.Address]struct{}),
		readAccounts: newS3FIFO[types.Address](b.AccountBytes),
		readStorage:  newS3FIFO[[storageCompositeKeyLen]byte](b.StorageBytes),
		readCode:     newByteLRU[types.Hash](b.CodeBytes),
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

// CacheCode populates the bytecode read cache. Used by speculative
// prefetch when a contract's code is loaded from MDBX during prewarm.
func (b *PlainStateBuffer) CacheCode(codeHash types.Hash, code []byte) {
	cost := hashKeyLen + len(code) + cacheOverheadPerEntry
	b.readCode.Put(codeHash, code, cost)
}

// CacheAccountIfAbsent populates the account read cache only if no entry
// exists. Tombstone-safe: never overwrites a tombstone or fresher value
// that RefreshLRUForSnapshot has already written. Designed for the async
// prefetcher whose MDBX snapshot may lag behind executor writes.
func (b *PlainStateBuffer) CacheAccountIfAbsent(address types.Address, enc []byte) bool {
	cost := addrKeyLen + len(enc) + cacheOverheadPerEntry
	return b.readAccounts.PutIfAbsent(address, enc, cost)
}

// CacheStorageIfAbsent populates the storage read cache only if no entry
// exists. Tombstone-safe variant of CacheStorage.
func (b *PlainStateBuffer) CacheStorageIfAbsent(address types.Address, key types.Hash, value []byte) bool {
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	cost := storageCompositeKeyLen + len(value) + cacheOverheadPerEntry
	return b.readStorage.PutIfAbsent(ck, value, cost)
}

// CacheCodeIfAbsent populates the bytecode read cache only if no entry
// exists.
func (b *PlainStateBuffer) CacheCodeIfAbsent(codeHash types.Hash, code []byte) bool {
	cost := hashKeyLen + len(code) + cacheOverheadPerEntry
	return b.readCode.PutIfAbsent(codeHash, code, cost)
}

// CurrentFlushEpoch returns the count of completed flushes. The async
// prefetcher captures this BEFORE opening its RoTx so a later check
// (CacheAccount/StorageIfAbsentEpoch) can detect whether an address was
// wiped between BeginRo and the prefetcher's PutIfAbsent — a tombstone
// race that PutIfAbsent alone cannot block (the LRU sweep in
// RefreshLRUForSnapshot empties LRU for the wiped addr, so the
// prefetcher's stale value would slip into the gap).
func (b *PlainStateBuffer) CurrentFlushEpoch() uint64 {
	return b.flushEpoch.Load()
}

// StampWipes records that the given addresses were wiped at the just-
// committed flush epoch. Called by the flusher AFTER tx.Commit succeeds
// — same lock-section as RefreshLRUForSnapshot — so any prefetcher
// PutIfAbsentEpoch arriving later sees these stamps and skips. The map
// grows monotonically; pruning is handled by a separate compaction
// once flushEpoch advances enough that no live RoTx could be older.
func (b *PlainStateBuffer) StampWipes(wipedAddrs []types.Address) uint64 {
	epoch := b.flushEpoch.Add(1)
	for _, addr := range wipedAddrs {
		b.wipedAtEpoch.Store(addr, epoch)
	}
	return epoch
}

// PruneWipesBefore drops wipe records older than `cutoff`. Caller must
// be sure no live RoTx was opened before cutoff. Called periodically
// from the flusher to bound memory growth.
func (b *PlainStateBuffer) PruneWipesBefore(cutoff uint64) {
	if cutoff == 0 {
		return
	}
	var stale []types.Address
	b.wipedAtEpoch.Range(func(k, v interface{}) bool {
		if v.(uint64) <= cutoff {
			stale = append(stale, k.(types.Address))
		}
		return true
	})
	for _, a := range stale {
		b.wipedAtEpoch.Delete(a)
	}
}

// CacheAccountIfAbsentEpoch is the epoch-aware variant of
// CacheAccountIfAbsent for the async prefetcher. It additionally skips
// when the address was wiped at an epoch later than the prefetcher's
// captured epoch — i.e. a flush completed between the prefetcher's
// BeginRo and its PutIfAbsent, and our stale RoTx-derived value would
// otherwise land in the gap RefreshLRUForSnapshot just opened.
func (b *PlainStateBuffer) CacheAccountIfAbsentEpoch(address types.Address, enc []byte, prefetcherEpoch uint64) bool {
	// GLOBAL flush-epoch guard. The previous wipedAtEpoch-only check was
	// too narrow: it only stamped SELFDESTRUCT'd addresses, so a normal
	// balance/code/storage update in interval N could leave a stale
	// pre-flush value writeable from a prefetcher whose RoTx still
	// pinned interval N-1's snapshot. Observed at block 2520771 (sender
	// 0xB8cc0F060AAd92d4eb8B36b3B95cE9E90eb383d7 missing 150,000 ETH —
	// see commit e4496062). Rejecting any write whose captured epoch
	// trails the current flush epoch closes that race at the cost of
	// wasting the rare prefetch cycle that straddles a commit-interval
	// boundary (~0.01% of cycles at CommitInterval=10000).
	if prefetcherEpoch < b.flushEpoch.Load() {
		return false
	}
	// Per-address SELFDESTRUCT epoch check (kept for the same-epoch
	// case where prefetcherEpoch matches current but a contractWipes
	// stamp landed at the same epoch).
	if v, ok := b.wipedAtEpoch.Load(address); ok && v.(uint64) > prefetcherEpoch {
		return false
	}
	cost := addrKeyLen + len(enc) + cacheOverheadPerEntry
	return b.readAccounts.PutIfAbsent(address, enc, cost)
}

// CacheStorageIfAbsentEpoch — see CacheAccountIfAbsentEpoch for the
// flush-epoch guard reasoning.
func (b *PlainStateBuffer) CacheStorageIfAbsentEpoch(address types.Address, key types.Hash, value []byte, prefetcherEpoch uint64) bool {
	if prefetcherEpoch < b.flushEpoch.Load() {
		return false
	}
	if v, ok := b.wipedAtEpoch.Load(address); ok && v.(uint64) > prefetcherEpoch {
		return false
	}
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	cost := storageCompositeKeyLen + len(value) + cacheOverheadPerEntry
	return b.readStorage.PutIfAbsent(ck, value, cost)
}

// LookupReadAccount returns the v2-encoded account bytes from the read
// LRU. (nil, true) means "negative cache hit" — known-absent. (nil,
// false) means cache miss. Used by the speculative prefetch reader so
// it can skip the active write-buffer tier (which is mutated by the
// executor goroutine and unsafe to read concurrently).
func (b *PlainStateBuffer) LookupReadAccount(address types.Address) ([]byte, bool) {
	return b.readAccounts.Get(address)
}

// LookupReadStorage returns the storage slot value from the read LRU.
// Same semantics as LookupReadAccount.
func (b *PlainStateBuffer) LookupReadStorage(address types.Address, key types.Hash) ([]byte, bool) {
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	return b.readStorage.Get(ck)
}

// LookupReadCode returns the bytecode from the read LRU.
func (b *PlainStateBuffer) LookupReadCode(codeHash types.Hash) ([]byte, bool) {
	return b.readCode.Get(codeHash)
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
		contractWipes: b.contractWipes,
		wipedStorage:  b.wipedStorage,
	}
	// Pre-size the next interval's maps from this interval's actuals so
	// we don't pay 8+ rehash cycles per commit-interval at the 11M-block
	// scale (bufAccs ~900K, bufStos addrs ~50K).
	nextAcctCap := nextMapCap(len(b.accounts), 4096)
	nextStoAddrCap := nextMapCap(len(b.storage), 4096)
	nextCodeCap := nextMapCap(len(b.code), 64)
	b.accounts = make(map[types.Address][]byte, nextAcctCap)
	b.storage = make(map[types.Address]map[types.Hash]storageEntry, nextStoAddrCap)
	b.code = make(map[types.Hash][]byte, nextCodeCap)
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

// InFlightSnapshot returns the current in-flight snapshot (may be nil).
// The snapshot is immutable and safe to read from any goroutine.
func (b *PlainStateBuffer) InFlightSnapshot() *BufferSnapshot {
	return b.inFlight.Load()
}

// LookupAccount returns the encoded account in the snapshot.
// Returns (enc, true) if found, (nil, false) if not in this snapshot.
func (s *BufferSnapshot) LookupAccount(addr types.Address) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	enc, ok := s.accounts[addr]
	return enc, ok
}

// LookupStorage returns a storage slot from the snapshot.
// Returns (value, true) if the slot is explicitly in the snapshot.
// Returns (nil, true) if the address was wiped (SELFDESTRUCT) in this snapshot.
// Returns (nil, false) if neither the slot nor a wipe is recorded.
func (s *BufferSnapshot) LookupStorage(addr types.Address, slot types.Hash) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	if slots, ok := s.storage[addr]; ok {
		if entry, ok2 := slots[slot]; ok2 {
			return entry.value, true
		}
	}
	if _, wiped := s.wipedStorage[addr]; wiped {
		return nil, true // wiped — slot is gone
	}
	return nil, false
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
		if isTracked(addr) {
			fmt.Printf("[TRACK] ApplyTo.sweep addr=%x swept_rows=%d\n", addr[:], len(keysToDelete))
			// Post-sweep probe: any rows still present?
			c2, err := tx.Cursor(modules.Storage)
			if err == nil {
				remaining := 0
				for k, _, err := c2.Seek(prefix); k != nil; k, _, err = c2.Next() {
					if err != nil {
						break
					}
					if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
						break
					}
					remaining++
				}
				c2.Close()
				fmt.Printf("[TRACK] ApplyTo.sweep/after addr=%x remaining_rows=%d\n", addr[:], remaining)
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

	// 4. Code (content-addressed bytecode). PlainContractCode is no longer
	// maintained as of Phase D — Account row carries CodeHash inline.
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

	// SELFDESTRUCT-aware LRU sweep. CreateContract did `delete(buf.storage,
	// addr)`, so snap.storage doesn't contain the wiped addresses, so the
	// snap.storage iteration above never touched those LRU keys. But the
	// LRU may hold non-zero entries from PRIOR-INTERVAL EVM reads of those
	// addresses' slots — those entries are now stale (MDBX rows just got
	// prefix-swept by ApplyTo). Without invalidation, the next interval's
	// ReadAccountStorage hits stale LRU and propagates wrong original
	// values into IBS, producing wipe-stale storcs entries (oldVal = stale
	// non-zero, newVal = 0) and divergent state-root computation.
	//
	// Empirical confirmation: block 10,941,141 SELFDESTRUCT'd MEV proxy
	// 00000000002bde…b8e1; LRU kept slot 8 = 0x2432 from prior reads;
	// blocks 10,941,146 and 10,941,306 read that stale 0x2432 even though
	// MDBX was clean — both blocks emitted (slot 8, oldVal=0x2432, newVal=0)
	// while reth-on-mainnet has no entry at 10,941,146 and preVal=0 at
	// 10,941,306. Same fingerprint at 17,150,008 (1ac01ebe…) and 3,180,272
	// (8563cc86…). Single root cause across all three.
	//
	// Sweep is O(LRU) per snap; only fires when contractWipes is non-empty,
	// which is rare per interval. Account/code LRUs aren't affected: the
	// SELFDESTRUCT path WriteAccountStorage's DeleteAccount call writes
	// snap.accounts[addr] = empty, so RefreshLRU above tombstones account
	// LRU correctly.
	// ORDER MATTERS — StampWipes MUST run BEFORE the LRU prefix sweep.
	//
	//   1. StampWipes (epoch++) ⇒ subsequent prefetcher PutIfAbsentEpoch
	//      calls observe wipedAtEpoch[addr] > prefetcherEpoch and SKIP.
	//   2. Range + DeleteBatch ⇒ flush any prefetcher entries that snuck
	//      in BEFORE step 1 took effect (their epoch check used the old
	//      wipedAtEpoch state).
	//
	// Reverse order leaves a window: prefetcher passes its epoch check
	// against the OLD epoch, RefreshLRU sweeps an empty LRU (prefetcher's
	// insert hasn't landed yet), THEN StampWipes runs but the entry is
	// already in LRU — no one will re-sweep it. That's the leak the
	// SELFDESTRUCT-LRU regression at block 17,150,008 originally fired
	// through; this ordering closes it.
	b.StampWipes(snap.contractWipes)

	if len(snap.contractWipes) > 0 {
		wipedAddrs := make(map[types.Address]struct{}, len(snap.contractWipes))
		for _, addr := range snap.contractWipes {
			wipedAddrs[addr] = struct{}{}
		}
		var stale [][storageCompositeKeyLen]byte
		b.readStorage.Range(func(k [storageCompositeKeyLen]byte) bool {
			var addr types.Address
			copy(addr[:], k[:20])
			if _, ok := wipedAddrs[addr]; ok {
				stale = append(stale, k)
			}
			return true
		})
		if len(stale) > 0 {
			b.readStorage.DeleteBatch(stale)
		}
	}
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

// auditNilAccount, when set via env N42_AUDIT_NIL_ACCT=1, makes
// bufReader.ReadAccountData cross-check every nil-or-empty-codeHash
// account return against MDBX. If MDBX has the account with a
// non-empty CodeHash but the cache returned nil/empty, the divergence
// is recorded to ./audit_nil.log with which tier returned the wrong
// answer. Default off (zero overhead). Enable to diagnose the
// non-deterministic "contract treated as EOA" gas-mismatch bug.
var auditNilAccount = os.Getenv("N42_AUDIT_NIL_ACCT") == "1"

var (
	auditLogOnce sync.Once
	auditLogBuf  *bufio.Writer
	auditLogFile *os.File
	auditLogMu   sync.Mutex
)

func ensureAuditLog() {
	auditLogOnce.Do(func() {
		f, err := os.OpenFile("audit_nil.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return
		}
		auditLogFile = f
		auditLogBuf = bufio.NewWriterSize(f, 64*1024)
	})
}

func auditWrite(format string, args ...interface{}) {
	ensureAuditLog()
	if auditLogBuf == nil {
		return
	}
	auditLogMu.Lock()
	defer auditLogMu.Unlock()
	fmt.Fprintf(auditLogBuf, format, args...)
}

// FlushAuditLog forces the nil-audit diagnostic file to disk.
func FlushAuditLog() {
	auditLogMu.Lock()
	defer auditLogMu.Unlock()
	if auditLogBuf != nil {
		_ = auditLogBuf.Flush()
	}
	if auditLogFile != nil {
		_ = auditLogFile.Sync()
	}
}

type BufferedPlainStateReader struct {
	buf *PlainStateBuffer
	db  kv.Getter
}

func NewBufferedPlainStateReader(buf *PlainStateBuffer, db kv.Getter) *BufferedPlainStateReader {
	return &BufferedPlainStateReader{buf: buf, db: db}
}

// auditStorageLRU cross-checks a storage value returned from the LRU
// against MDBX. The two MUST agree because LRU is fed exclusively from
// RefreshLRUForSnapshot (post-flush canonical values) and bufReader's
// own MDBX miss path (executor's stable RoTx). A divergence is a
// smoking gun: either the LRU was poisoned, or some path bypassed
// MDBX. Logs to audit_nil.log when divergence detected. No-op when
// auditNilAccount is false.
func (r *BufferedPlainStateReader) auditStorageLRU(addr types.Address, slot types.Hash, ck [storageCompositeKeyLen]byte, lruValue []byte) {
	mdbxValue, err := r.db.GetOne(modules.Storage, ck[:])
	if err != nil {
		return
	}
	// Normalize: deletedSentinel ([]byte{}) and nil and (len=0) all
	// represent "slot is zero" for our purposes.
	lruZero := len(lruValue) == 0
	mdbxZero := len(mdbxValue) == 0
	if lruZero && mdbxZero {
		return
	}
	if !lruZero && !mdbxZero && bytes.Equal(lruValue, mdbxValue) {
		return
	}
	auditWrite("AUDIT_STO addr=%x slot=%x lru_zero=%v lru_val=%x mdbx_zero=%v mdbx_val=%x\n",
		addr[:], slot[:], lruZero, lruValue, mdbxZero, mdbxValue)
}

// auditNilReturn cross-checks a "nil or empty-CodeHash" cache return
// against MDBX. Logs to audit_nil.log if MDBX shows the account has
// a non-empty CodeHash (i.e., the cache lied). Caller passes the tier
// that produced the result (e.g. "buf.accounts/deletedSentinel",
// "inFlight.accounts", "LRU/negative", "MDBX") for diagnostic context.
// No-op when auditing isn't enabled.
func (r *BufferedPlainStateReader) auditNilReturn(addr types.Address, tier string, returned *account.StateAccount) {
	if !auditNilAccount {
		return
	}
	mdbxEnc, err := r.db.GetOne(modules.Account, addr[:])
	if err != nil || len(mdbxEnc) == 0 {
		return
	}
	var mdbxAcct account.StateAccount
	if err := mdbxAcct.DecodeForStorage(mdbxEnc); err != nil {
		return
	}
	if mdbxAcct.IsEmptyCodeHash() {
		// MDBX agrees this is EOA / no code. No divergence.
		return
	}
	// MDBX has a real contract; cache returned EOA-shaped result.
	var retNonce uint64
	var retCH types.Hash
	if returned != nil {
		retNonce = returned.Nonce
		retCH = returned.CodeHash
	}
	auditWrite("AUDIT_NIL addr=%x tier=%s ret_nil=%v ret_nonce=%d ret_ch=%x mdbx_nonce=%d mdbx_ch=%x\n",
		addr[:], tier, returned == nil, retNonce, retCH[:], mdbxAcct.Nonce, mdbxAcct.CodeHash[:])
}

func (r *BufferedPlainStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	tracked := isTracked(address)
	// 1. Active write buffer.
	if enc, ok := r.buf.accounts[address]; ok {
		r.buf.hits.Add(1)
		r.buf.acctHits.Add(1)
		if len(enc) == 0 {
			if tracked {
				fmt.Printf("[TRACK] ReadAccountData addr=%x src=buf.accounts return_nil (deletedSentinel)\n", address[:])
			}
			r.auditNilReturn(address, "buf.accounts/deletedSentinel", nil)
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(enc); err != nil {
			return nil, err
		}
		if tracked {
			fmt.Printf("[TRACK] ReadAccountData addr=%x src=buf.accounts nonce=%d codeHash=%x\n", address[:], a.Nonce, a.CodeHash[:])
		}
		if a.IsEmptyCodeHash() {
			r.auditNilReturn(address, "buf.accounts/empty-codeHash", &a)
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
				if tracked {
					fmt.Printf("[TRACK] ReadAccountData addr=%x src=inFlight.accounts return_nil (deletedSentinel)\n", address[:])
				}
				r.auditNilReturn(address, "inFlight.accounts/deletedSentinel", nil)
				return nil, nil
			}
			var a account.StateAccount
			if err := a.DecodeForStorage(enc); err != nil {
				return nil, err
			}
			if tracked {
				fmt.Printf("[TRACK] ReadAccountData addr=%x src=inFlight.accounts nonce=%d codeHash=%x\n", address[:], a.Nonce, a.CodeHash[:])
			}
			if a.IsEmptyCodeHash() {
				r.auditNilReturn(address, "inFlight.accounts/empty-codeHash", &a)
			}
			return &a, nil
		}
	}
	// 2. LRU read cache.
	if v, present := r.buf.readAccounts.Get(address); present {
		r.buf.hits.Add(1)
		r.buf.acctHits.Add(1)
		if len(v) == 0 {
			if tracked {
				fmt.Printf("[TRACK] ReadAccountData addr=%x src=LRU return_nil (negative cache)\n", address[:])
			}
			r.auditNilReturn(address, "LRU/negative", nil)
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			return nil, err
		}
		if tracked {
			fmt.Printf("[TRACK] ReadAccountData addr=%x src=LRU nonce=%d codeHash=%x\n", address[:], a.Nonce, a.CodeHash[:])
		}
		if a.IsEmptyCodeHash() {
			r.auditNilReturn(address, "LRU/empty-codeHash", &a)
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
		// Cache the negative result. SAFE here because bufReader is the
		// executor goroutine's reader, using the executor's per-interval
		// RoTx. Within an interval, RoTx is stable, so a nil from MDBX
		// is the canonical "absent at start of interval" answer. If a
		// later block in the same interval creates the address, it goes
		// through active buf (read tier 1) and bypasses LRU. End-of-
		// interval RefreshLRUForSnapshot's PutBatch then overwrites the
		// nil with the new encoded account. The prefetcher's nil-filter
		// (in prefetch.go and prefetch_speculative.go) handles the
		// stale-RoTx race separately — that's the racy path, not this one.
		r.buf.readAccounts.Put(address, nil, addrKeyLen+cacheOverheadPerEntry)
		if tracked {
			fmt.Printf("[TRACK] ReadAccountData addr=%x src=MDBX return_nil (cached negative)\n", address[:])
		}
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readAccounts.Put(address, cached, addrKeyLen+len(cached)+cacheOverheadPerEntry)
	var a account.StateAccount
	if err = a.DecodeForStorage(cached); err != nil {
		return nil, err
	}
	if tracked {
		fmt.Printf("[TRACK] ReadAccountData addr=%x src=MDBX nonce=%d codeHash=%x cached_to_LRU\n", address[:], a.Nonce, a.CodeHash[:])
	}
	return &a, nil
}

func (r *BufferedPlainStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	tracked := isTracked(address)
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
			if tracked {
				fmt.Printf("[TRACK] ReadAccountStorage addr=%x slot=%x src=buf.wipedStorage return_nil\n", address[:], key)
			}
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
			if tracked {
				fmt.Printf("[TRACK] ReadAccountStorage addr=%x slot=%x src=inFlight.wipedStorage return_nil\n", address[:], key)
			}
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
		// Audit: cross-check LRU value against MDBX. They MUST agree
		// because LRU is fed exclusively from RefreshLRUForSnapshot
		// (post-flush canonical snap values) and bufReader's own MDBX
		// miss path (executor's stable RoTx). Any divergence is a
		// smoking-gun bug — either LRU was poisoned by a stale-RoTx
		// prefetcher write, or there's a path that overwrites LRU
		// without going through MDBX. See ensureAuditLog comment.
		if auditNilAccount {
			r.auditStorageLRU(address, *key, ck, v)
		}
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
	if tracked && enc != nil {
		// Only log MDBX reads that hit non-nil — these are the "actual
		// leftover" values we care about for wipe-verification.
		fmt.Printf("[TRACK] ReadAccountStorage addr=%x slot=%x src=MDBX val=%x\n", address[:], key, enc)
	}
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		// Cache the negative result — same safety reasoning as
		// ReadAccountData: bufReader uses executor's per-interval stable
		// RoTx, so nil from MDBX is the canonical "absent at start of
		// interval" answer. End-of-interval RefreshLRU overwrites if the
		// slot becomes non-zero. Prefetcher paths handle stale-RoTx race
		// separately via their own nil-filter.
		r.buf.readStorage.Put(ck, nil, storageCompositeKeyLen+cacheOverheadPerEntry)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readStorage.Put(ck, cached, storageCompositeKeyLen+len(cached)+cacheOverheadPerEntry)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
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

func (r *BufferedPlainStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}

// -----------------------------------------------------------------------
// BufferedPlainStateWriter writes to the buffer (not MDBX).
// -----------------------------------------------------------------------

type BufferedPlainStateWriter struct {
	buf *PlainStateBuffer
	csw *ChangeSetWriter

	// Per-block touched-entry tracking. Populated by every Update*/
	// Delete*/WriteAccountStorage/CreateContract/UpdateAccountCode
	// call; consumed by RefreshLRUForBlock at end-of-block, which
	// PutBatches the current buffer values for those keys into the
	// shared read LRU. This is the reth-style cross-block cache
	// refresh — without it, recently written entries miss the LRU
	// for the rest of the commit interval (~10K blocks) and only
	// get promoted at the next interval boundary.
	//
	// All maps are nil until first write to keep allocation cost
	// zero on read-only / empty-block paths.
	touchedAccts map[types.Address]struct{}
	touchedStor  map[types.Address]map[types.Hash]struct{}
	touchedCode  map[types.Hash]struct{}
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

// RefreshLRUForBlock pushes the current buffer values for every key
// that this block's writer touched into the shared read LRU. Mirrors
// the per-block insert_state(BundleState) path in reth's
// CachedStateProvider — the only cross-block cache writer apart from
// RefreshLRUForSnapshot at commit-interval boundaries. Safe to call
// from the executor goroutine after processBlock returns; not safe
// from any other goroutine because it reads buf.accounts/storage/code
// (which are mutated by the executor on the same goroutine).
//
// Cost: O(touched entries per block); typical mainnet block has
// ~50-200 dirty entries, so this is microseconds per block. Compared
// to the previous LRU-touch from prefetcher writes (which were racy
// and got removed in 49b700ae), this is single-goroutine, sees the
// authoritative just-committed buf state, and matches reth's invariant:
// cache is only mutated by the post-block insert_state path.
func (w *BufferedPlainStateWriter) RefreshLRUForBlock() {
	if w.buf == nil {
		return
	}
	if n := len(w.touchedAccts); n > 0 {
		keys := make([]types.Address, 0, n)
		vals := make([][]byte, 0, n)
		costs := make([]int, 0, n)
		for addr := range w.touchedAccts {
			v := w.buf.accounts[addr]
			keys = append(keys, addr)
			// deletedSentinel ([]byte{}) gets cached as nil so reads
			// see the negative answer; identical to RefreshLRUForSnapshot's
			// account loop semantics.
			if len(v) == 0 {
				vals = append(vals, nil)
				costs = append(costs, addrKeyLen+cacheOverheadPerEntry)
			} else {
				vals = append(vals, v)
				costs = append(costs, addrKeyLen+len(v)+cacheOverheadPerEntry)
			}
		}
		w.buf.readAccounts.PutBatch(keys, vals, costs)
	}
	if len(w.touchedStor) > 0 {
		// Two-pass: first count to size the slice, then fill.
		stoCount := 0
		for _, slots := range w.touchedStor {
			stoCount += len(slots)
		}
		stoKeys := make([][storageCompositeKeyLen]byte, 0, stoCount)
		stoVals := make([][]byte, 0, stoCount)
		stoCosts := make([]int, 0, stoCount)
		for addr, slots := range w.touchedStor {
			bufSlots := w.buf.storage[addr]
			for slot := range slots {
				var ck [storageCompositeKeyLen]byte
				copy(ck[:20], addr[:])
				copy(ck[20:], slot[:])
				stoKeys = append(stoKeys, ck)
				var v []byte
				if bufSlots != nil {
					v = bufSlots[slot].value
				}
				if len(v) == 0 {
					stoVals = append(stoVals, nil)
					stoCosts = append(stoCosts, storageCompositeKeyLen+cacheOverheadPerEntry)
				} else {
					stoVals = append(stoVals, v)
					stoCosts = append(stoCosts, storageCompositeKeyLen+len(v)+cacheOverheadPerEntry)
				}
			}
		}
		w.buf.readStorage.PutBatch(stoKeys, stoVals, stoCosts)
	}
	if n := len(w.touchedCode); n > 0 {
		keys := make([]types.Hash, 0, n)
		vals := make([][]byte, 0, n)
		costs := make([]int, 0, n)
		for h := range w.touchedCode {
			code := w.buf.code[h]
			keys = append(keys, h)
			vals = append(vals, code)
			costs = append(costs, hashKeyLen+len(code)+cacheOverheadPerEntry)
		}
		w.buf.readCode.PutBatch(keys, vals, costs)
	}
	// The writer is one-shot per block; clearing here is defensive in
	// case a caller re-uses it (none currently do).
	w.touchedAccts = nil
	w.touchedStor = nil
	w.touchedCode = nil
}

// markTouchedAcct / markTouchedStor / markTouchedCode are tiny helpers
// used by every Write* method to populate the touched-entry maps.
// Lazy allocation: empty blocks pay zero.
func (w *BufferedPlainStateWriter) markTouchedAcct(addr types.Address) {
	if w.touchedAccts == nil {
		w.touchedAccts = make(map[types.Address]struct{}, 64)
	}
	w.touchedAccts[addr] = struct{}{}
}

func (w *BufferedPlainStateWriter) markTouchedStor(addr types.Address, slot types.Hash) {
	if w.touchedStor == nil {
		w.touchedStor = make(map[types.Address]map[types.Hash]struct{}, 32)
	}
	slots := w.touchedStor[addr]
	if slots == nil {
		slots = make(map[types.Hash]struct{}, 8)
		w.touchedStor[addr] = slots
	}
	slots[slot] = struct{}{}
}

func (w *BufferedPlainStateWriter) markTouchedCode(h types.Hash) {
	if w.touchedCode == nil {
		w.touchedCode = make(map[types.Hash]struct{}, 4)
	}
	w.touchedCode[h] = struct{}{}
}

func (w *BufferedPlainStateWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountData(address, original, acct); err != nil {
			return err
		}
	}
	if original != nil && original.Equals(acct) {
		if isTracked(address) {
			bn := uint64(0)
			if w.csw != nil {
				bn = w.csw.blockNumber
			}
			fmt.Printf("[TRACK] UpdateAccountData blk=%d addr=%x SHORT_CIRCUIT (orig==acct) nonce=%d codeHash=%x\n",
				bn, address[:], acct.Nonce, acct.CodeHash[:])
		}
		return nil
	}
	if isTracked(address) {
		bn := uint64(0)
		if w.csw != nil {
			bn = w.csw.blockNumber
		}
		var origNonce uint64
		var origCH types.Hash
		if original != nil {
			origNonce = original.Nonce
			origCH = original.CodeHash
		}
		fmt.Printf("[TRACK] UpdateAccountData blk=%d addr=%x orig_nonce=%d orig_ch=%x → new_nonce=%d new_ch=%x\n",
			bn, address[:], origNonce, origCH[:], acct.Nonce, acct.CodeHash[:])
	}
	// WRITE-SIDE AUDIT: catch the moment a real contract gets buf-ified
	// with empty/zero CodeHash. This is the SOURCE of the "contract
	// treated as EOA" gas-mismatch bug — IBS computed wrong state for
	// this addr (almost certainly because bufReader earlier returned
	// nil for it, leading IBS to createObject with zero CodeHash data).
	// Cross-check against MDBX (via csw.db) — if MDBX has the contract
	// with non-empty CodeHash, this UpdateAccountData call is poisoning
	// the buffer. Log it. Volume bounded — only fires for "writing zero
	// codeHash" cases AND only when MDBX disagrees.
	if auditNilAccount && acct.IsEmptyCodeHash() && w.csw != nil && w.csw.db != nil {
		mdbxEnc, err := w.csw.db.GetOne(modules.Account, address[:])
		if err == nil && len(mdbxEnc) > 0 {
			var mdbxAcct account.StateAccount
			if dErr := mdbxAcct.DecodeForStorage(mdbxEnc); dErr == nil && !mdbxAcct.IsEmptyCodeHash() {
				bn := uint64(0)
				if w.csw != nil {
					bn = w.csw.blockNumber
				}
				var origNonce uint64
				var origCH types.Hash
				origNil := true
				if original != nil {
					origNil = false
					origNonce = original.Nonce
					origCH = original.CodeHash
				}
				auditWrite("AUDIT_WRITE blk=%d addr=%x orig_nil=%v orig_nonce=%d orig_ch=%x acct_nonce=%d acct_balance=%s acct_ch=%x mdbx_nonce=%d mdbx_ch=%x\n",
					bn, address[:], origNil, origNonce, origCH[:], acct.Nonce, acct.Balance.String(), acct.CodeHash[:], mdbxAcct.Nonce, mdbxAcct.CodeHash[:])
			}
		}
	}
	w.buf.accounts[address] = acct.MarshalV2()
	w.markTouchedAcct(address)
	return nil
}

func (w *BufferedPlainStateWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountCode(address, codeHash, code); err != nil {
			return err
		}
	}
	// Bytecode is content-addressed in modules.Code (codeHash → bytecode).
	// Account row's CodeHash field is the only address→code link.
	w.buf.code[codeHash] = code
	w.markTouchedCode(codeHash)
	return nil
}

func (w *BufferedPlainStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.DeleteAccount(address, original); err != nil {
			return err
		}
	}
	bn := uint64(0)
	if w.csw != nil {
		bn = w.csw.blockNumber
	}
	if isTracked(address) {
		var origNonce uint64
		var origCH types.Hash
		if original != nil {
			origNonce = original.Nonce
			origCH = original.CodeHash
		}
		fmt.Printf("[TRACK] DeleteAccount blk=%d addr=%x orig_nonce=%d orig_ch=%x → deletedSentinel\n",
			bn, address[:], origNonce, origCH[:])
	}
	// Diagnostic: emit one line to ./deletes.log for every contract
	// (non-empty CodeHash) being destroyed. EOA empty-account removals
	// (Spurious Dragon) are skipped because they're high-volume and
	// not the suspect path. See ensureDeletesLog comment.
	if original != nil && !original.IsEmptyCodeHash() {
		recordContractDelete(bn, address, original.Nonce, original.CodeHash)
	}
	w.buf.accounts[address] = deletedSentinel
	w.markTouchedAcct(address)
	return nil
}

func (w *BufferedPlainStateWriter) WriteAccountStorage(address types.Address, key *types.Hash, original, value *uint256.Int) error {
	if isTracked(address) {
		bn := uint64(0)
		if w.csw != nil {
			bn = w.csw.blockNumber
		}
		fmt.Printf("[TRACK] WriteAccountStorage blk=%d addr=%x slot=%x orig=%s val=%s\n",
			bn, address[:], key, original.Hex(), value.Hex())
	}
	if w.csw != nil {
		if err := w.csw.WriteAccountStorage(address, key, original, value); err != nil {
			return err
		}
	}
	// IMPORTANT: do NOT short-circuit on original==value here. `original` is
	// blockOriginStorage[slot] (the value at block start). When an earlier
	// tx in this same block wrote the slot to some intermediate V_mid and a
	// later tx in the same block reverted it back to V_start, updateTrie
	// calls WriteAccountStorage(V_start, V_start). Skipping the buffer
	// write would leave the earlier tx's V_mid in w.buf, which then gets
	// flushed to MDBX as the final block state — silently corrupting the
	// slot to V_mid instead of the consensus-correct V_start. Observed
	// pattern: approve/reset, DEX reserves temp-modify, etc. Always write
	// the final `value` so the buffer reflects post-tx truth; redundant
	// Put of the same value to MDBX is cheap compared to this correctness
	// bug cascading across 100k+ blocks.
	slots := w.buf.storage[address]
	if slots == nil {
		slots = make(map[types.Hash]storageEntry, 8)
		w.buf.storage[address] = slots
	}
	v := value.Bytes()
	if len(v) > 32 {
		panic(fmt.Sprintf("BufferedPlainStateWriter.WriteAccountStorage: value.Bytes() len=%d > 32 (addr=%x slot=%x)",
			len(v), address, key))
	}
	if len(v) == 0 {
		slots[*key] = storageEntry{value: deletedSentinel}
	} else {
		slots[*key] = storageEntry{value: v}
	}
	w.markTouchedStor(address, *key)
	return nil
}

func (w *BufferedPlainStateWriter) CreateContract(address types.Address) error {
	if isTracked(address) {
		bn := uint64(0)
		if w.csw != nil {
			bn = w.csw.blockNumber
		}
		_, bufWiped := w.buf.wipedStorage[address]
		fmt.Printf("[TRACK] CreateContract blk=%d addr=%x bufWiped=%v contractWipes_len=%d buf.storage_slots=%d\n",
			bn, address[:], bufWiped, len(w.buf.contractWipes), len(w.buf.storage[address]))
	}
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
	// Invalidate every cached slot for this address. The flat composite-key
	// LRU has no per-address sub-map, so we evict each known slot individually.
	//
	// CRITICAL: we must NOT rely on `wipedSlots` for the LRU purge — that
	// set is for the storage CHANGESET (csw.recordStorageWipe) and drops
	// slots whose current visible value is empty (`len(entry.value)==0`).
	// But the LRU may hold a STALE non-zero value for exactly those slots:
	// e.g. an earlier commit interval flushed buf.storage[addr][slot]=V2
	// into MDBX and RefreshLRUForSnapshot wrote V2 into LRU; a later block
	// in this interval issues SSTORE slot=0, which sets buf[slot] to
	// deletedSentinel — so collectPreWipeSlots drops it and this loop
	// would leave V2 in the LRU. On the next block's read of `slot`, the
	// LRU returns V2 even though MDBX has been swept clean and the snap
	// tombstone has been refreshed past. That stale V2 propagates into
	// EVM SSTORE gas calc (orig=V2 → RESET path, 15K refund delta vs
	// the correct SET path). See project_lru_empty_slot_leftover memory.
	//
	// Correct set to purge = union of every slot key known to touch this
	// address: active buffer (incl. empty/deletedSentinel), in-flight
	// snapshot, AND MDBX scan (delegated via wipedSlots which already
	// merges MDBX rows). That covers every key that could sit in LRU.
	lruKeys := make(map[types.Hash]struct{}, len(wipedSlots)+len(w.buf.storage[address]))
	for slot := range wipedSlots {
		lruKeys[slot] = struct{}{}
	}
	if slots, ok := w.buf.storage[address]; ok {
		for slot := range slots {
			lruKeys[slot] = struct{}{}
		}
	}
	if snap := w.buf.inFlight.Load(); snap != nil {
		if slots, ok := snap.storage[address]; ok {
			for slot := range slots {
				lruKeys[slot] = struct{}{}
			}
		}
	}
	var compositeKey [storageCompositeKeyLen]byte
	copy(compositeKey[:20], address[:])
	for slot := range lruKeys {
		copy(compositeKey[20:], slot[:])
		w.buf.readStorage.Delete(compositeKey)
	}
	// Clear buffered storage for this address AFTER computing lruKeys so we
	// don't lose the buf slot list.
	delete(w.buf.storage, address)
	// Mark as wiped so ReadAccountStorage returns nil for slots not in buffer.
	w.buf.wipedStorage[address] = struct{}{}
	// Record address for MDBX storage wipe during FlushToMDBX.
	w.buf.contractWipes = append(w.buf.contractWipes, address)
	if isTracked(address) {
		bn := uint64(0)
		if w.csw != nil {
			bn = w.csw.blockNumber
		}
		fmt.Printf("[TRACK] CreateContract/done blk=%d addr=%x wipedSlots=%d contractWipes_len=%d\n",
			bn, address[:], len(wipedSlots), len(w.buf.contractWipes))
	}
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
	tracked := isTracked(address)
	// snap is captured once for the entire function so the MDBX-shadowing
	// loop sees the same snapshot we enumerated, even if the bg flush
	// completes mid-call and clears w.buf.inFlight.
	snap := w.buf.inFlight.Load()
	bufSlots, hasBuf := w.buf.storage[address]
	// If the CURRENT active buffer already wiped this address (a SELFDESTRUCT
	// earlier in the same commit interval produced a buf.wipedStorage entry
	// and buf.contractWipes append), MDBX still holds the pre-interval slots
	// — but they are logically gone. If a LATER block in the same interval
	// re-runs CreateContract (e.g. CREATE2 metamorphic pattern), we must NOT
	// re-harvest the stale MDBX slots here: csw.recordStorageWipe would then
	// write them to the NEW block's storcs entry as if they were pre-block
	// values, even though the true pre-block value is 0 (wiped). Forward
	// replay of that bogus storcs row does not currently break state root
	// (V* → 0 collides with 0 → 0), but the changeset's old_val lies about
	// history and downstream consumers that trust it (snapshot diffs,
	// history v3 inverted index, gas refund reasoning that walks back
	// storcs) silently break.
	_, bufWiped := w.buf.wipedStorage[address]

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
			if len(entry.value) > 32 {
				panic(fmt.Sprintf("BufferedPlainStateWriter.collectPreWipeSlots: bufSlots[slot].value len=%d > 32 (addr=%x slot=%x src=active_buffer)",
					len(entry.value), address, slot))
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
				if len(entry.value) > 32 {
					panic(fmt.Sprintf("BufferedPlainStateWriter.collectPreWipeSlots: inFlight.storage[addr][slot].value len=%d > 32 (addr=%x slot=%x src=inflight_snap)",
						len(entry.value), address, slot))
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

	// 2b. Active-buffer wipe: same semantics as in-flight wipe, but for a
	// SELFDESTRUCT earlier in the current (pre-snapshot) interval. MDBX
	// still holds the old rows but they are logically gone.
	if bufWiped {
		if tracked {
			fmt.Printf("[TRACK] collectPreWipeSlots addr=%x bufWiped=true early_return out_slots=%d\n",
				address[:], len(out))
		}
		return out, nil
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
			if len(v) > 32 {
				cursor.Close()
				panic(fmt.Sprintf("BufferedPlainStateWriter.collectPreWipeSlots: MDBX cursor v len=%d > 32 (addr=%x slot=%x src=mdbx_cursor)",
					len(v), address, slot))
			}
			out[slot] = v
		}
		cursor.Close()
	}

	if tracked {
		fmt.Printf("[TRACK] collectPreWipeSlots addr=%x out_slots=%d (via MDBX scan)\n",
			address[:], len(out))
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
