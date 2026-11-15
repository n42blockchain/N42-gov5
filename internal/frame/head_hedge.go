// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
)

// N42_FRAME_HEAD_HEDGE=K (default 0 = off): keep the head of every
// gateway's pick order ready. A build takes each gateway's frames as an
// arrival-order prefix and stops at the first frame whose senders are not
// cached; under frame attestation (P8b) that frame waits for another
// member's attestation, 20-50 ms after admission for most frames and
// longer when the covering member is busy (run 74: per-member recovery
// means 8-34 ms with tails to 1 s). One late member holds every stream
// behind its frames.
//
// The hedge recovers, on this node and on a small pool of its own
// (N42_FRAME_HEAD_HEDGE_WORKERS, default 2), the first K frames of each
// gateway that have no senders yet, once they have been without them for
// N42_FRAME_HEAD_HEDGE_MS (default 10) since admission. Nothing behind the
// first K is touched: the frames further back are given the time their
// attestations need. It is driven by the store's changes (admission,
// senders cached, a recovery released, pins and commits) and a timer for
// the next frame to pass the grace, not by a periodic scan, and it walks
// only the store's unready index.
//
// Exactly once: a hedged frame is claimed in the store (BeginRecover) like
// every other recovery, so a frame being recovered elsewhere or claimed by
// a block is left alone. An attestation that arrives at any time is cached
// at once (the frame is ready the moment either source delivers); a local
// recovery that finishes after it is discarded (FinishRecoverKeep), counted
// as wasted CPU, and readiness is counted once. What a block picks and in
// which order is not touched, and nothing runs on a builder's goroutine.
const (
	EnvHeadHedge        = "N42_FRAME_HEAD_HEDGE"
	EnvHeadHedgeMs      = "N42_FRAME_HEAD_HEDGE_MS"
	EnvHeadHedgeWorkers = "N42_FRAME_HEAD_HEDGE_WORKERS"

	defaultHeadHedgeGrace   = 10 * time.Millisecond
	defaultHeadHedgeWorkers = 2
	// headHedgeMinGap bounds the evaluations: one per gap at most, however
	// many changes arrive meanwhile.
	headHedgeMinGap = 500 * time.Microsecond
	headHedgeLog    = 10 * time.Second
)

// HeadHedgeFromEnv reads N42_FRAME_HEAD_HEDGE (K; 0: off),
// N42_FRAME_HEAD_HEDGE_MS (grace; default 10, 0 allowed) and
// N42_FRAME_HEAD_HEDGE_WORKERS (default 2).
func HeadHedgeFromEnv() (k int, grace time.Duration, workers int) {
	if v, err := strconv.Atoi(os.Getenv(EnvHeadHedge)); err == nil && v > 0 {
		k = v
	}
	grace = defaultHeadHedgeGrace
	if v, err := strconv.Atoi(os.Getenv(EnvHeadHedgeMs)); err == nil && v >= 0 {
		grace = time.Duration(v) * time.Millisecond
	}
	workers = defaultHeadHedgeWorkers
	if v, err := strconv.Atoi(os.Getenv(EnvHeadHedgeWorkers)); err == nil && v > 0 {
		workers = v
	}
	return k, grace, workers
}

// HeadHedge keeps the first K unready frames of each gateway recovered
// (N42_FRAME_HEAD_HEDGE).
type HeadHedge struct {
	r       *SenderRecoverer
	st      *store.Store
	k       int
	grace   time.Duration
	workers int
	// skip (optional) reports frames a builder does not pick from the store
	// (held by an unwritten block): they are not the head.
	skip func(types.Hash) bool
	now  func() time.Time
	// afterRecover (tests) runs between a local recovery and its caching.
	afterRecover func(types.Hash)

	kick chan struct{}
	jobs chan HeadFrameJob

	mu   sync.Mutex
	inq  map[types.Hash]struct{} // dispatched, not finished
	busy int

	// dispatched: frames handed to a worker; hedged: local recoveries run;
	// wonLocal: frames whose cached senders are this node's recovery;
	// wonAttested: frames another source answered first (before the
	// recovery started or while it ran); wastedUs: CPU spent on recoveries
	// that lost; evals: evaluations of the head.
	dispatched, hedged, wonLocal, wonAttested, wastedUs, evals atomic.Uint64

	startOnce, stopOnce sync.Once
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
}

// HeadFrameJob is one frame handed to a hedge worker.
type HeadFrameJob struct {
	H   types.Hash
	Raw []byte
}

// NewHeadHedge returns a hedge over r's store keeping the first k (> 0)
// unready frames of each gateway recovered once they are grace old, on
// workers goroutines (<= 0: the default). It turns the store's unready
// index on.
func NewHeadHedge(r *SenderRecoverer, k int, grace time.Duration, workers int) *HeadHedge {
	if workers <= 0 {
		workers = defaultHeadHedgeWorkers
	}
	if k <= 0 {
		k = 1
	}
	r.st.EnableHeadIndex()
	return &HeadHedge{r: r, st: r.st, k: k, grace: grace, workers: workers, now: time.Now,
		kick: make(chan struct{}, 1), jobs: make(chan HeadFrameJob, workers), inq: make(map[types.Hash]struct{})}
}

// SetSkip installs the builder's in-flight check (before Start).
func (h *HeadHedge) SetSkip(skip func(types.Hash) bool) { h.skip = skip }

// Kick tells the hedge the head may have moved. It never blocks.
func (h *HeadHedge) Kick() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// Start subscribes to the store's changes and launches the loop and the
// workers.
func (h *HeadHedge) Start(ctx context.Context) {
	h.startOnce.Do(func() {
		ctx, h.cancel = context.WithCancel(ctx)
		h.st.SetChangeNotify(h.Kick)
		for i := 0; i < h.workers; i++ {
			h.wg.Add(1)
			go h.work(ctx)
		}
		h.wg.Add(1)
		go h.loop(ctx)
		h.Kick()
	})
}

// Stop unsubscribes and waits for the loop and the workers.
func (h *HeadHedge) Stop() {
	h.stopOnce.Do(func() {
		h.st.SetChangeNotify(nil)
		if h.cancel != nil {
			h.cancel()
		}
		h.wg.Wait()
	})
}

func (h *HeadHedge) loop(ctx context.Context) {
	defer h.wg.Done()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	logT := time.NewTicker(headHedgeLog)
	defer logT.Stop()
	var last HeadHedgeStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
		case <-timer.C:
		case <-logT.C:
			if s := h.Stats(); s != last {
				log.Info("frame head hedge", "k", h.k, "graceMs", h.grace.Milliseconds(), "workers", h.workers,
					"dispatched", s.Dispatched, "hedged", s.Hedged, "wonLocal", s.WonLocal, "wonAttested", s.WonAttested,
					"wastedMs", s.WastedUs/1000, "evals", s.Evals)
				last = s
			}
			continue
		}
		wait := h.Evaluate()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if wait <= 0 {
			wait = time.Hour
		}
		timer.Reset(wait)
		// Coalesce the burst of changes behind this evaluation.
		select {
		case <-ctx.Done():
			return
		case <-time.After(headHedgeMinGap):
		}
	}
}

// headCand is one head frame found by an evaluation.
type headCand struct {
	f  store.HeadFrame
	gw types.Address
}

// Evaluate looks at the head of every gateway's unready frames once and
// hands the first K of each that passed the grace, are not being recovered
// and not claimed, to free workers. It returns how long until the next of
// those first K passes the grace (0: none pending). Exported for tests (the
// loop calls it on every change).
func (h *HeadHedge) Evaluate() time.Duration {
	h.evals.Add(1)
	// Collect a window of the oldest unready frames under the store's read
	// lock; the builder's in-flight check runs outside it. The window is
	// several times K so frames held by unwritten blocks do not hide the
	// head.
	window := 8*h.k + 32
	cands := make([]headCand, 0, window)
	h.st.HeadUnready(4*window, func(f store.HeadFrame) bool {
		if f.Claimed {
			return true // a block's import recovers it; not the builder's head
		}
		gw, _ := GatewayOf(f.Raw)
		cands = append(cands, headCand{f: f, gw: gw})
		return len(cands) < window
	})
	now := h.now()
	cutoff := now.Add(-h.grace).UnixNano()
	var next int64
	counted := make(map[types.Address]int, 2)
	for _, c := range cands {
		if h.skip != nil && h.skip(c.f.H) {
			continue
		}
		if counted[c.gw] >= h.k {
			continue
		}
		counted[c.gw]++
		if c.f.Running {
			continue
		}
		if c.f.At > cutoff {
			if at := c.f.At + int64(h.grace); next == 0 || at < next {
				next = at
			}
			continue
		}
		if !h.dispatch(c.f) {
			// No free worker: a finishing worker kicks the hedge again.
			break
		}
	}
	if next == 0 {
		return 0
	}
	if d := time.Duration(next - now.UnixNano()); d > 0 {
		return d
	}
	return time.Microsecond
}

// dispatch hands f to a free worker. It reports false when every worker is
// busy; a frame already dispatched counts as handled.
func (h *HeadHedge) dispatch(f store.HeadFrame) bool {
	h.mu.Lock()
	if _, in := h.inq[f.H]; in {
		h.mu.Unlock()
		return true
	}
	if h.busy >= h.workers {
		h.mu.Unlock()
		return false
	}
	h.busy++
	h.inq[f.H] = struct{}{}
	h.mu.Unlock()
	h.dispatched.Add(1)
	h.jobs <- HeadFrameJob{H: f.H, Raw: f.Raw} // capacity = workers >= busy
	return true
}

func (h *HeadHedge) work(ctx context.Context) {
	defer h.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-h.jobs:
			h.Run(j)
		}
	}
}

// Outcomes of a hedge job (Run).
const (
	HedgeNone        = iota // gone, recovered or claimed elsewhere, undecodable, no signer
	HedgeWonLocal           // this node's recovery became the cached senders
	HedgeWonAttested        // another source answered first
)

// Run recovers one dispatched frame on the caller's goroutine and returns
// the outcome (the workers call it; exported for tests).
func (h *HeadHedge) Run(j HeadFrameJob) (outcome int) {
	defer func() {
		h.mu.Lock()
		delete(h.inq, j.H)
		if h.busy > 0 {
			h.busy--
		}
		h.mu.Unlock()
		h.Kick()
	}()
	t := h.st.BeginRecover(j.H, true)
	if !t.Stored {
		return HedgeNone
	}
	if !t.Own {
		if t.Senders != nil {
			h.wonAttested.Add(1)
			return HedgeWonAttested
		}
		return HedgeNone
	}
	var ss *store.Senders
	finished := false
	defer func() {
		if recover() != nil {
			outcome = HedgeNone
		}
		if !finished {
			h.st.FinishRecover(t, ss)
		}
	}()
	signer := h.r.signer()
	if signer == nil {
		return HedgeNone
	}
	src := h.r.src
	if src != nil {
		if addrs := src.Senders(j.H, -1); addrs != nil {
			// Accepted by the pool, not yet cached by the store.
			ss = &store.Senders{Signer: signer, Addrs: addrs}
			h.wonAttested.Add(1)
			return HedgeWonAttested
		}
	}
	h.hedged.Add(1)
	t0 := time.Now()
	got, _, err := RecoverSenders(signer, j.Raw)
	if err != nil {
		return HedgeNone
	}
	if h.afterRecover != nil {
		h.afterRecover(j.H)
	}
	finished = true
	if !h.st.FinishRecoverKeep(t, got) {
		// An attestation was cached while this ran: it stands.
		h.wastedUs.Add(uint64(time.Since(t0).Microseconds()))
		h.wonAttested.Add(1)
		return HedgeWonAttested
	}
	h.wonLocal.Add(1)
	if src != nil {
		// A committee member publishes its attestation; any other node
		// counts a fallback.
		src.Recovered(j.H, got)
	}
	return HedgeWonLocal
}

// NextJobForTest takes the next dispatched job without a worker (false:
// none queued).
func (h *HeadHedge) NextJobForTest() (HeadFrameJob, bool) {
	select {
	case j := <-h.jobs:
		return j, true
	default:
		return HeadFrameJob{}, false
	}
}

// SetNowForTest replaces the hedge's clock.
func (h *HeadHedge) SetNowForTest(now func() time.Time) { h.now = now }

// HeadHedgeStats is a snapshot of the hedge's counters.
type HeadHedgeStats struct {
	Dispatched, Hedged, WonLocal, WonAttested, WastedUs, Evals uint64
}

// Stats returns the hedge's counters.
func (h *HeadHedge) Stats() HeadHedgeStats {
	return HeadHedgeStats{Dispatched: h.dispatched.Load(), Hedged: h.hedged.Load(), WonLocal: h.wonLocal.Load(),
		WonAttested: h.wonAttested.Load(), WastedUs: h.wastedUs.Load(), Evals: h.evals.Load()}
}
