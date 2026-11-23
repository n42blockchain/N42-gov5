// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// One run of the parallel catch-up pipeline (sync v2 design §4.2, S2).
//
//	fetch workers (one goroutine per request, several peers in parallel)
//	    read + decode + screen numbers and parent links of their response
//	    -> reorder buffer (bounded in blocks and bytes)
//	single importer (the goroutine that owns catchUpInProgress)
//	    takes the run at its cursor as soon as it is present,
//	    checks the link to the block below the cursor,
//	    passes the S1 anchor gate (observe / enforce, unchanged rules),
//	    executes one block per transaction (Q3 = no batched commits).
//
// Fetching overlaps execution: while block N executes, N+1... download into
// the buffer. The importer is the only goroutine of the run that inserts.
//
// What the run leaves to the serial catch-up (catchUpTo), counted in
// sync_pipeline_fallbacks_total: a fork below the cursor (two peers disagree
// with the executed head: equal-height fork recovery needs branch-switch
// authority and backtracking), an unknown-ancestor insert failure, and an
// unanchored stretch long enough for the S1 tail bound to evict (look-ahead
// for the next QC).

package sync

import (
	"context"
	"errors"
	gosync "sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// Fallback reasons (sync_pipeline_fallbacks_total).
const (
	pipeFallbackFork    = "fork"
	pipeFallbackInsert  = "insert"
	pipeFallbackEvicted = "tail_evicted"
)

// pipeStallTimeout ends a run that made no progress with nothing in flight;
// the next commit or the 8 s tick starts a new one.
var pipeStallTimeout = 10 * time.Second

// pipeProbeMisses ends a run whose cursor is above the backed height (the
// verified commit watermark or the quorum height) once that many replies
// covering the cursor came back without it: the target was only a claim
// (CatchUp follows the highest advertised height) and nobody stores more.
const pipeProbeMisses = 2

// pipeTick wakes the importer to re-plan (timeouts, head-of-line duplicates,
// back-off expiry).
const pipeTick = 100 * time.Millisecond

type pipeRun struct {
	s    *Service
	ctx  context.Context
	stop context.CancelFunc
	mode syncanchor.Mode

	mu      gosync.Mutex
	cond    *gosync.Cond
	sched   *pipeScheduler
	buf     *reorderBuffer
	cancels map[uint64]context.CancelFunc
	ended   bool

	cursor   uint64     // next number the importer needs
	prevHash types.Hash // hash of the block below the cursor
	target   uint64
	nextSeg  uint64
	// misses counts replies covering the cursor that did not deliver it,
	// since the cursor last moved (missAt).
	misses int
	missAt uint64

	// disagree records, per hash below the cursor, the peers whose
	// responses did not link to it.
	disagree map[types.Hash]map[peer.ID]struct{}

	lastProgress time.Time
	imported     int
}

// usePipeline decides whether a catch-up to target runs the pipeline: the
// switch is on and the node is in Range mode or the gap exceeds the live
// window. Small live gaps keep the serial path (equal-height fork recovery).
func (s *Service) usePipeline(target uint64) bool {
	if !PipelineOn() {
		return false
	}
	if s.syncModes.current() == syncModeRange {
		return true
	}
	head := currentBlockNumberOrZero(s.cfg.chain)
	return target > head && target-head > defaultSyncModeParams().enterGap
}

// pipelineCatchUp runs the pipeline towards target (raised by targets that
// arrive meanwhile). It returns a non-empty fallback reason when the serial
// catch-up must take over, and the target reached for.
func (s *Service) pipelineCatchUp(target uint64) (string, uint64) {
	mode := syncanchor.Current()
	if mode == syncanchor.Enforce {
		s.releaseAnchoredTail(false)
	}
	head := s.cfg.chain.CurrentBlock()
	if head == nil || target <= head.Number64().Uint64() {
		return "", target
	}
	ctx, stop := context.WithCancel(s.ctx)
	r := &pipeRun{
		s: s, ctx: ctx, stop: stop, mode: mode,
		sched:        newPipeScheduler(pipelineCfg),
		buf:          newReorderBuffer(pipelineCfg.bufferBlocks, pipelineCfg.bufferBytes),
		cancels:      make(map[uint64]context.CancelFunc),
		target:       target,
		disagree:     make(map[types.Hash]map[peer.ID]struct{}),
		lastProgress: time.Now(),
	}
	r.cond = gosync.NewCond(&r.mu)
	r.resetCursor()
	start, from := time.Now(), r.cursor
	log.Info("sync pipeline: run started", "from", from, "target", target, "anchor", mode.String(),
		"mode", s.syncModes.current().String())
	go r.ticker()
	reason := r.loop()
	r.end()
	took := time.Since(start)
	rate := 0.0
	if took > 0 {
		rate = float64(r.imported) / took.Seconds()
	}
	log.Info("sync pipeline: run ended", "from", from, "head", currentBlockNumberOrZero(s.cfg.chain),
		"target", r.target, "imported", r.imported, "took", took, "blocksPerSec", rate, "fallback", reason)
	if reason != "" {
		syncPipelineFallbacks.WithLabelValues(reason).Inc()
	}
	return reason, r.target
}

func (r *pipeRun) ticker() {
	t := time.NewTicker(pipeTick)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
			return
		case <-t.C:
			r.mu.Lock()
			r.cond.Broadcast()
			r.mu.Unlock()
		}
	}
}

func (r *pipeRun) end() {
	r.mu.Lock()
	r.ended = true
	for _, cancel := range r.cancels {
		cancel()
	}
	r.cancels = map[uint64]context.CancelFunc{}
	r.sched.cancelAll()
	r.buf.clear()
	r.mu.Unlock()
	r.stop()
	syncPipelineBufferBlocks.Set(0)
	syncPipelineBufferBytes.Set(0)
	syncPipelineInflight.Set(0)
}

// resetCursor places the cursor above the held S1 tail when it extends the
// applied head, else above the head.
func (r *pipeRun) resetCursor() {
	head := r.s.cfg.chain.CurrentBlock()
	r.cursor, r.prevHash = head.Number64().Uint64()+1, head.Hash()
	if r.mode != syncanchor.Enforce {
		return
	}
	b := &r.s.anchors
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.tail) == 0 {
		return
	}
	first, top := b.tail[0], b.tail[len(b.tail)-1]
	if first.ParentHash() == head.Hash() && first.Number64().Uint64() == r.cursor {
		r.cursor, r.prevHash = top.Number64().Uint64()+1, top.Hash()
	}
}

// loop is the importer.
func (r *pipeRun) loop() string {
	for {
		run, done := r.next()
		if done {
			return ""
		}
		if run == nil {
			// Woken without a run: a later anchor may have authenticated
			// part of the held tail.
			if r.mode == syncanchor.Enforce {
				if _, ok := r.s.anchors.tailReleasable(); ok {
					if n := r.s.releaseAnchoredTail(false); n > 0 {
						r.progress(n)
					}
					r.mu.Lock()
					r.resetCursor()
					r.mu.Unlock()
				}
			}
			continue
		}
		if reason := r.importRun(run); reason != "" {
			return reason
		}
	}
}

// next waits until the run at the cursor is buffered and takes it. done
// reports the end of the run (target reached, stalled, or shutting down).
func (r *pipeRun) next() (run []pipeBlock, done bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.ctx.Err() != nil {
			return nil, true
		}
		if t := r.s.catchUpTarget.Swap(0); t > r.target {
			r.target = t
		}
		if r.s.syncModes.current() == syncModeRange {
			r.s.raiseRangeEnd(r.target)
		}
		r.dispatchLocked()
		if run := r.buf.take(r.cursor, int(pipelineCfg.slotBlocks)); len(run) > 0 {
			r.gaugesLocked()
			return run, false
		}
		if r.cursor > r.target {
			return nil, true
		}
		if r.missAt == r.cursor && r.misses >= pipeProbeMisses &&
			r.cursor > r.s.catchUpBackedHeight(currentBlockNumber(r.s.cfg.chain), len(r.s.cfg.p2p.Peers().Connected())) {
			log.Info("sync pipeline: nothing stored above the backed height, ending the run",
				"cursor", r.cursor, "target", r.target)
			return nil, true
		}
		if len(r.sched.inflight) == 0 && time.Since(r.lastProgress) > pipeStallTimeout {
			log.Warn("sync pipeline: no progress, ending the run", "cursor", r.cursor, "target", r.target)
			return nil, true
		}
		if r.mode == syncanchor.Enforce {
			if _, ok := r.s.anchors.tailReleasable(); ok {
				return nil, false
			}
		}
		r.cond.Wait()
	}
}

func (r *pipeRun) gaugesLocked() {
	syncPipelineBufferBlocks.Set(float64(r.buf.len()))
	syncPipelineBufferBytes.Set(float64(r.buf.size()))
	syncPipelineInflight.Set(float64(len(r.sched.inflight)))
}

func (r *pipeRun) progress(n int) {
	r.mu.Lock()
	r.imported += n
	r.lastProgress = time.Now()
	r.mu.Unlock()
	syncPipelineImported.Add(float64(n))
}

// candidatesLocked lists the peers a slot may go to, from the stage-A peer
// set (bad peers excluded) with their claims and catch-up back-off.
func (r *pipeRun) candidatesLocked(now time.Time) []pipeCandidate {
	peers := r.s.catchUpPeerSet()
	out := make([]pipeCandidate, 0, len(peers))
	for _, pid := range peers {
		c := pipeCandidate{pid: pid}
		if h := r.s.cfg.p2p.Peers().PeerHeight(pid); h != nil {
			c.claim, c.known = h.Uint64(), true
		}
		st := r.s.catchUpBook.snapshot(pid)
		c.failures = st.failures
		c.backedOff = now.Before(st.until)
		out = append(out, c)
	}
	return out
}

func (r *pipeRun) dispatchLocked() {
	if r.ended {
		return
	}
	now := time.Now()
	cands := r.candidatesLocked(now)
	if len(cands) == 0 {
		return
	}
	self := currentBlockNumber(r.s.cfg.chain)
	backed := r.s.catchUpBackedHeight(self, len(cands))
	for _, q := range r.sched.plan(now, r.cursor, r.target, r.buf, cands, backed) {
		r.launchLocked(q)
	}
	r.gaugesLocked()
}

func (r *pipeRun) launchLocked(q *pipeReq) {
	if q.dup {
		syncPipelineRequests.WithLabelValues(pipeOutcomeDup).Inc()
	}
	ctx, cancel := context.WithTimeout(r.ctx, q.wholeTO)
	r.cancels[q.id] = cancel
	go func() {
		t0 := time.Now()
		var first atomic.Int64
		firstTimer := time.AfterFunc(q.firstTO, cancel)
		req := &sync_pb.BodiesByRangeRequest{
			StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(q.start)),
			Count:            q.count(),
			Step:             1,
		}
		fetched, err := sendBodiesByRange(ctx, r.s.cfg.chain, r.s.cfg.p2p, q.pid, req, func(*block.Block) error {
			if first.Load() == 0 {
				first.Store(int64(time.Since(t0)) | 1)
				firstTimer.Stop()
			}
			return nil
		}, true)
		firstTimer.Stop()
		timedOut := ctx.Err() != nil && r.ctx.Err() == nil
		cancel()
		if err != nil && timedOut && !errors.Is(err, context.DeadlineExceeded) {
			// The first-chunk timer cancels; stage A treats it as a timeout.
			err = context.DeadlineExceeded
		}
		r.onResult(q, fetched, err, time.Duration(first.Load()), time.Since(t0))
	}()
}

// onResult screens one response in its fetch goroutine and buffers it.
func (r *pipeRun) onResult(q *pipeReq, fetched []*block.Block, err error, firstLatency, took time.Duration) {
	// Screen outside the lock: numbers contiguous from the request start and
	// parent links inside the response (standard §3.4 step 3).
	blocks := make([]block.IBlock, 0, len(fetched))
	broken := false
	for i, b := range fetched {
		if b == nil {
			broken = true
			break
		}
		if b.Number64().Uint64() != q.start+uint64(i) ||
			(i > 0 && b.ParentHash() != fetched[i-1].Hash()) {
			broken = true
			break
		}
		blocks = append(blocks, b)
	}
	var bytes uint64
	pbs := make([]pipeBlock, len(blocks))
	for i, b := range blocks {
		sz := estimateBlockBytes(b)
		bytes += sz
		pbs[i] = pipeBlock{blk: b, bytes: sz, peer: q.pid}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.cond.Broadcast()
	delete(r.cancels, q.id)
	if r.ended {
		return
	}
	now := time.Now()
	if q.start <= r.cursor && r.cursor <= q.end &&
		(err != nil || len(blocks) == 0 || blocks[len(blocks)-1].Number64().Uint64() < r.cursor) {
		if r.missAt != r.cursor {
			r.missAt, r.misses = r.cursor, 0
		}
		r.misses++
	}
	h := r.s.cfg.p2p.Peers().PeerHeight(q.pid)
	withinClaim := h != nil && h.Uint64() >= q.end
	switch {
	case err != nil:
		r.s.noteCatchUpRequestFailed(q.pid, err, withinClaim)
		outcome := pipeOutcomeFailed
		if errors.Is(err, ErrInvalidFetchedData) {
			outcome = pipeOutcomeFault
		}
		syncPipelineRequests.WithLabelValues(outcome).Inc()
		r.sched.complete(q, false, 0, 0, 0, took, now)
		log.Debug("sync pipeline: request failed", "peer", shortPeer(q.pid), "from", q.start, "to", q.end, "err", err)
		return
	case len(fetched) == 0:
		r.s.noteCatchUpEmpty(q.pid, withinClaim)
		syncPipelineRequests.WithLabelValues(pipeOutcomeEmpty).Inc()
		r.sched.complete(q, false, 0, 0, 0, took, now)
		return
	}
	if broken {
		if r.mode != syncanchor.Off {
			countAnchorRefusal(anchorReasonBrokenLink, 1)
		}
		if r.mode == syncanchor.Enforce {
			// A provable fault of the serving peer (stage A scoring).
			r.s.noteCatchUpInvalid(q.pid)
			syncPipelineRequests.WithLabelValues(pipeOutcomeFault).Inc()
			r.sched.complete(q, false, 0, 0, 0, took, now)
			log.Warn("sync pipeline: response dropped, broken link", "peer", shortPeer(q.pid), "from", q.start, "to", q.end)
			return
		}
		// Observe / off: keep the linked prefix (the serial path's insert
		// would stop at the break the same way).
	}
	if len(blocks) == 0 {
		syncPipelineRequests.WithLabelValues(pipeOutcomeFault).Inc()
		r.sched.complete(q, false, 0, 0, 0, took, now)
		return
	}
	r.s.noteCatchUpServed(q.pid, len(blocks), took)
	r.nextSeg++
	for i := range pbs {
		pbs[i].seg = r.nextSeg
	}
	r.buf.admit(r.cursor, pbs)
	r.sched.complete(q, true, len(blocks), bytes, firstLatency, took, now)
	if uint64(len(blocks)) < q.count() {
		syncPipelineRequests.WithLabelValues(pipeOutcomeShort).Inc()
	} else {
		syncPipelineRequests.WithLabelValues(pipeOutcomeOK).Inc()
	}
	r.gaugesLocked()
}

// dropSegLocked discards what remains buffered of a response and backs its
// peer off for this run (no score).
func (r *pipeRun) dropSegLocked(seg uint64, pid peer.ID) {
	r.buf.dropSeg(seg)
	r.sched.backoff(pid, time.Now())
}

// importRun links, gates and executes one in-order run.
func (r *pipeRun) importRun(run []pipeBlock) string {
	pid, seg := run[0].peer, run[0].seg
	blocks := make([]block.IBlock, len(run))
	for i := range run {
		blocks[i] = run[i].blk
	}

	r.mu.Lock()
	prevHash := r.prevHash
	r.mu.Unlock()
	if blocks[0].ParentHash() != prevHash {
		return r.mismatch(pid, seg, prevHash, blocks[0])
	}

	exec := blocks
	if r.mode != syncanchor.Off {
		gate := r.s.anchorScreen(pid, blocks, r.mode)
		if gate.active && r.mode == syncanchor.Enforce {
			if gate.fault != "" {
				r.s.noteCatchUpInvalid(pid)
				log.Warn("sync pipeline: run dropped, provable fault", "peer", shortPeer(pid),
					"fault", gate.fault, "from", blocks[0].Number64().Uint64(), "count", len(blocks))
				r.mu.Lock()
				r.dropSegLocked(seg, pid)
				r.resetCursor()
				r.mu.Unlock()
				return ""
			}
			if gate.evicted > 0 {
				return pipeFallbackEvicted
			}
			exec = gate.exec
		}
	}

	imported, knownBad, err := r.insert(exec)
	if imported > 0 {
		r.progress(imported)
	}
	r.s.observeSyncMode(false)
	r.mu.Lock()
	// The cursor follows the executed head, or the top of the held S1 tail.
	r.resetCursor()
	r.buf.dropBelow(r.cursor)
	if knownBad {
		r.s.noteCatchUpInvalid(pid)
		r.dropSegLocked(seg, pid)
	}
	r.mu.Unlock()
	if err == nil {
		return ""
	}
	if errors.Is(err, consensus.ErrExecutionInvalid) {
		r.s.noteCatchUpInvalid(pid)
		log.Warn("sync pipeline: block failed execution, ending the run", "peer", shortPeer(pid), "err", err)
		r.mu.Lock()
		r.dropSegLocked(seg, pid)
		r.mu.Unlock()
		return ""
	}
	if isAncestorError(err) {
		return pipeFallbackInsert
	}
	log.Warn("sync pipeline: insert failed", "err", err, "imported", imported)
	return pipeFallbackInsert
}

// insert executes blocks one per transaction, skipping blocks the live path
// already applied (no double import). knownBad reports a run that starts with
// a block of a branch already known bad.
//
// One skip rule with the serial catch-up (catchUpRange): an applied block is
// never re-inserted, and under N42_CATCHUP_SKIP_APPLIED it is reported to the
// engine as an import would (notifyCatchUpApplied). Unlike the serial path
// the pipeline does not skip blocks the lag pipeline holds: its next block
// needs that parent applied, and in Range mode the lag executor leaves the
// height to this importer anyway.
func (r *pipeRun) insert(blocks []block.IBlock) (imported int, knownBad bool, err error) {
	todo := make([]block.IBlock, 0, len(blocks))
	var applied []block.IBlock
	for _, b := range blocks {
		if r.s.blockApplied(b.Hash(), b.Number64().Uint64()) {
			applied = append(applied, b)
			continue
		}
		todo = append(todo, b)
	}
	if catchUpSkipAppliedOn && len(applied) > 0 {
		catchUpSkippedApplied.Add(uint64(len(applied)))
		r.s.notifyCatchUpApplied(applied)
	}
	if len(todo) == 0 {
		return 0, false, nil
	}
	if r.s.rejectBadBlock(r.ctx, todo[0]) {
		return 0, true, nil
	}
	_, imported, err = r.s.insertCatchUpBlocks(r.ctx, todo, false)
	return imported, false, err
}

// mismatch handles a run whose first block does not link to the block below
// the cursor. One peer disagreeing is dropped and backed off (no score: it may
// serve a fork it believes in). Two distinct peers disagreeing with the
// executed head means this node is on a fork: the serial catch-up recovers it
// with branch-switch authority. Two disagreeing with an unexecuted held tail
// discard the tail, which is re-fetched.
func (r *pipeRun) mismatch(pid peer.ID, seg uint64, below types.Hash, first block.IBlock) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropSegLocked(seg, pid)
	set := r.disagree[below]
	if set == nil {
		set = make(map[peer.ID]struct{})
		r.disagree[below] = set
	}
	set[pid] = struct{}{}
	log.Info("sync pipeline: response does not link to the block below it", "peer", shortPeer(pid),
		"number", first.Number64().Uint64(), "parent", first.ParentHash(), "below", below, "peers", len(set))
	if len(set) < 2 {
		return ""
	}
	head := r.s.cfg.chain.CurrentBlock()
	if head != nil && head.Hash() == below {
		return pipeFallbackFork
	}
	r.s.anchors.dropTail()
	delete(r.disagree, below)
	r.resetCursor()
	return ""
}
