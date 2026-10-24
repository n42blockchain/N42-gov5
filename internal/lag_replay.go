// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// lagReplayMaxDepth bounds how many stored, unapplied ancestors the lag
// executor replays for one queued block. It stays well inside the QMDB undo
// window; a deeper gap is catch-up's job.
const lagReplayMaxDepth = 64

// Stored is not applied (round S132). The startup revert
// (revertSpeculativeOnStartup) unwinds the applied state of every
// uncommitted block but keeps its header and body, and consensus may still
// extend such a block: it carries a QC, so the TC after the restart names it
// as the highest QC and the next leader builds on it. Before this, nothing
// executed it again: the lag executor saw its child's parent "stored" and
// waited for it to become the applied head, the authorized import of the
// child could not execute on a parent whose state is gone, and a stored
// block re-queued was dropped as "written by another path". The whole fleet
// then committed children it could never import and stopped.

// lagAppliedHead is the applied state's head: the in-flight write's block
// (applied in memory) or the durable marker.
func (bc *BlockChain) lagAppliedHead(tx kv.Tx) (uint64, types.Hash, bool) {
	if j := bc.lagWrite.visible.Load(); j != nil {
		return j.number, j.hash, true
	}
	n, h, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil || !ok {
		return 0, types.Hash{}, false
	}
	return n, types.Hash(h), true
}

// lagStoredApplied reports whether a block this node has STORED is also
// APPLIED. Without QMDB stored means executed (the pre-QMDB rule).
func (bc *BlockChain) lagStoredApplied(hash types.Hash, number uint64) bool {
	if !bc.qmdbEnabled {
		return true
	}
	return bc.lagAppliedIs(hash, number) || bc.HasAppliedBlock(hash, number)
}

// lagStoredUnappliedAncestors returns, lowest first, the stored blocks
// between the applied head and b's parent (inclusive) when b's parent
// descends from the applied head through stored but unapplied blocks only.
// It returns nil when the parent is applied, is not stored, sits on another
// branch than the applied head (a sibling switch, which the authorized
// import owns), or is more than lagReplayMaxDepth above it.
func (bc *BlockChain) lagStoredUnappliedAncestors(b *block.Block) []*block.Header {
	if !bc.qmdbEnabled || bc.ChainDB == nil || b.Number64().Uint64() == 0 {
		return nil
	}
	var chain []*block.Header
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		appliedNum, appliedHash, ok := bc.lagAppliedHead(tx)
		if !ok {
			return nil
		}
		hash, num := b.ParentHash(), b.Number64().Uint64()-1
		var walked []*block.Header
		for i := 0; i <= lagReplayMaxDepth; i++ {
			if num <= appliedNum {
				if num == appliedNum && hash == appliedHash && len(walked) > 0 {
					chain = walked
				}
				return nil
			}
			hdr := rawdb.ReadHeader(tx, hash, num)
			if hdr == nil {
				return nil
			}
			walked = append(walked, hdr)
			hash, num = hdr.ParentHash, num-1
		}
		return nil
	})
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// lagReplayStoredAncestors executes, in parent order, the stored but
// unapplied blocks under b's parent (lagStoredUnappliedAncestors), with
// branch-switch authority so the import does not skip them as known. It
// reports whether it applied at least one; it stops at the first block it
// cannot load or apply.
func (bc *BlockChain) lagReplayStoredAncestors(b *block.Block) bool {
	chain := bc.lagStoredUnappliedAncestors(b)
	if len(chain) == 0 {
		return false
	}
	log.Warn("lag executor: replaying stored blocks that are not applied (reverted at startup or never executed) under a queued block",
		"queued", b.Number64().Uint64(), "queuedHash", b.Hash().Hex()[:12],
		"from", chain[0].Number.Uint64(), "to", chain[len(chain)-1].Number.Uint64())
	applied := 0
	for _, hdr := range chain {
		h, n := hdr.Hash(), hdr.Number.Uint64()
		ib := bc.GetBlock(h, n)
		blk, ok := ib.(*block.Block)
		if ib == nil || !ok {
			log.Warn("lag executor: stored ancestor has no body here, cannot replay it", "number", n, "hash", h.Hex()[:12])
			break
		}
		handed, err := bc.insertChainLag(blk, true)
		if !handed && !bc.lagStoredApplied(h, n) {
			log.Warn("lag executor: stored ancestor replay did not apply", "number", n, "hash", h.Hex()[:12], "err", err)
			break
		}
		applied++
	}
	return applied > 0
}

// HeaderKnownHere reports whether this node holds hash's header at number:
// stored, or held as an available (not yet executed) block. The block-push
// path asks it before letting a header alone release a prepare vote: a
// block whose parent this node does not hold cannot be executed here, and a
// quorum of such votes commits blocks nobody can import.
func (bc *BlockChain) HeaderKnownHere(hash types.Hash, number uint64) bool {
	if bc.ChainDB == nil {
		return false
	}
	known := false
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		known = bc.headerAnywhere(tx, hash, number) != nil
		return nil
	})
	return known
}
