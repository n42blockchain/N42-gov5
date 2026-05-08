// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// s3fifo.go — Simple Scalable Scan-resistant FIFO cache.
//
// Reference: Yang, Jiacheng et al. "FIFO queues are all you need for
// cache eviction." SOSP 2023. https://dl.acm.org/doi/10.1145/3600006.3613147
//
// Three queues: small (S, 10%), main (M, 90%), ghost (G, key-only).
// New Put lands in S; one-touch entries are evicted from S to G
// without polluting M, giving the policy natural scan resistance —
// critical for ETH storage where each commit-interval RefreshLRU
// dumps 700K+ one-touch writes (NFT mints, ERC20 approvals).
//
// Eviction:
//   - S tail with freq>0 → promote to M (it has earned a place)
//   - S tail with freq==0 → drop key into G
//   - M tail with freq>0 → freq--, requeue at head (second chance)
//   - M tail with freq==0 → drop key into G
//
// Re-admission:
//   - Put for a key in G → insert directly into M (it was hot enough
//     to come back), bypass S
//
// Storage layout — pointer-free
// ─────────────────────────────
// Per-entry data lives in slab arrays indexed by a 32-bit slot id:
//
//	keys     []K         // K is a fixed-size byte array (e.g. [52]byte)
//	valBuf   []byte      // valSize × cap, contiguous; entry i at [i*valSize:(i+1)*valSize]
//	valLen   []uint8     // 0..valSize; 0 also means tombstone (caller treats nil and empty alike)
//	flags    []uint8     // bit 0: alive, bit 1: inMain, bits 2-3: freq (0..3)
//	next/prev []int32    // intrusive doubly-linked lists
//
// Doubly-linked free list reuses next[] (prev[] unused while free).
// Map from K → int32 slot id is the only structure with internal Go
// pointers; everything else is plain byte / int / uint slices that GC
// scans as one allocation each.
//
// For 250M storage entries (8192-block segment under heavy DeFi load)
// the previous *list.Element + *s3Entry layout pinned 700M+ live heap
// objects and made GC dominate ~90% of CPU; the slab layout collapses
// that to ~10 large allocations + the items map.

package state

import (
	"sync"
	"sync/atomic"
)

const (
	flagInMain uint8 = 1 << 1
	freqShift        = 2
	freqMask   uint8 = 0b1100 // bits 2-3
	freqMax    uint8 = 3
)

// nilSlot is the sentinel for an empty list pointer / no-such-slot id.
const nilSlot int32 = -1

// s3FIFO is a slot-based scan-resistant cache. Generic over K (any
// fixed-size comparable byte array — [20]byte, [52]byte, etc.); value
// width valSize is fixed at construction.
//
// Slabs are LAZY: cap starts small and grows up to maxCap on demand
// when the free list is exhausted. This matters because the natural
// bytes-budget for a real run (16 GiB storage) translates to ~119M
// slots, and pre-allocating 13 GiB up-front per cache made the test
// suite OOM-kill the host (each NewPlainStateBuffer in a test case
// would gobble that much). The cap budget still drives eviction; we
// just don't reserve all of it in RAM until we actually need it.
type s3FIFO[K comparable] struct {
	mu sync.Mutex

	cap    int // currently allocated slot count
	maxCap int // budget ceiling; cap may grow up to here
	sLen   int
	mLen    int
	valSize int

	keys   []K
	valBuf []byte
	valLen []uint8
	flags  []uint8
	next   []int32
	prev   []int32

	items map[K]int32

	sHead, sTail int32
	mHead, mTail int32
	free         int32

	// Ghost: ring of recently-evicted keys, used to detect re-admit.
	ghostKeys []K
	ghostMap  map[K]int32 // key → ring index
	ghostHead int
	ghostFull bool
	maxGhost  int

	hits   atomic.Uint64
	misses atomic.Uint64
}

// perSlotBytesEstimate matches the slab footprint used to translate a
// caller-supplied byte budget into a slot count. The 50 B addend is
// the empirical Go map bucket amortised overhead per entry (bucket
// header + tophash slot + value slot for int32) so the cap reflects
// real RSS, not just slab bytes.
func perSlotBytesEstimate[K comparable](valSize int) int {
	var k K
	return keySize(k) + valSize + 1 + 1 + 4 + 4 + 50
}

// keySize returns the byte width of K, hard-coded because Go generics
// don't expose type metadata at runtime. Supported K types are the
// fixed-size byte arrays used by readAccounts ([20]byte) and
// readStorage ([52]byte); anything else falls back to 32 B.
func keySize[K comparable](k K) int {
	switch any(k).(type) {
	case [20]byte:
		return 20
	case [32]byte:
		return 32
	case [52]byte:
		return 52
	}
	return 32
}

// initialCacheSlots is the slab size at construction. We grow on
// demand from here; allocating 1024 slots costs ~150 KB total which
// is fine even for tests that build many caches.
const initialCacheSlots = 1024

// newS3FIFO constructs a cache whose budget ceiling is capBytes. The
// initial slab size is small (initialCacheSlots) and grows up to the
// computed maxCap as Put pressure demands. valSize is the maximum
// value byte width per entry: 32 for storage (uint256), 80 for
// accounts (V2 encoding).
func newS3FIFO[K comparable](capBytes int64, valSize int) *s3FIFO[K] {
	perSlot := int64(perSlotBytesEstimate[K](valSize))
	if perSlot <= 0 {
		perSlot = 144 // fallback for unrecognised K
	}
	maxCap := int(capBytes / perSlot)
	if maxCap < initialCacheSlots {
		maxCap = initialCacheSlots
	}

	maxGhost := int(int64(100_000) * capBytes / (1 << 30))
	if maxGhost < 16384 {
		maxGhost = 16384
	}

	cap := initialCacheSlots
	if cap > maxCap {
		cap = maxCap
	}
	c := &s3FIFO[K]{
		cap:      cap,
		maxCap:   maxCap,
		valSize:  valSize,
		keys:     make([]K, cap),
		valBuf:   make([]byte, cap*valSize),
		valLen:   make([]uint8, cap),
		flags:    make([]uint8, cap),
		next:     make([]int32, cap),
		prev:     make([]int32, cap),
		items:    make(map[K]int32),
		sHead:    nilSlot,
		sTail:    nilSlot,
		mHead:    nilSlot,
		mTail:    nilSlot,
		free:     0,
		ghostMap: make(map[K]int32),
		maxGhost: maxGhost,
		// ghostKeys allocated lazily on first addGhost — would be
		// up to 1.6M × 52B = 80 MB upfront for a 16 GiB storage cache,
		// which is wasteful for short-lived test caches that never
		// evict. The cache works without ghosts (first-touch keys
		// land in S as they would on a cold cache).
	}
	for i := int32(0); i < int32(cap)-1; i++ {
		c.next[i] = i + 1
	}
	c.next[cap-1] = nilSlot
	return c
}

// growLocked extends slabs by doubling cap, capped at maxCap. Returns
// true if growth happened (caller can retry allocSlot). Caller must
// hold c.mu.
func (c *s3FIFO[K]) growLocked() bool {
	if c.cap >= c.maxCap {
		return false
	}
	newCap := c.cap * 2
	if newCap > c.maxCap {
		newCap = c.maxCap
	}
	delta := newCap - c.cap
	c.keys = append(c.keys, make([]K, delta)...)
	c.valBuf = append(c.valBuf, make([]byte, delta*c.valSize)...)
	c.valLen = append(c.valLen, make([]uint8, delta)...)
	c.flags = append(c.flags, make([]uint8, delta)...)
	c.next = append(c.next, make([]int32, delta)...)
	c.prev = append(c.prev, make([]int32, delta)...)
	// New slots become the new free-list head; the old head links the
	// tail of the new range so any pre-existing free entries remain
	// reachable.
	for i := int32(c.cap); i < int32(newCap)-1; i++ {
		c.next[i] = i + 1
	}
	c.next[newCap-1] = c.free
	c.free = int32(c.cap)
	c.cap = newCap
	return true
}

func (c *s3FIFO[K]) freq(idx int32) uint8 { return (c.flags[idx] & freqMask) >> freqShift }

func (c *s3FIFO[K]) setFreq(idx int32, f uint8) {
	if f > freqMax {
		f = freqMax
	}
	c.flags[idx] = (c.flags[idx] &^ freqMask) | (f << freqShift)
}

func (c *s3FIFO[K]) bumpFreq(idx int32) {
	if f := c.freq(idx); f < freqMax {
		c.setFreq(idx, f+1)
	}
}

// allocSlot pops the free list. Returns nilSlot when exhausted (caller
// must evict first). With pre-sized cap and eviction running before
// every Put, this only fires during a transient brief overshoot.
func (c *s3FIFO[K]) allocSlot() int32 {
	if c.free == nilSlot {
		return nilSlot
	}
	idx := c.free
	c.free = c.next[idx]
	return idx
}

// freeSlot returns the slot to the free list and clears flags.
func (c *s3FIFO[K]) freeSlot(idx int32) {
	c.flags[idx] = 0
	c.valLen[idx] = 0
	c.prev[idx] = nilSlot
	c.next[idx] = c.free
	c.free = idx
}

func (c *s3FIFO[K]) listPushFront(head, tail *int32, idx int32) {
	c.prev[idx] = nilSlot
	c.next[idx] = *head
	if *head != nilSlot {
		c.prev[*head] = idx
	} else {
		*tail = idx
	}
	*head = idx
}

func (c *s3FIFO[K]) listRemove(head, tail *int32, idx int32) {
	p, n := c.prev[idx], c.next[idx]
	if p != nilSlot {
		c.next[p] = n
	} else {
		*head = n
	}
	if n != nilSlot {
		c.prev[n] = p
	} else {
		*tail = p
	}
	c.prev[idx] = nilSlot
	c.next[idx] = nilSlot
}

// writeValue copies value into slot idx's slab segment, capping at
// valSize. Stores actual length in valLen[idx]. nil/empty value stores
// length 0 (caller treats both as "absent" / negative cache).
func (c *s3FIFO[K]) writeValue(idx int32, value []byte) {
	n := len(value)
	if n > c.valSize {
		n = c.valSize
	}
	if n > 0 {
		copy(c.valBuf[int(idx)*c.valSize:int(idx)*c.valSize+n], value)
	}
	c.valLen[idx] = uint8(n)
}

// copyValue returns a freshly allocated slice with the slot's value
// bytes. Callers receive a stable copy so a concurrent Put of the same
// key (which would overwrite the slab segment) cannot mutate bytes
// they're still reading. The previous design (aliased return) tripped
// the race detector via main↔prefetcher Put/Get on the same address.
func (c *s3FIFO[K]) copyValue(idx int32) []byte {
	n := int(c.valLen[idx])
	if n == 0 {
		return nil
	}
	off := int(idx) * c.valSize
	out := make([]byte, n)
	copy(out, c.valBuf[off:off+n])
	return out
}

// Get returns the value for key and bumps its frequency. The returned
// slice is a fresh copy — safe to retain across concurrent Puts.
func (c *s3FIFO[K]) Get(key K) (value []byte, present bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.items[key]
	if !ok {
		c.misses.Add(1)
		return nil, false
	}
	c.bumpFreq(idx)
	c.hits.Add(1)
	return c.copyValue(idx), true
}

// Put inserts or updates key. New keys land in S; keys present in
// the ghost set get re-admitted directly to M.
func (c *s3FIFO[K]) Put(key K, value []byte, cost int) {
	_ = cost // accounting now slot-based; cost arg kept for API compat
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictUntilFreeLocked()
	c.putLocked(key, value)
}

// PutBatch is the batch form used by RefreshLRUForSnapshot. Single
// mutex acquisition + amortised eviction sweep.
func (c *s3FIFO[K]) PutBatch(keys []K, values [][]byte, costs []int) {
	if len(keys) == 0 {
		return
	}
	_ = costs
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, k := range keys {
		c.evictUntilFreeLocked()
		c.putLocked(k, values[i])
	}
}

// PutIfAbsent inserts only when key is not already present. Returns true
// if inserted, false if the key already had a value (including a
// tombstone with valLen=0). Designed for the async prefetcher: it can
// stale-read MDBX concurrently with the executor's flush+RefreshLRU,
// so blindly Putting risks overwriting a fresh tombstone (or any newer
// value RefreshLRU just wrote) with a stale snapshot read. PutIfAbsent
// says "only fill the gap; never overwrite". This preserves prefetch
// warmth (fills empty LRU entries) without contaminating values
// RefreshLRU is responsible for.
func (c *s3FIFO[K]) PutIfAbsent(key K, value []byte, cost int) bool {
	_ = cost
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; ok {
		return false
	}
	c.evictUntilFreeLocked()
	c.putLocked(key, value)
	return true
}

func (c *s3FIFO[K]) putLocked(key K, value []byte) {
	// Update existing entry in S or M.
	if idx, ok := c.items[key]; ok {
		c.writeValue(idx, value)
		c.bumpFreq(idx)
		return
	}
	idx := c.allocSlot()
	if idx == nilSlot {
		// Eviction couldn't free a slot — happens only when cap=0
		// or every entry is locked into freq>0 and queues are tiny.
		// Safe fallback: drop the insert.
		return
	}
	c.keys[idx] = key
	c.writeValue(idx, value)
	c.flags[idx] = 0
	// Re-admission from ghost: hot enough to come back, skip S.
	if _, ok := c.ghostMap[key]; ok {
		c.removeGhost(key)
		c.flags[idx] |= flagInMain
		c.listPushFront(&c.mHead, &c.mTail, idx)
		c.mLen++
	} else {
		c.listPushFront(&c.sHead, &c.sTail, idx)
		c.sLen++
	}
	c.items[key] = idx
}

// evictUntilFreeLocked ensures at least one free slot is available.
// Strategy: grow slabs first (free real estate is cheaper than
// evicting), and only fall back to eviction once we're at maxCap.
// Steady-state past warmup, growth has stopped and this is one
// eviction per Put.
func (c *s3FIFO[K]) evictUntilFreeLocked() {
	if c.free != nilSlot {
		return
	}
	if c.growLocked() {
		return
	}
	// S keeps ~10% of currently-allocated capacity; recomputed each
	// pass so growLocked doesn't leave it stale during warmup.
	for c.free == nilSlot && (c.sLen > 0 || c.mLen > 0) {
		sCap := c.cap / 10
		if sCap < 16 {
			sCap = 16
		}
		if c.sLen > sCap || c.mLen == 0 {
			if !c.evictSLocked() && !c.evictMLocked() {
				return
			}
		} else {
			if !c.evictMLocked() && !c.evictSLocked() {
				return
			}
		}
	}
}

// evictSLocked pops S tail. Promotes to M if it earned at least one
// access; otherwise drops the key into G.
func (c *s3FIFO[K]) evictSLocked() bool {
	idx := c.sTail
	if idx == nilSlot {
		return false
	}
	c.listRemove(&c.sHead, &c.sTail, idx)
	c.sLen--
	if c.freq(idx) > 0 {
		c.setFreq(idx, 0)
		c.flags[idx] |= flagInMain
		c.listPushFront(&c.mHead, &c.mTail, idx)
		c.mLen++
		return true
	}
	key := c.keys[idx]
	delete(c.items, key)
	c.addGhost(key)
	c.freeSlot(idx)
	return true
}

// evictMLocked walks M's tail with the second-chance rule. Bounded
// to 32 demotions per call so a fully-hot M can't deadlock eviction.
func (c *s3FIFO[K]) evictMLocked() bool {
	for i := 0; i < 32; i++ {
		idx := c.mTail
		if idx == nilSlot {
			return false
		}
		if c.freq(idx) > 0 {
			c.setFreq(idx, c.freq(idx)-1)
			c.listRemove(&c.mHead, &c.mTail, idx)
			c.listPushFront(&c.mHead, &c.mTail, idx)
			continue
		}
		c.listRemove(&c.mHead, &c.mTail, idx)
		c.mLen--
		key := c.keys[idx]
		delete(c.items, key)
		c.addGhost(key)
		c.freeSlot(idx)
		return true
	}
	// All 32 still had freq>0; force-evict current tail to make
	// progress instead of looping forever.
	idx := c.mTail
	if idx == nilSlot {
		return false
	}
	c.listRemove(&c.mHead, &c.mTail, idx)
	c.mLen--
	key := c.keys[idx]
	delete(c.items, key)
	c.addGhost(key)
	c.freeSlot(idx)
	return true
}

func (c *s3FIFO[K]) addGhost(key K) {
	if c.maxGhost == 0 {
		return
	}
	if _, ok := c.ghostMap[key]; ok {
		return
	}
	if c.ghostKeys == nil {
		c.ghostKeys = make([]K, c.maxGhost)
	}
	if c.ghostFull {
		oldKey := c.ghostKeys[c.ghostHead]
		delete(c.ghostMap, oldKey)
	}
	c.ghostKeys[c.ghostHead] = key
	c.ghostMap[key] = int32(c.ghostHead)
	c.ghostHead++
	if c.ghostHead >= c.maxGhost {
		c.ghostHead = 0
		c.ghostFull = true
	}
}

func (c *s3FIFO[K]) removeGhost(key K) {
	pos, ok := c.ghostMap[key]
	if !ok {
		return
	}
	delete(c.ghostMap, key)
	if c.ghostKeys != nil {
		// Tombstone the ring slot with a zero key; addGhost will
		// reuse it when wrap-around hits.
		var zero K
		c.ghostKeys[pos] = zero
	}
}

// Delete removes a key from the cache. Returns true if it existed.
func (c *s3FIFO[K]) Delete(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deleteLocked(key)
}

// DeleteBatch removes a batch of keys under a single lock acquisition.
func (c *s3FIFO[K]) DeleteBatch(keys []K) {
	if len(keys) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		c.deleteLocked(k)
	}
}

// Range visits every key currently in the cache (excluding ghost
// entries). Used by SELFDESTRUCT-aware LRU invalidation: walks
// c.items O(n), only run from the post-flush LRU refresh path.
func (c *s3FIFO[K]) Range(visit func(K) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		if !visit(k) {
			return
		}
	}
}

func (c *s3FIFO[K]) deleteLocked(key K) bool {
	idx, ok := c.items[key]
	if !ok {
		return false
	}
	if c.flags[idx]&flagInMain != 0 {
		c.listRemove(&c.mHead, &c.mTail, idx)
		c.mLen--
	} else {
		c.listRemove(&c.sHead, &c.sTail, idx)
		c.sLen--
	}
	delete(c.items, key)
	c.freeSlot(idx)
	return true
}

// Reset drops all entries (including the ghost set).
func (c *s3FIFO[K]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.flags {
		c.flags[i] = 0
		c.valLen[i] = 0
	}
	for i := int32(0); i < int32(c.cap)-1; i++ {
		c.next[i] = i + 1
		c.prev[i] = nilSlot
	}
	c.next[c.cap-1] = nilSlot
	c.prev[c.cap-1] = nilSlot
	c.free = 0
	c.sHead, c.sTail, c.mHead, c.mTail = nilSlot, nilSlot, nilSlot, nilSlot
	c.sLen, c.mLen = 0, 0
	c.items = make(map[K]int32)
	c.ghostMap = make(map[K]int32)
	c.ghostKeys = nil
	c.ghostHead = 0
	c.ghostFull = false
}

// Stats returns (hits, misses, currentBytes, entries). currentBytes
// approximates RSS via the per-slot estimate × live entries.
func (c *s3FIFO[K]) Stats() (hits, misses uint64, bytes int64, entries int) {
	c.mu.Lock()
	live := c.sLen + c.mLen
	c.mu.Unlock()
	per := int64(perSlotBytesEstimate[K](c.valSize))
	return c.hits.Load(), c.misses.Load(), int64(live) * per, live
}

// ResetStats clears hit/miss counters without touching cache contents.
func (c *s3FIFO[K]) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
}
