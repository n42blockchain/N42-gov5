// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"errors"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/log"
)

// blockPushStreamHandler receives a block pushed directly by a HotStuff leader
// and imports it, so a follower can vote on / finalize the matching hash-only
// Proposal. This is the reliable alternative to single-publisher block gossip:
// when only the current leader publishes the block topic, the gossipsub mesh
// does not form and followers never receive the block. The leader therefore
// opens a direct stream to each peer and writes the block here (see
// p2p.RPCBlockPushTopicV1 and BlockChain.DirectPushBlock).
func (s *Service) blockPushStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	blk, err := ReadChunkedBlock(stream, s.cfg.p2p, true)
	if err != nil {
		log.Info("block push: read failed", "peer", stream.Conn().RemotePeer().String()[:12], "err", err)
		return
	}
	if s.rejectBadBlock(s.ctx, blk) {
		log.Debug("block push: dropping known bad branch", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
		return
	}
	s.promoteCommittedTarget(blk.Hash(), blk.Number64().Uint64())
	// Already stored (a re-pushed uncommitted leader block): skip re-entering
	// InsertChain's reorg path. Notify the engine only when the block is
	// actually APPLIED — a stored-but-never-executed body must not unlock an
	// import-gated vote (alignment is driven by the engine's per-proposal
	// fetch-on-miss, not by every re-push, to avoid unwind storms).
	if s.cfg.chain.HasBlock(blk.Hash(), blk.Number64().Uint64()) {
		if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
			n.NotifyBlockImported(blk.Hash(), blk.TxHash())
		}
		return
	}
	if _, err := s.cfg.chain.InsertChain([]block.IBlock{blk}); err != nil {
		if isAncestorError(err) {
			// Missing parent (e.g. a committed same-height sibling this node
			// never received): queue the child and actively fetch the parent
			// by hash — mirroring the gossip path — instead of dropping it.
			log.Debug("block push: parent missing, queuing + fetching parent",
				"number", blk.Number64().Uint64(), "parent", blk.ParentHash().Hex()[:12])
			_ = s.cfg.chain.AddFutureBlock(blk)
			s.FetchBlockByHash(blk.ParentHash())
			return
		}
		if errors.Is(err, consensus.ErrExecutionInvalid) {
			s.setBadBlock(s.ctx, blk.Hash())
		}
		log.Info("block push: insert failed", "number", blk.Number64().Uint64(), "err", err)
		return
	}
	log.Info("block push: received", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
	// Notify the HotStuff engine that this block is imported so a deferred
	// import-gated prepare vote can be cast. The gossip path does this in
	// subscriber_blocks.go; the direct-push path must do it too, otherwise a
	// pushed block never triggers EventBlockImported and the round stalls.
	// A nil insert error is not proof of execution (future-queued blocks
	// return nil too) — require applied-state evidence.
	if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		n.NotifyBlockImported(blk.Hash(), blk.TxHash())
	}
}
