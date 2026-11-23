// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Branch-switch authority on the lag executor (sync review S0b / C3, the
// stage A rule applied to the performance line).
//
// The lag executor imports the blocks the availability check queued, which
// any peer can feed (direct push, gossip). A queued block whose parent is
// stored on a branch that is not applied used to be imported WITH switch
// authority after a short grace, so a pushed child of a stored sibling
// could unwind the applied speculative head: the same escalation the
// future queue had. Authority is now granted only to a block consensus
// named: the target of a verified proposal or CommitQC (MarkConsensusNamed,
// fed by the HotStuff service through the sync layer), or a block an
// authorized import future-queued (future_authority.go). Any other queued
// block waits for its parent to become the applied head, which a
// consensus-named block or the range catch-up brings about.
// N42_SYNC_LEGACY_FUTURE_AUTHORITY=1 restores the old escalation.

package internal

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/synclegacy"
)

// MarkConsensusNamed records that a verified proposal or CommitQC named
// hash: the lag executor may import it with branch-switch authority.
func (bc *BlockChain) MarkConsensusNamed(hash types.Hash) {
	if hash == (types.Hash{}) {
		return
	}
	// Bounded like the future-queue set: entries whose block is neither
	// queued nor future-queued are dropped on overflow.
	bc.consensusNamed.mark(hash, func(h types.Hash) bool {
		return bc.futureBlocks.Contains(h) || bc.LagImportQueued(h)
	})
}

// lagSwitchAuthorized reports whether the lag executor may import hash
// with branch-switch authority onto a parent that is stored but not
// applied.
func (bc *BlockChain) lagSwitchAuthorized(hash types.Hash) bool {
	if synclegacy.FutureAuthority.On() {
		return true
	}
	return bc.consensusNamed.has(hash) || bc.futureAuth.has(hash)
}
