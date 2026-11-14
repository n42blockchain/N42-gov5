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
// Block-committed notifications, wait helpers and the latest-block channel.

package internal

import (
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// WaitBlockPersisted blocks until the given block's header is readable (its
// write transaction committed — headers land atomically with state) or the
// timeout passes. The early-vote path lets a view advance while the previous
// block's persistence is still in flight, so a leader triggered at the view
// change may need to wait a few milliseconds before building on it.
// WaitBlockApplied waits until hash is the applied head (the QMDB applied
// marker), the state a build on it must read. Persisted is not enough: a
// sibling's header is stored before its state is applied.
func (bc *BlockChain) WaitBlockApplied(hash types.Hash, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if bc.AppliedHeadIs(hash) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-bc.ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (bc *BlockChain) WaitBlockPersisted(hash types.Hash, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		found := false
		_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
			found = rawdb.ReadHeaderNumber(tx, hash) != nil
			return nil
		})
		if found {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-bc.ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// SetOnBlockCommitted installs a generic canonical-commit hook, called
// alongside the ExEx notification for every newly-canonical block on every
// node (leader or follower) — unlike consensus.BlockImportNotifier, which is
// a single HotStuff-owned slot, this is a plain callback any package-agnostic
// consumer can attach without introducing a blockchain -> consumer import
// (e.g. mobile-attestation cohort merge uses block height as its
// cross-node coordination clock; see internal/mobileverify/coordinator.go).
func (bc *BlockChain) SetOnBlockCommitted(fn func(number uint64)) {
	bc.onBlockCommittedMu.Lock()
	bc.onBlockCommitted = fn
	bc.onBlockCommittedMu.Unlock()
}

// RememberSealedHeader makes a just-sealed header findable by GetHeader /
// GetHeaderByHash before its block is written: a speculative build chained
// on this node's own unwritten block (track 3c) needs the engine's Prepare
// to find the parent header -- its committee-evidence link (ParentBeaconRoot)
// and its time derive from it. Round 35zo: without it the chained header
// carried no link and every follower rejected the block. ForgetSealedHeader
// drops it again when the seal is discarded.
func (bc *BlockChain) RememberSealedHeader(h *block.Header) {
	if h == nil || bc.headerCache == nil {
		return
	}
	bc.headerCache.Add(h.Hash(), h)
}

func (bc *BlockChain) ForgetSealedHeader(hash types.Hash) {
	if bc.headerCache == nil {
		return
	}
	bc.headerCache.Remove(hash)
}

func (bc *BlockChain) notifyBlockCommitted(number uint64) {
	bc.onBlockCommittedMu.RLock()
	fn := bc.onBlockCommitted
	bc.onBlockCommittedMu.RUnlock()
	if fn != nil {
		fn(number)
	}
}

// =============================================================================
// Chain Queries (simple)
// =============================================================================

func (bc *BlockChain) LatestBlockCh() (block.IBlock, error) {
	select {
	case <-bc.ctx.Done():
		return nil, errors.New("the main chain is closed")
	case blk, ok := <-bc.latestBlockCh:
		if !ok {
			return nil, errors.New("the main chain is closed")
		}
		return blk, nil
	}
}
