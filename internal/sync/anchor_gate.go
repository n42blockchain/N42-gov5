// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S1 anchor gate of the range catch-up (sync review C1/C5/C15, sync v2
// design §2, N42 sync standard §3.3-§3.4): a fetched range is executed only
// up to the highest block hash-linked to a verified commit-domain QC.
//
//   - Screen: numbers contiguous and parent hashes linked through the range
//     (a break is a provable fault of the serving peer).
//   - Harvest anchors top-down: decode each header QC above the highest block
//     already authenticated; verify it only when it names a block of the
//     chain being screened or of the link skeleton. Authentication is
//     downward-closed, so the first verified QC from the top usually settles
//     the whole range with a single BLS verification.
//   - Anchors also come from the consensus path: every CommitQC the engine
//     commits (NoteCommitAnchor) and the hash targets of CatchUpToHash.
//
// N42_SYNC_ANCHOR selects the mode (syncanchor): observe (default) leaves
// the import unchanged and only logs and counts; enforce executes the
// anchored prefix, holds the rest as a bounded tail and fetches further when
// a stretch carries no QC; off skips all of this.
//
// Scope (owner decision QB): only the sync importer. The live consensus path
// (push, gossip, fetch-on-miss of the current proposal) is not gated.

package sync

import (
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/log"
)

// maxAnchorLookahead bounds the extra range requests one catch-up round may
// issue to find the next embedded QC above an unanchored stretch, or to
// re-fetch bodies the tail bound evicted once their hashes were anchored.
const maxAnchorLookahead = 8

// anchorWarnInterval rate-limits the per-range WARN of the gate.
const anchorWarnInterval = 10 * time.Second

var anchorLastWarn atomic.Int64

func anchorWarn(msg string, ctx ...interface{}) {
	now := time.Now().UnixNano()
	last := anchorLastWarn.Load()
	if now-last < int64(anchorWarnInterval) || !anchorLastWarn.CompareAndSwap(last, now) {
		log.Debug(msg, ctx...)
		return
	}
	log.Warn(msg, ctx...)
}

// unknownEpochWarnInterval rate-limits the unknown-epoch WARN per epoch. Each
// screened range re-reports the epoch, so without it a node waiting for a set
// logs one line per range. Only the log is limited: verdicts are never cached
// here, so a set that becomes known later is used on the next screen.
const unknownEpochWarnInterval = 30 * time.Second

// maxUnknownEpochWarnKeys bounds the per-epoch warn bookkeeping.
const maxUnknownEpochWarnKeys = 64

var unknownEpochWarn struct {
	mu   gosync.Mutex
	last map[uint64]time.Time
}

// shouldWarnUnknownEpoch reports whether the unknown-epoch WARN for epoch is
// due at now, and records it when it is.
func shouldWarnUnknownEpoch(epoch uint64, now time.Time) bool {
	unknownEpochWarn.mu.Lock()
	defer unknownEpochWarn.mu.Unlock()
	if last, ok := unknownEpochWarn.last[epoch]; ok && now.Sub(last) < unknownEpochWarnInterval {
		return false
	}
	if unknownEpochWarn.last == nil {
		unknownEpochWarn.last = make(map[uint64]time.Time)
	}
	if len(unknownEpochWarn.last) >= maxUnknownEpochWarnKeys {
		for e, t := range unknownEpochWarn.last {
			if now.Sub(t) >= unknownEpochWarnInterval {
				delete(unknownEpochWarn.last, e)
			}
		}
		if len(unknownEpochWarn.last) >= maxUnknownEpochWarnKeys {
			return false
		}
	}
	unknownEpochWarn.last[epoch] = now
	return true
}

// anchorVerifier returns the consensus-side header-QC verifier, or nil when
// this is not a HotStuff chain (the gate then stays inactive).
func (s *Service) anchorVerifier() syncanchor.Verifier {
	if v, ok := s.cfg.blockImportNotifier.(syncanchor.Verifier); ok {
		return v
	}
	return nil
}

// NoteCommitAnchor records a block hash named by a CommitQC the consensus
// layer verified (a Decide / OutputBlockCommitted). In enforce mode a held
// tail that it authenticates is released through the catch-up worker.
func (s *Service) NoteCommitAnchor(hash types.Hash, number uint64) {
	if PipelineOn() {
		// S5: every Decide is a commit sample for the sync mode machine.
		if number > 0 && hash != (types.Hash{}) {
			s.noteVerifiedCommitTarget(number)
		}
		s.observeSyncMode(true)
	}
	mode := syncanchor.Current()
	if mode == syncanchor.Off || hash == (types.Hash{}) {
		return
	}
	s.anchors.noteAnchor(hash)
	if mode != syncanchor.Enforce {
		return
	}
	if top, ok := s.anchors.tailReleasable(); ok {
		if number < top {
			number = top
		}
		go s.enqueueCatchUp(number)
	}
}

type anchorCandidate struct {
	blk   block.IBlock
	index int  // position in the screened chain; -1 for a links-only block
	own   bool // served by the peer under screening
}

// anchorScreenResult is the gate's verdict on one fetched range.
type anchorScreenResult struct {
	active        bool           // false: no verifier, the range passes untouched
	exec          []block.IBlock // blocks to insert now
	fault         string         // a provable fault of the serving peer (enforce drops the range)
	anchored      int            // blocks of the screened chain that are authenticated
	unanchored    int            // blocks above the highest anchor
	evicted       int            // tail blocks dropped by the bound (enforce)
	verifications int            // BLS verifications run (cache misses)
	took          time.Duration
}

// anchorScreen applies the gate to blocks (non-nil, ascending as served).
func (s *Service) anchorScreen(pid peer.ID, blocks []block.IBlock, mode syncanchor.Mode) anchorScreenResult {
	res := anchorScreenResult{exec: blocks}
	v := s.anchorVerifier()
	if v == nil || len(blocks) == 0 || mode == syncanchor.Off {
		return res
	}
	t0 := time.Now()
	res.active = true
	enforce := mode == syncanchor.Enforce

	// 1. Contiguity and parent links inside the response.
	linked := blocks
	for i := 1; i < len(blocks); i++ {
		if blocks[i].Number64().Uint64() != blocks[i-1].Number64().Uint64()+1 ||
			blocks[i].ParentHash() != blocks[i-1].Hash() {
			countAnchorRefusal(anchorReasonBrokenLink, 1)
			res.fault = anchorReasonBrokenLink
			linked = blocks[:i]
			break
		}
	}
	if res.fault != "" && enforce {
		res.exec = nil
		res.took = time.Since(t0)
		return res
	}

	// 2. Join with the held tail (enforce) and record the link skeleton.
	b := &s.anchors
	b.mu.Lock()
	chain := linked
	own := 0
	var linksOnly []block.IBlock
	if enforce && len(b.tail) > 0 {
		top := b.tail[len(b.tail)-1]
		topNum := top.Number64().Uint64()
		first := linked[0]
		firstNum := first.Number64().Uint64()
		switch {
		case firstNum == topNum+1 && first.ParentHash() == top.Hash():
			chain = append(append(make([]block.IBlock, 0, len(b.tail)+len(linked)), b.tail...), linked...)
			own = len(b.tail)
		case firstNum > topNum:
			// A look-ahead range above the tail (past evicted blocks, or a
			// branch that does not extend it): it contributes links and QCs
			// only; its bodies cannot execute yet and it never displaces the
			// held tail.
			chain = append([]block.IBlock(nil), b.tail...)
			own = len(chain)
			linksOnly = linked
		}
		// Otherwise the range starts inside or below the tail (a re-fetch
		// from the local head) and supersedes it.
	}
	linkedTop := linked[len(linked)-1].Number64().Uint64()
	if len(linksOnly) > 0 {
		b.recordLinksLocked(linksOnly)
		if linkedTop > b.skelTop {
			b.skelTop = linkedTop
		}
	} else {
		b.skelTop = linkedTop
	}
	b.recordLinksLocked(chain)
	anchoredTop := b.highestAuthLocked(chain)
	b.mu.Unlock()

	// 3. Harvest anchors top-down from headers above the highest anchor.
	pos := make(map[types.Hash]int, len(chain)-anchoredTop)
	for i := anchoredTop + 1; i < len(chain); i++ {
		pos[chain[i].Hash()] = i
	}
	candidates := make([]anchorCandidate, 0, len(linksOnly)+len(chain)-anchoredTop)
	for i := len(linksOnly) - 1; i >= 0; i-- {
		candidates = append(candidates, anchorCandidate{blk: linksOnly[i], index: -1, own: true})
	}
	for i := len(chain) - 1; i > anchoredTop; i-- {
		candidates = append(candidates, anchorCandidate{blk: chain[i], index: i, own: i >= own})
	}
	unknownEpoch := uint64(0)
	sawUnknownEpoch := false
	for _, c := range candidates {
		if anchoredTop >= len(chain)-1 || (c.index >= 0 && c.index <= anchoredTop) {
			break
		}
		h, ok := c.blk.Header().(*block.Header)
		if !ok {
			continue
		}
		r := v.DecodeHeaderQC(h.Extra)
		switch r.Verdict {
		case syncanchor.BadExtra:
			countAnchorRefusal(anchorReasonBadExtra, 1)
			if c.own && res.fault == "" {
				res.fault = anchorReasonBadExtra
			}
			continue
		case syncanchor.Unverified:
		default:
			continue // no QC, or a pre-consensus header
		}
		if j, inChain := pos[r.BlockHash]; inChain && j <= anchoredTop {
			continue // names a block already authenticated
		} else if !inChain && !b.hasUnauthLink(r.BlockHash) {
			continue // names nothing this screen can use
		}
		vr := v.VerifyHeaderQC(h.Extra)
		if !vr.Cached {
			res.verifications++
			syncAnchorQCVerifications.Inc()
		}
		switch vr.Verdict {
		case syncanchor.Verified:
			b.noteAnchor(r.BlockHash)
			b.mu.Lock()
			anchoredTop = b.highestAuthLocked(chain)
			b.mu.Unlock()
		case syncanchor.PrepareDomain:
			countAnchorRefusal(anchorReasonPrepareDomain, 1)
		case syncanchor.BadQC:
			countAnchorRefusal(anchorReasonBadQC, 1)
			if c.own && res.fault == "" {
				res.fault = anchorReasonBadQC
			}
		case syncanchor.UnknownEpoch:
			countAnchorRefusal(anchorReasonUnknownEpoch, 1)
			sawUnknownEpoch, unknownEpoch = true, vr.Epoch
		case syncanchor.NoVerifier:
			res.active = false
			res.took = time.Since(t0)
			return res
		}
		if enforce && res.fault != "" {
			break
		}
	}
	if sawUnknownEpoch && !shouldWarnUnknownEpoch(unknownEpoch, time.Now()) {
		log.Debug("sync anchor: validator set for a QC epoch is unknown", "epoch", unknownEpoch)
	} else if sawUnknownEpoch {
		log.Warn("sync anchor: validator set for a QC epoch is unknown; blocks above the last anchor stay unexecuted until it is provided (epoch_schedule.json)",
			"epoch", unknownEpoch)
	}

	res.anchored = anchoredTop + 1
	res.unanchored = len(chain) - res.anchored + len(linksOnly)
	syncAnchorOK.Add(float64(res.anchored))
	countAnchorRefusal(anchorReasonUnanchored, res.unanchored)
	res.took = time.Since(t0)

	first, last := blocks[0].Number64().Uint64(), blocks[len(blocks)-1].Number64().Uint64()
	if res.unanchored > 0 || res.fault != "" {
		anchoredTo := uint64(0)
		if anchoredTop >= 0 {
			anchoredTo = chain[anchoredTop].Number64().Uint64()
		}
		anchorWarn("sync anchor: catch-up range not fully anchored",
			"mode", mode.String(), "peer", shortPeer(pid), "from", first, "to", last,
			"anchoredTo", anchoredTo, "unanchored", res.unanchored, "fault", res.fault,
			"qcVerifications", res.verifications, "took", res.took)
	}

	if !enforce {
		return res // observe: the import proceeds exactly as before
	}
	if res.fault != "" {
		res.exec = nil
		return res
	}
	res.exec = chain[:anchoredTop+1]
	b.mu.Lock()
	res.evicted = b.setTailLocked(chain[anchoredTop+1:])
	b.mu.Unlock()
	if res.evicted > 0 {
		syncAnchorTailEvicted.Add(float64(res.evicted))
	}
	return res
}

func shortPeer(pid peer.ID) string {
	s := pid.String()
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// releaseAnchoredTail executes the authenticated prefix of the held tail
// (enforce). It runs on the catch-up worker, before any new fetch.
func (s *Service) releaseAnchoredTail(authorized bool) int {
	if syncanchor.Current() != syncanchor.Enforce {
		return 0
	}
	released := s.anchors.releaseAuthenticatedTail()
	if len(released) == 0 {
		return 0
	}
	_, imported, err := s.insertCatchUpBlocks(s.ctx, released, authorized)
	syncAnchorOK.Add(float64(imported))
	if err != nil {
		log.Warn("sync anchor: releasing the anchored tail failed", "err", err,
			"from", released[0].Number64().Uint64(), "count", len(released), "imported", imported)
	} else {
		log.Info("sync anchor: released anchored tail",
			"from", released[0].Number64().Uint64(), "to", released[len(released)-1].Number64().Uint64())
	}
	return imported
}

// appliedAuthedTop returns the highest height h >= self such that every
// height in self+1..h holds an authenticated skeleton block the chain has
// already executed. The canonical head trails the executed blocks (the
// canonical row moves only with a CommitQC, which for the newest blocks sits
// in the held tail), so catch-up measures its progress from this frontier,
// not from the canonical head.
func (s *Service) appliedAuthedTop(self uint64) uint64 {
	top, _ := s.appliedAuthedFrontier(self, types.Hash{})
	return top
}

// appliedAuthedFrontier is appliedAuthedTop that also returns the hash of the
// frontier block (selfHash when the frontier is self).
func (s *Service) appliedAuthedFrontier(self uint64, selfHash types.Hash) (uint64, types.Hash) {
	byNum := s.anchors.authedLinksAbove(self, maxRequestBlocks)
	top, topHash := self, selfHash
	for n := self + 1; ; n++ {
		applied := false
		for _, h := range byNum[n] {
			if s.blockApplied(h, n) {
				applied, topHash = true, h
				break
			}
		}
		if !applied {
			return top, topHash
		}
		top = n
	}
}

// anchorNextStart tells an enforcing catch-up round where to fetch next when
// the last range left blocks unexecuted: re-fetch above the executed frontier
// when the link skeleton already authenticates that height (bodies were
// evicted), or look ahead above the highest fetched block when it is still
// below target.
func (s *Service) anchorNextStart(target uint64, authorized bool) (uint64, bool) {
	if syncanchor.Current() != syncanchor.Enforce || s.anchorVerifier() == nil {
		return 0, false
	}
	s.releaseAnchoredTail(authorized)
	applied := s.appliedAuthedTop(currentBlockNumberOrZero(s.cfg.chain))
	if s.anchors.authedLinkAt(applied + 1) {
		// The skeleton authenticates the next height but its body is not
		// executed (evicted, or the tail holds another branch): re-fetch.
		// Executed blocks above the canonical head are not re-fetched.
		return applied + 1, true
	}
	s.anchors.mu.Lock()
	top, held := s.anchors.skelTop, len(s.anchors.tail)
	s.anchors.mu.Unlock()
	if held == 0 {
		return 0, false // nothing waits for an anchor
	}
	if top > applied && top < target {
		return top + 1, true
	}
	return 0, false
}
