// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"encoding/binary"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// BaseCache memoises reads of the BASE state of one block -- the parent's
// post-state, which cannot change while that block executes: every write a
// transaction makes goes to the MVS, and the readers consult the MVS first.
//
// Without it every transaction pays its own base read for the accounts it
// touches, because the parallel executor gives each transaction its own
// IntraBlockState. On the bench's blocks that is ~163,000 reads for ~23,000
// distinct accounts a block on every node (35zzo profile: ReadAccountData 3.1%
// of a follower's CPU, and the Prague delegation check alone 2.9%, which is
// where the recipient of every transfer gets read even though the transfer
// itself credits it through the delta buffer without reading).
//
// One cache per block, shared by every worker. Values are copied in and out, so
// a caller that mutates what it gets back cannot corrupt the cache.
//
// The cache is split into baseCacheShards shards. A lookup takes no lock:
// each shard is an insert-only open-addressing table read with atomic loads
// (the MVS's index, mvs.go). Its RWMutex predecessor made every get an
// atomic add on a shared reader count and queued the gets behind each
// first-read put: with the MVS lock-free, BaseCache put and get were 80% of
// the P3 transfer benchmark's blocked time (internal/transfer_block_bench_test.go).
type BaseCache struct {
	shards [baseCacheShards]baseCacheShard
}

const baseCacheShards = 256

type baseCacheShard struct {
	mu  sync.Mutex // serialises inserts and growth; lookups skip it
	tab atomic.Pointer[baseTable]
	n   int // entries in tab, under mu
	_   [40]byte
}

// baseEntry is one cached base read; acc nil records "absent". Set before
// publication, never changed.
type baseEntry struct {
	addr types.Address
	hash uint64
	acc  *account.StateAccount
}

// baseTable is an insert-only linear-probing table (see mvTable).
type baseTable struct {
	mask  uint64
	slots []atomic.Pointer[baseEntry]
}

func newBaseTable(size int) *baseTable {
	return &baseTable{mask: uint64(size - 1), slots: make([]atomic.Pointer[baseEntry], size)}
}

func (t *baseTable) find(addr *types.Address, h uint64) *baseEntry {
	for i := h & t.mask; ; i = (i + 1) & t.mask {
		e := t.slots[i].Load()
		if e == nil {
			return nil
		}
		if e.hash == h && e.addr == *addr {
			return e
		}
	}
}

func (t *baseTable) place(e *baseEntry) {
	for i := e.hash & t.mask; ; i = (i + 1) & t.mask {
		if t.slots[i].Load() == nil {
			t.slots[i].Store(e)
			return
		}
	}
}

// NewBaseCache returns a cache sized for a block's distinct accounts.
func NewBaseCache(hint int) *BaseCache {
	if hint < 64 {
		hint = 64
	}
	per := 16
	for per*3 < (hint/baseCacheShards+1)*4 {
		per *= 2
	}
	c := &BaseCache{}
	for i := range c.shards {
		c.shards[i].tab.Store(newBaseTable(per))
	}
	return c
}

// addressHash mixes every byte of addr. Addresses are hash-like, but a few
// are not (the zero address, precompiles, vanity prefixes). The top byte
// picks the shard, the low bits the slot.
func addressHash(addr *types.Address) uint64 {
	h := binary.LittleEndian.Uint64(addr[0:8])*0x9E3779B97F4A7C15 ^
		binary.LittleEndian.Uint64(addr[8:16])*0xC2B2AE3D27D4EB4F ^
		uint64(binary.LittleEndian.Uint32(addr[16:20]))*0x165667B19E3779F9
	h ^= h >> 31
	h *= 0x9E3779B97F4A7C15
	h ^= h >> 29
	return h
}

// get reports a cached account (which may be nil for "absent") and whether the
// address was cached at all.
func (c *BaseCache) get(addr types.Address) (*account.StateAccount, bool) {
	if c == nil {
		return nil, false
	}
	h := addressHash(&addr)
	e := c.shards[h>>56].tab.Load().find(&addr, h)
	if e == nil {
		return nil, false
	}
	if e.acc == nil {
		return nil, true
	}
	cp := *e.acc
	return &cp, true
}

// put records a base read. A nil account records "absent", which is as useful
// to cache as a present one: a block's transfers create accounts that every
// later transaction would otherwise look for in the tree. The first put of an
// address wins; a racing second read of the same base state is identical.
func (c *BaseCache) put(addr types.Address, acc *account.StateAccount) {
	if c == nil {
		return
	}
	h := addressHash(&addr)
	sh := &c.shards[h>>56]
	if sh.tab.Load().find(&addr, h) != nil {
		return
	}
	e := &baseEntry{addr: addr, hash: h}
	if acc != nil {
		cp := *acc
		e.acc = &cp
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	t := sh.tab.Load()
	if t.find(&addr, h) != nil {
		return
	}
	if (sh.n+1)*4 > len(t.slots)*3 {
		g := newBaseTable(len(t.slots) * 2)
		for i := range t.slots {
			if old := t.slots[i].Load(); old != nil {
				g.place(old)
			}
		}
		sh.tab.Store(g)
		t = g
	}
	t.place(e)
	sh.n++
}

// Len reports how many addresses are cached (tests and diagnostics).
func (c *BaseCache) Len() int {
	if c == nil {
		return 0
	}
	n := 0
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		n += sh.n
		sh.mu.Unlock()
	}
	return n
}
