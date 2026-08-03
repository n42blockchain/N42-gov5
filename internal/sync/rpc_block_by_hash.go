// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// blockByHashStreamHandler serves a single block requested by its 32-byte hash.
// It is the server side of fetch-on-miss: a follower that received a HotStuff
// Proposal but not the referenced block (its direct push was dropped) asks a
// peer for the block here so it can import and cast its import-gated vote.
func (s *Service) blockByHashStreamHandler(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	var hb [32]byte
	if _, err := io.ReadFull(stream, hb[:]); err != nil {
		return
	}
	hash := types.BytesToHash(hb[:])
	blk, err := s.cfg.chain.GetBlockByHash(hash)
	if err != nil || blk == nil {
		log.Debug("block by hash: not found locally", "hash", hash.Hex()[:12])
		// Say so explicitly instead of silently closing: a bare close makes the
		// requester read a naked EOF ("failed to read status code from first
		// chunk"), indistinguishable from a real transport fault — and since
		// the fetch fans out to every peer while typically only the leader has
		// the block, the log drowned in phantom EOF errors.
		writeErrorResponseToStream(responseCodeServerError, "block not found", stream, s.cfg.p2p)
		return
	}
	// Debug, not Info: this fires once PER PEER PER FAN-OUT of a single
	// fetch-on-miss, so at Info a single wedged requester re-fetching in a
	// loop turned every healthy peer's run.log into a sustained multi-MB/s
	// write stream (observed live: 7/7 nodes' logs ballooning in lockstep).
	log.Debug("block by hash: serving", "reqHash", hash.Hex()[:12], "blkHash", blk.Hash().Hex()[:12], "number", blk.Number64().Uint64())
	if err := WriteBlockChunk(stream, s.cfg.chain, blk); err != nil {
		log.Debug("block by hash: write failed", "hash", hash.Hex()[:12], "err", err)
	}
}

// FetchBlockByHash requests a block by hash from all connected peers and imports
// the first valid response, then notifies the consensus engine. This is the
// client side of fetch-on-miss, invoked when the engine asks to execute a
// proposed block we don't yet have. Implements hotstuff.BlockFetcher.
func (s *Service) FetchBlockByHash(hash types.Hash) {
	if s.hasBadBlock(hash) {
		log.Debug("fetch-on-miss: dropping known bad block", "hash", hash.Hex()[:12])
		return
	}
	// Already have the block BODY: don't just return — the caller needs it
	// APPLIED. On a branch switch the whole ancestor sub-branch may already be
	// in the DB from earlier rounds but never executed onto the world state.
	// Align SYNCHRONOUSLY along the parent chain in ONE pass: walk back until
	// a block imports (its parent is applied — the unwind-on-switch import
	// path reverts the sibling branch), then replay descendants in order.
	// Doing this asynchronously per block (the previous behavior) let several
	// stale same-height candidates' retries tug the applied head between
	// sibling branches every 2s, so the CURRENT proposal's parent never
	// stayed aligned long enough for its import to succeed.
	// Per-hash rate limit, covering BOTH paths below: the engine re-requests
	// a missing proposal on every event that touches it, several times a
	// second. The remote path fans out to EVERY connected peer — a node
	// wedged on one unimportable block hammered the same hashes 1.5M times in
	// a few hours, network spam plus a serve-log line on all six peers per
	// copy. The local path spawns an alignAndImport per call — the same wedge
	// re-ran the discontinuity realign (an MDBX-write + log line each time)
	// in a tight loop for hours. One attempt per fetchMissInterval per hash
	// keeps liveness (retries still land every interval) and caps both.
	s.fetchMissLock.Lock()
	if last, ok := s.fetchMissCache.Get(hash); ok && time.Since(last) < fetchMissInterval {
		s.fetchMissLock.Unlock()
		return
	}
	s.fetchMissCache.Add(hash, time.Now())
	s.fetchMissLock.Unlock()
	if blk, _ := s.cfg.chain.GetBlockByHash(hash); blk != nil {
		s.observeCatchUpBlock(hash, blk.Number64().Uint64())
		go s.alignAndImport(blk)
		return
	}
	protoID := protocol.ID(p2p.RPCBlockByHashTopicV1 + s.cfg.p2p.Encoding().ProtocolSuffix())
	log.Info("fetch-on-miss: requesting block", "hash", hash.Hex()[:12], "peers", len(s.cfg.p2p.Peers().Connected()))
	for _, pid := range s.cfg.p2p.Peers().Connected() {
		go func(pid peer.ID) {
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			defer cancel()
			stream, err := s.cfg.p2p.Host().NewStream(ctx, pid, protoID)
			if err != nil {
				return
			}
			defer func() { _ = stream.Close() }()
			if _, err := stream.Write(hash[:]); err != nil {
				return
			}
			// Do NOT CloseWrite: the handler reads exactly 32 bytes (io.ReadFull)
			// then writes the block back on the same stream. Closing the write half
			// here tears the stream down before the response can be read (observed
			// as "read status code: EOF").
			blk, err := ReadChunkedBlock(stream, s.cfg.p2p, true)
			if err != nil {
				// Debug: with the fan-out, most peers legitimately answer
				// "block not found" — only aggregate failure is interesting.
				log.Debug("fetch-on-miss: read failed", "hash", hash.Hex()[:12], "err", err)
				return
			}
			if blk.Hash() != hash {
				log.Info("fetch-on-miss: hash mismatch", "want", hash.Hex()[:12], "got", blk.Hash().Hex()[:12])
				return // peer returned the wrong block
			}
			s.observeCatchUpBlock(hash, blk.Number64().Uint64())
			// A one-shot insert is not enough: the proposal block's parent may
			// be a QC branch this node never applied. Chain-align exactly like
			// the local-hit path (walk back, switch with authority, replay).
			s.alignAndImport(blk)
		}(pid)
	}
}

// insertAuthorized imports consensus-requested blocks WITH branch-switch
// authority (the engine named this block/branch via a proposal, so reverting
// a losing applied sibling is legitimate). Falls back to the plain insert if
// the chain implementation doesn't support authorization.
func (s *Service) insertAuthorized(blocks []block.IBlock) (int, error) {
	if a, ok := s.cfg.chain.(interface {
		InsertChainAuthorized([]block.IBlock) (int, error)
	}); ok {
		return a.InsertChainAuthorized(blocks)
	}
	return s.cfg.chain.InsertChain(blocks)
}

// blockApplied reports whether the chain has EXECUTED the block onto the
// world state (it is on the applied lineage) — the evidence level a consensus
// vote needs. Body presence and a nil InsertChain error are NOT that
// evidence: a future-queued block returns nil from InsertChain, and a stored-
// but-never-executed block satisfies HasBlock (observed live: six validators
// voted a forked leader's inexecutable block into a CommitQC on exactly that
// confusion). Falls back to body presence for chain implementations without
// the applied-lineage query.
func (s *Service) blockApplied(hash types.Hash, number uint64) bool {
	if a, ok := s.cfg.chain.(interface {
		HasAppliedBlock(types.Hash, uint64) bool
	}); ok {
		return a.HasAppliedBlock(hash, number)
	}
	return s.cfg.chain.HasBlock(hash, number)
}

// BlockApplied exposes the applied-state probe to the consensus layer
// (hotstuff asserts for it on its BlockFetcher): the import-gated vote must
// be certified by executed state, not stored bytes.
func (s *Service) BlockApplied(hash types.Hash, number uint64) bool {
	return s.blockApplied(hash, number)
}

// alignAndImport imports a consensus-requested block, aligning the applied
// branch SYNCHRONOUSLY along its parent chain in one pass: walk back through
// locally stored ancestors until one imports (its parent is applied — the
// authorized unwind reverts the losing sibling branch), then replay the
// descendants oldest-first, and finally notify the engine so the deferred
// import-gated vote fires. One pass, no async tug-of-war between candidates.
// Every "imported" decision below is judged by applied-state evidence, never
// by a nil insert error alone (future-queued blocks return nil too).
func (s *Service) alignAndImport(blk block.IBlock) {
	if blk == nil || s.rejectBadBlock(s.ctx, blk) {
		return
	}
	// Already applied: the walk-back + authorized replay is unnecessary (and
	// would re-enter the reorg path for a non-canonical sibling). Just notify
	// the engine. A block that is merely STORED falls through to the
	// walk-back — it still needs its ancestors executed.
	if s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		if n := s.cfg.blockImportNotifier; n != nil {
			n.NotifyBlockImported(blk.Hash(), blk.TxHash())
		}
		return
	}
	cur := blk
	pending := []block.IBlock{}
	for depth := 0; depth < 32; depth++ {
		if s.rejectBadBlock(s.ctx, cur) {
			return
		}
		if _, err := s.insertAuthorized([]block.IBlock{cur}); err == nil &&
			s.blockApplied(cur.Hash(), cur.Number64().Uint64()) {
			break
		} else if errors.Is(err, consensus.ErrExecutionInvalid) {
			s.setBadBlock(s.ctx, cur.Hash())
			return
		}
		p, _ := s.cfg.chain.GetBlockByHash(cur.ParentHash())
		if p == nil {
			// Genuinely missing ancestor: fetch it remotely; the current
			// blocks stay future-queued until it arrives and aligns.
			s.FetchBlockByHash(cur.ParentHash())
			return
		}
		pending = append(pending, cur)
		cur = p
	}
	for i := len(pending) - 1; i >= 0; i-- {
		if _, err := s.insertAuthorized([]block.IBlock{pending[i]}); err != nil {
			if errors.Is(err, consensus.ErrExecutionInvalid) {
				s.setBadBlock(s.ctx, pending[i].Hash())
			}
			log.Debug("fetch-on-miss: chain-align replay failed",
				"number", pending[i].Number64().Uint64(),
				"hash", pending[i].Hash().Hex()[:12], "err", err)
			return
		}
	}
	// The replay may have future-queued (rather than executed) the target —
	// e.g. its branch is inexecutable on this node's state. No evidence, no
	// notification: a vote must never certify a block this node hasn't run.
	if !s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		log.Debug("fetch-on-miss: chain-align left the block unapplied; not notifying",
			"number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
		return
	}
	log.Info("fetch-on-miss: aligned and imported", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "walkback", len(pending))
	if n := s.cfg.blockImportNotifier; n != nil {
		n.NotifyBlockImported(blk.Hash(), blk.TxHash())
	}
}
