// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashed_read_cache.go — cross-block read cache for hashed-canonical
// execution.
//
// Profiling archive catch-up (staged, hashed-canonical) shows MDBX point
// reads at ~35% of total CPU (cursor_get_val under cgocall) plus ~7% keccak,
// because HashedStateReader hashes the key and round-trips MDBX on EVERY
// account/slot/code access with no reuse across blocks — a hot contract
// (router/stable/oracle) re-pays the full cost on every block it is touched
// in.
//
// Correctness model — INVALIDATE, never update: the cache stores the exact
// bytes MDBX returned (hits and misses both). Writers (staged
// HashOnlyComputer, live TrieRootComputer) call Invalidate* on every forward
// Put/Delete of HashedAccounts/HashedStorage, so a dirtied key simply misses
// and refills from MDBX with byte-true values. The cache never fabricates an
// encoding, so it cannot diverge from what an uncached read would see. Any
// account deletion wholesale-purges the storage tier (post-Cancun SELFDESTRUCT
// wipes are same-tx-create only, so this is essentially free) — that closes
// the "stale slots of a wiped account" class without per-address indexing.
// Reorg unwind must call PurgeAll.
//
// Safety net: hashed-canonical execution verifies the incremental state root
// against header.Root (per block live, per sub-batch staged), so a cache bug
// halts loudly instead of silently corrupting.
//
// The keccak memo (addr/slot → hash) is a pure-function cache and needs no
// invalidation. The code cache is keyed by codeHash — content-addressed and
// immutable — and likewise never invalidated.

package state

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Default entry counts. acc ~150B, sto ~120B, keccak memo ~60B, code ~5-10KB:
// roughly 300MB + 1GB + 120MB + 200MB. Override via env; 0 disables a tier.
const (
	defaultHashedAccEntries    = 2_000_000
	defaultHashedStoEntries    = 8_000_000
	defaultHashedKeccakEntries = 2_000_000
	defaultHashedCodeEntries   = 20_000
)

type hashedAccEntry struct {
	enc     []byte
	present bool
}

type hashedStoEntry struct {
	val     []byte
	present bool
	gen     uint64 // wipe generation of the owning account at fill time
}

// HashedReadCache is shared across blocks (and goroutine-safe via the LRUs'
// internal locking). One instance per node, hung off the execution adapter.
type HashedReadCache struct {
	acc      *lru.Cache[[32]byte, hashedAccEntry] // addrHash → account enc
	sto      *lru.Cache[[64]byte, hashedStoEntry] // addrHash||slotHash → value
	addrMemo *lru.Cache[types.Address, [32]byte]  // keccak(addr) memo
	slotMemo *lru.Cache[types.Hash, [32]byte]     // keccak(slot) memo
	code     *lru.Cache[types.Hash, []byte]       // codeHash → raw code (immutable)

	// Per-account wipe generations: a storage entry is valid only if its gen
	// matches the account's current one, so wiping an account invalidates all
	// its cached slots in O(1) without nuking the whole tier.
	genMu sync.RWMutex
	gens  map[[32]byte]uint64

	// Hit/miss counters (atomic; read+reset via StatsAndReset for per-batch
	// effectiveness logging).
	accHit, accMiss, stoHit, stoMiss, codeHit, codeMiss atomic.Uint64
}

// StatsAndReset returns and zeroes the hit/miss counters.
func (c *HashedReadCache) StatsAndReset() (accHit, accMiss, stoHit, stoMiss, codeHit, codeMiss uint64) {
	if c == nil {
		return
	}
	return c.accHit.Swap(0), c.accMiss.Swap(0),
		c.stoHit.Swap(0), c.stoMiss.Swap(0),
		c.codeHit.Swap(0), c.codeMiss.Swap(0)
}

func (c *HashedReadCache) genOf(addrHash [32]byte) uint64 {
	c.genMu.RLock()
	g := c.gens[addrHash]
	c.genMu.RUnlock()
	return g
}

// NewHashedReadCache builds the cache with env-tunable sizes:
// N42_HASHED_ACC_CACHE / N42_HASHED_STO_CACHE / N42_HASHED_KECCAK_CACHE /
// N42_HASHED_CODE_CACHE (entry counts; 0 disables that tier).
func NewHashedReadCache() *HashedReadCache {
	c := &HashedReadCache{gens: make(map[[32]byte]uint64)}
	if n := envEntries("N42_HASHED_ACC_CACHE", defaultHashedAccEntries); n > 0 {
		c.acc, _ = lru.New[[32]byte, hashedAccEntry](n)
	}
	if n := envEntries("N42_HASHED_STO_CACHE", defaultHashedStoEntries); n > 0 {
		c.sto, _ = lru.New[[64]byte, hashedStoEntry](n)
	}
	if n := envEntries("N42_HASHED_KECCAK_CACHE", defaultHashedKeccakEntries); n > 0 {
		c.addrMemo, _ = lru.New[types.Address, [32]byte](n)
		c.slotMemo, _ = lru.New[types.Hash, [32]byte](n)
	}
	if n := envEntries("N42_HASHED_CODE_CACHE", defaultHashedCodeEntries); n > 0 {
		c.code, _ = lru.New[types.Hash, []byte](n)
	}
	return c
}

func envEntries(env string, def int) int {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// AddrHash returns keccak256(addr), memoized.
func (c *HashedReadCache) AddrHash(addr types.Address) [32]byte {
	if c != nil && c.addrMemo != nil {
		if h, ok := c.addrMemo.Get(addr); ok {
			return h
		}
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(addr[:]))
	if c != nil && c.addrMemo != nil {
		c.addrMemo.Add(addr, h)
	}
	return h
}

// SlotHash returns keccak256(slot), memoized.
func (c *HashedReadCache) SlotHash(slot types.Hash) [32]byte {
	if c != nil && c.slotMemo != nil {
		if h, ok := c.slotMemo.Get(slot); ok {
			return h
		}
	}
	var h [32]byte
	copy(h[:], crypto.Keccak256(slot[:]))
	if c != nil && c.slotMemo != nil {
		c.slotMemo.Add(slot, h)
	}
	return h
}

// GetAccount returns (enc, present, cached). cached=false → caller must read
// MDBX and PutAccount the result.
func (c *HashedReadCache) GetAccount(addrHash [32]byte) ([]byte, bool, bool) {
	if c == nil || c.acc == nil {
		return nil, false, false
	}
	e, ok := c.acc.Get(addrHash)
	if !ok {
		c.accMiss.Add(1)
		return nil, false, false
	}
	c.accHit.Add(1)
	return e.enc, e.present, true
}

// PutAccount memoizes an MDBX read result (enc may be nil for absent). The
// bytes are COPIED: MDBX GetOne returns a view into the B-tree page, which a
// later Put rewrites in place — caching the raw slice would silently mutate.
func (c *HashedReadCache) PutAccount(addrHash [32]byte, enc []byte) {
	if c == nil || c.acc == nil {
		return
	}
	var cp []byte
	if len(enc) > 0 {
		cp = append([]byte(nil), enc...)
	}
	c.acc.Add(addrHash, hashedAccEntry{enc: cp, present: len(cp) > 0})
}

// GetStorage returns (val, present, cached). An entry whose owning account
// was wiped since fill time (gen mismatch) reads as a miss.
func (c *HashedReadCache) GetStorage(composite [64]byte) ([]byte, bool, bool) {
	if c == nil || c.sto == nil {
		return nil, false, false
	}
	e, ok := c.sto.Get(composite)
	if !ok {
		c.stoMiss.Add(1)
		return nil, false, false
	}
	var ah [32]byte
	copy(ah[:], composite[:32])
	if e.gen != c.genOf(ah) {
		c.stoMiss.Add(1)
		return nil, false, false
	}
	c.stoHit.Add(1)
	return e.val, e.present, true
}

// PutStorage memoizes an MDBX storage read (val may be nil for absent slot).
// Bytes copied — see PutAccount.
func (c *HashedReadCache) PutStorage(composite [64]byte, val []byte) {
	if c == nil || c.sto == nil {
		return
	}
	var cp []byte
	if len(val) > 0 {
		cp = append([]byte(nil), val...)
	}
	var ah [32]byte
	copy(ah[:], composite[:32])
	c.sto.Add(composite, hashedStoEntry{val: cp, present: len(cp) > 0, gen: c.genOf(ah)})
}

// GetCode / PutCode cache raw deployed bytecode by codeHash (immutable).
func (c *HashedReadCache) GetCode(codeHash types.Hash) ([]byte, bool) {
	if c == nil || c.code == nil {
		return nil, false
	}
	raw, ok := c.code.Get(codeHash)
	if ok {
		c.codeHit.Add(1)
	} else {
		c.codeMiss.Add(1)
	}
	return raw, ok
}

func (c *HashedReadCache) PutCode(codeHash types.Hash, raw []byte) {
	if c == nil || c.code == nil || len(raw) == 0 {
		return
	}
	// Copy — raw may be an MDBX page view (see PutAccount).
	c.code.Add(codeHash, append([]byte(nil), raw...))
}

// InvalidateAccount drops the cached account for addrHash. Call on every
// forward Put/Delete of HashedAccounts.
func (c *HashedReadCache) InvalidateAccount(addrHash [32]byte) {
	if c == nil || c.acc == nil {
		return
	}
	c.acc.Remove(addrHash)
}

// InvalidateStorage drops one cached slot (64B addrHash||slotHash key). Call
// on every forward Put/Delete of HashedStorage.
func (c *HashedReadCache) InvalidateStorage(composite [64]byte) {
	if c == nil || c.sto == nil {
		return
	}
	c.sto.Remove(composite)
}

// PurgeAccountStorage invalidates every cached slot of ONE account by bumping
// its wipe generation (O(1); stale entries age out of the LRU naturally).
// Called when an account is deleted/wiped — including the every-block EIP-158
// empty-account cleanup, which is why this must not nuke the whole tier.
func (c *HashedReadCache) PurgeAccountStorage(addrHash [32]byte) {
	if c == nil || c.sto == nil {
		return
	}
	c.genMu.Lock()
	c.gens[addrHash]++
	// Bound the gen map: it only grows on account deletions; reset wholesale
	// if it somehow balloons (invalidates everything — correct, just cold).
	if len(c.gens) > 1_000_000 {
		c.gens = make(map[[32]byte]uint64)
		c.genMu.Unlock()
		c.sto.Purge()
		return
	}
	c.genMu.Unlock()
}

// PurgeAll clears every invalidatable tier (accounts + storage). Call on
// reorg unwind / batch-tx boundaries. The keccak memo and code cache are
// pure/immutable and stay.
func (c *HashedReadCache) PurgeAll() {
	if c == nil {
		return
	}
	if c.acc != nil {
		c.acc.Purge()
	}
	if c.sto != nil {
		c.sto.Purge()
	}
	c.genMu.Lock()
	c.gens = make(map[[32]byte]uint64)
	c.genMu.Unlock()
}
