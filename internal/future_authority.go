// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Future-queue provenance (sync review S0b / C3).
//
// processFutureBlocks used to retry EVERY queued block with branch-switch
// authority, while the queue is fed by any peer: direct push, gossip, range
// catch-up and every non-authorized import that hit an unknown ancestor. A
// block that InsertChain had refused to switch to got that authority two
// seconds later, so a pushed sibling of the applied-but-uncommitted head
// could unwind the live speculative head.
//
// Authority is now a property of the hash, recorded when the block is queued
// by an import that itself carried consensus authority (fetch-on-miss of a
// proposal / commit target or the hash chain below one, the miner's
// consensus-parent import). Push, gossip and range catch-up entries retry
// with plain InsertChain: they may still import onto the applied head or be
// stored as side blocks, but never switch branches. A later authorized queue
// of the same hash upgrades it; a passive one never downgrades it.
// N42_SYNC_LEGACY_FUTURE_AUTHORITY=1 restores the old retry.

package internal

import (
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// futureAuthority is the set of future-queued hashes that may be retried with
// branch-switch authority. The zero value is ready to use.
type futureAuthority struct {
	mu sync.Mutex
	m  map[types.Hash]struct{}
}

func (f *futureAuthority) mark(h types.Hash, queued func(types.Hash) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = make(map[types.Hash]struct{})
	}
	if len(f.m) >= 4*maxFutureBlocks {
		// Entries whose block left the queue without a retry (evicted, or
		// imported by another path) are dropped here.
		for k := range f.m {
			if !queued(k) {
				delete(f.m, k)
			}
		}
		if len(f.m) >= 4*maxFutureBlocks {
			f.m = make(map[types.Hash]struct{})
		}
	}
	f.m[h] = struct{}{}
}

func (f *futureAuthority) has(h types.Hash) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.m[h]
	return ok
}

func (f *futureAuthority) drop(h types.Hash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, h)
}

// addFutureBlock queues blk from inside an import. authorized is the
// branch-switch authority of that import; it is kept for the retry only when
// the import was consensus-driven (not a range catch-up, see
// InsertChainCatchUp). Caller holds bc.lock.
func (bc *BlockChain) addFutureBlock(blk block.IBlock, authorized bool) error {
	if err := bc.AddFutureBlock(blk); err != nil {
		return err
	}
	if authorized && !bc.futureQueuePassive {
		bc.futureAuth.mark(blk.Hash(), bc.futureBlocks.Contains)
	}
	return nil
}

// futureRetryAuthorized reports whether a queued block is retried with
// branch-switch authority.
func (bc *BlockChain) futureRetryAuthorized(h types.Hash) bool {
	if synclegacy.FutureAuthority.On() {
		return true
	}
	return bc.futureAuth.has(h)
}

// InsertChainCatchUp imports blocks with branch-switch authority for THIS
// call only: blocks it cannot import yet are future-queued without
// authority, so peer-served range data never gains authority on a retry.
// Used by the range catch-up's equal-height fork recovery.
func (bc *BlockChain) InsertChainCatchUp(chain []block.IBlock) (int, error) {
	if len(chain) == 0 {
		return 0, nil
	}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	if !synclegacy.FutureAuthority.On() {
		bc.futureQueuePassive = true
		defer func() { bc.futureQueuePassive = false }()
	}
	return bc.insertChain(chain, true)
}
