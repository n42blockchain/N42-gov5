// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
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
type BaseCache struct {
	mu   sync.RWMutex
	accs map[types.Address]*account.StateAccount // nil value = the account is absent
}

// NewBaseCache returns a cache sized for a block's distinct accounts.
func NewBaseCache(hint int) *BaseCache {
	if hint < 64 {
		hint = 64
	}
	return &BaseCache{accs: make(map[types.Address]*account.StateAccount, hint)}
}

// get reports a cached account (which may be nil for "absent") and whether the
// address was cached at all.
func (c *BaseCache) get(addr types.Address) (*account.StateAccount, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	acc, ok := c.accs[addr]
	c.mu.RUnlock()
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
	c.mu.Lock()
	c.accs[addr] = stored
	c.mu.Unlock()
}

// Len reports how many addresses are cached (tests and diagnostics).
func (c *BaseCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.accs)
}
