// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
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

	// S32 (docs/QS_BLOCK_TIME_BUDGET.md 6di/6dj, N42_BLOCK_DECODE_REUSE_POOL):
	// a nil lookup (switch off, or no pool wired) makes decodeChunkedBlockReusePool
	// behave exactly like the plain decode; reused/decoded are then 0/0 and
	// carry no meaning (DecodeReuseStats' own zero-value caveat).
	var lookup block.TxLookup
	if BlockDecodeReusePoolOn() && s.cfg.txPool != nil {
		lookup = s.cfg.txPool.GetTx
	}
	// S31 (docs/QS_BLOCK_TIME_BUDGET.md 6dg/6dh): peek the header the instant
	// its own bytes are decoded, well before the (possibly 160k-transaction)
	// body below and before deferredCheck's own per-transaction walk. The
	// engine's HotStuff-2 extends-rule only ever needs the parent hash, a
	// header field -- this lets a two-phase Round 1 prepare vote fire on it
	// directly instead of waiting for the full deferred check. A peek
	// failure or a nil notifier is silently ignored; the ordinary decode and
	// deferredCheck below are completely unaffected either way.
	blk, _, _, err := ReadChunkedBlockPeekHeader(stream, s.cfg.p2p, func(h *block.Header) {
		if n := s.cfg.blockImportNotifier; n != nil && h.Number != nil {
			n.NotifyBlockHeaderKnown(h.Hash(), h.ParentHash, h.Number.Uint64())
		}
	}, lookup)
	if err != nil {
		log.Info("block push: read failed", "peer", stream.Conn().RemotePeer().String()[:12], "err", err)
		return
	}
	if s.rejectBadBlock(s.ctx, blk) {
		log.Debug("block push: dropping known bad branch", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12])
		return
	}
	s.observeCatchUpBlock(blk.Hash(), blk.Number64().Uint64())
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
	s.handlePushedBlock(blk, DeferredCheckConcurrentOn())
}

// handlePushedBlock runs the deferred-execution check and InsertChain on a
// freshly-decoded, not-yet-stored pushed block. Factored out of
// blockPushStreamHandler (S42) so this ordering logic is testable without a
// live network.Stream; concurrent is DeferredCheckConcurrentOn() read once
// by the caller and passed in explicitly (not read again here), so a test
// can drive both orderings without the env-backed switch's own sync.Once
// memoizing a single value for the whole test binary -- the same pattern
// S32's decodeChunkedBlockReusePool already established (lookup passed in,
// not read from BlockDecodeReusePoolOn() internally).
func (s *Service) handlePushedBlock(blk *block.Block, concurrent bool) {
	s.pushInflight.Store(blk.Hash(), struct{}{})
	defer s.pushInflight.Delete(blk.Hash())
	log.Info("block push: arrived", "number", blk.Number64().Uint64(), "txs", len(blk.Transactions()), "tMs", time.Now().UnixMilli())
	// S42 (docs/QS_BLOCK_TIME_BUDGET.md 6dx/6dy, N42_DEFERRED_CHECK_CONCURRENT):
	// today's order runs CheckDeferredBlock (228ms median WORK, 6dx) to
	// completion before InsertChain even starts. The check does not gate
	// InsertChain -- it never returns a plan, a sender list or anything else
	// InsertChain reads (PART 1); InsertChain's own executor independently
	// re-validates nonces/balances/gas via real EVM execution, so a block
	// that fails the check fails execution too (the one exception: blob
	// transactions, which the check rejects outright but the executor would
	// process normally -- see 6dy). With the switch on, InsertChain is
	// dispatched immediately and the check runs concurrently on its own
	// goroutine; deferredCheck already tolerates running on an arbitrary
	// goroutine (retryDeferredChildren already calls it from a
	// time.AfterFunc callback) and already avoids bc.lock/the pool lock
	// (its own reads use bc.qmdbRootComputer.LockReaders() and a fresh
	// read-tx). Whichever finishes first advances the vote gate through the
	// EXISTING notification paths -- deferredAttested's own callers already
	// accept importedBlocks OR deferredAttested, so "imported first" and
	// "checked first" are both already-handled orderings; no consensus-side
	// change is needed. A check failure after InsertChain has already
	// succeeded changes nothing: deferredCheck never votes on failure (it
	// only calls NotifyBlockChecked on success), so the only possible
	// effect of a losing, failing check is the log line below -- the
	// import's own outcome, and the vote already unlocked by
	// NotifyBlockImported, are both untouched.
	if concurrent {
		go s.deferredCheck(blk)
	} else {
		s.deferredCheck(blk)
	}
	// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): the instant this block is
	// handed to InsertChain -- the near side of whatever lock/queue wait
	// sits between here and insertChain's own per-block processing start
	// (SetInsertStartTMs, internal's insertChain loop); the gap between the
	// two IS that wait, named by having both points rather than a separate
	// duration field. insDispatchTMs (S42) is the same instant, kept
	// separately so it can be compared against chkStart/chkEnd without
	// relying on qTMs' own, earlier-established meaning.
	if contentionDiagEnabled {
		now := timeNowMs()
		blk.SetQueueTMs(now)
		blk.SetInsDispatchTMs(now)
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
	log.Info("block push: received", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "tMs", time.Now().UnixMilli())
	// Notify the HotStuff engine that this block is imported so a deferred
	// import-gated prepare vote can be cast. The gossip path does this in
	// subscriber_blocks.go; the direct-push path must do it too, otherwise a
	// pushed block never triggers EventBlockImported and the round stalls.
	// A nil insert error is not proof of execution (future-queued blocks
	// return nil too) — require applied-state evidence.
	if n := s.cfg.blockImportNotifier; n != nil && s.blockApplied(blk.Hash(), blk.Number64().Uint64()) {
		n.NotifyBlockImported(blk.Hash(), blk.TxHash())
	}
	s.retryDeferredChildren(blk.Hash())
}

// deferredCheck runs the deferred-execution vote check on an arrived block
// and tells the consensus layer when it passes; a block whose parent is not
// applied yet is kept and checked again when the parent lands.
func (s *Service) deferredCheck(blk block.IBlock) {
	checker, ok := s.cfg.chain.(DeferredBlockChecker)
	if !ok || s.cfg.blockImportNotifier == nil {
		return
	}
	// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): brackets CheckDeferredBlock's
	// own call. A block whose parent is not yet applied re-enters this
	// function via retryDeferredChildren; the LATEST attempt's stamps are
	// what SetCheckStamps keeps, matching "the deferred check that actually
	// ran" rather than the first, retried attempt.
	var tChkStart int64
	if contentionDiagEnabled {
		tChkStart = timeNowMs()
	}
	checked, retry, err := checker.CheckDeferredBlock(blk)
	if contentionDiagEnabled {
		if b, ok := blk.(interface{ SetCheckStamps(int64, int64) }); ok {
			b.SetCheckStamps(tChkStart, timeNowMs())
		}
	}
	if !checked {
		return
	}
	if err == nil {
		log.Info("deferred check: block passes, vote may proceed before its import", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "tMs", time.Now().UnixMilli())
		s.cfg.blockImportNotifier.NotifyBlockChecked(blk.Hash(), blk.ParentHash())
		return
	}
	if retry {
		parent := blk.ParentHash()
		s.deferredMu.Lock()
		if s.deferredPending == nil {
			s.deferredPending = make(map[types.Hash][]block.IBlock)
			s.deferredAttempts = make(map[types.Hash]int)
		}
		s.deferredAttempts[blk.Hash()]++
		if s.deferredAttempts[blk.Hash()] > deferredMaxAttempts || len(s.deferredPending) > deferredMaxPendingParents {
			// Give up: the old import-gated vote still applies once the
			// block imports; nothing is lost but the pipeline's head start.
			delete(s.deferredAttempts, blk.Hash())
			s.deferredMu.Unlock()
			log.Debug("deferred check: giving up the pre-import vote for this block", "number", blk.Number64().Uint64(), "err", err)
			return
		}
		s.deferredPending[parent] = append(s.deferredPending[parent], blk)
		s.deferredMu.Unlock()
		log.Debug("deferred check: waiting for the parent to apply", "number", blk.Number64().Uint64(), "err", err)
		// The parent may have applied between the check and the enqueue,
		// or may land through a path that does not call
		// retryDeferredChildren (a fetch, the future queue, the miner's own
		// write): poll it back on a short timer.
		time.AfterFunc(deferredRetryInterval, func() { s.retryDeferredChildren(parent) })
		return
	}
	log.Warn("deferred check FAILED: not voting for this block", "number", blk.Number64().Uint64(), "hash", blk.Hash().Hex()[:12], "err", err)
}

// retryDeferredChildren re-runs the deferred check of the blocks waiting on
// parent, after parent was imported.
func (s *Service) retryDeferredChildren(parent types.Hash) {
	s.deferredMu.Lock()
	children := s.deferredPending[parent]
	delete(s.deferredPending, parent)
	s.deferredMu.Unlock()
	for _, c := range children {
		s.deferredCheck(c)
	}
}

const (
	deferredRetryInterval     = 200 * time.Millisecond
	deferredMaxAttempts       = 60 // ~12 s of polling a parent that never applies here
	deferredMaxPendingParents = 64
)
