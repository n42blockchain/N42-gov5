// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"encoding/binary"
	"sync"

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
// The map is split into baseCacheShards shards, each with its own lock. With
// one lock, every first read of an account took it exclusively while 31 other
// workers queued their shared reads behind it: in the P3 transfer benchmark
// (internal/transfer_block_bench_test.go) BaseCache put and get were 99% of
// the exec wave's blocked time, about 60% of the workers' wall time -- the
// off-CPU wait the S79b profile found on the fleet.
type BaseCache struct {
	shards [baseCacheShards]baseCacheShard
}

const baseCacheShards = 256

type baseCacheShard struct {
	mu   sync.RWMutex
	accs map[types.Address]*account.StateAccount // nil value = the account is absent
	_    [32]byte                                // keep neighbouring shard locks off one cache line
}

// NewBaseCache returns a cache sized for a block's distinct accounts.
func NewBaseCache(hint int) *BaseCache {
	if hint < 64 {
		hint = 64
	}
	per := hint/baseCacheShards + 1
	c := &BaseCache{}
	for i := range c.shards {
		c.shards[i].accs = make(map[types.Address]*account.StateAccount, per)
	}
	return c
}

// shard picks addr's shard. Addresses are hash-like, but a few are not (the
// zero address, precompiles, vanity prefixes), so bytes from both ends mix.
func (c *BaseCache) shard(addr types.Address) *baseCacheShard {
	h := binary.LittleEndian.Uint64(addr[0:8]) ^ binary.LittleEndian.Uint64(addr[12:20])
	h *= 0x9E3779B97F4A7C15
	return &c.shards[h>>56]
}

// get reports a cached account (which may be nil for "absent") and whether the
// address was cached at all.
func (c *BaseCache) get(addr types.Address) (*account.StateAccount, bool) {
	if c == nil {
		return nil, false
	}
	sh := c.shard(addr)
	sh.mu.RLock()
	acc, ok := sh.accs[addr]
	sh.mu.RUnlock()
	if !ok || acc == nil {
		return nil, ok
	}
	cp := *acc
	return &cp, true
}

// put records a base read. A nil account records "absent", which is as useful
// to cache as a present one: a block's transfers create accounts that every
// later transaction would otherwise look for in the tree.
func (c *BaseCache) put(addr types.Address, acc *account.StateAccount) {
	if c == nil {
		return
	}
	var stored *account.StateAccount
	if acc != nil {
		cp := *acc
		stored = &cp
	}
	sh := c.shard(addr)
	sh.mu.Lock()
	sh.accs[addr] = stored
	sh.mu.Unlock()
}

// Len reports how many addresses are cached (tests and diagnostics).
func (c *BaseCache) Len() int {
	if c == nil {
		return 0
	}
	n := 0
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.RLock()
		n += len(sh.accs)
		sh.mu.RUnlock()
	}
	return n
}
