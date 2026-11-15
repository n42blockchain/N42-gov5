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
	"github.com/n42blockchain/N42/log"
)

// N42_FRAME_LOCAL_AFTER_MS=N (default off): a frame whose senders are
// expected from attestations (P8b) and are still not cached N ms after its
// admission is recovered on this node, in the background, oldest first, on
// a small pool of its own (N42_FRAME_LOCAL_WORKERS, default 2) -- never on
// a block builder's goroutine. Run 72: a gateway's head frame held 25-32% of
// the builds while 45-60 ms old, waiting for other members' attestations
// (20-50 ms after admission for 62% of frames), and a wait at build time
// (N42_FRAME_HELD_WAIT_MS) cost more than it gained; recovering the late
// frames by age makes the head of each stream ready before the build looks
// at it. Suggested N: 15-20.
//
// Exactly once: the recovery claims the frame in the store (BeginRecover)
// like every other path, so a frame recovered, being recovered or claimed by
// a block is skipped, and senders an attestation delivered first are taken
// instead of recovered. An attestation that arrives while the local
// recovery runs caches its (digest-checked) list first; readiness is counted
// once, by whichever came first. What a block picks and in which order is
// not touched: only when a frame becomes ready.
const (
	EnvLocalAfterMs  = "N42_FRAME_LOCAL_AFTER_MS"
	EnvLocalWorkers  = "N42_FRAME_LOCAL_WORKERS"
	defaultLocalWkrs = 2
	localQueuePerWkr = 32
	localTickMin     = 2 * time.Millisecond
	localTickMax     = 10 * time.Millisecond
	localLogEvery    = 10 * time.Second
)

// LocalAfterFromEnv reads N42_FRAME_LOCAL_AFTER_MS (0: off).
func LocalAfterFromEnv() time.Duration {
	if v, err := strconv.Atoi(os.Getenv(EnvLocalAfterMs)); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return 0
}

// LocalWorkersFromEnv reads N42_FRAME_LOCAL_WORKERS (default 2).
func LocalWorkersFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv(EnvLocalWorkers)); err == nil && v > 0 {
		return v
	}
	return defaultLocalWkrs
}

// LocalAger recovers frames still without senders a set age after their
// admission (N42_FRAME_LOCAL_AFTER_MS) through a SenderRecoverer's recovery.
type LocalAger struct {
	r       *SenderRecoverer
	after   time.Duration
	workers int
	jobs    chan recoverJob

	mu  sync.Mutex
	inq map[types.Hash]struct{} // queued or being recovered here

	queued, recovered, attested, skipped atomic.Uint64
	// lateUs sums the frames' age when queued beyond after (scan lag).
	lateUs atomic.Uint64

	startOnce, stopOnce sync.Once
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
}

// NewLocalAger returns an ager recovering through r the frames older than
// after (> 0) on workers goroutines (<= 0: the default).
func NewLocalAger(r *SenderRecoverer, after time.Duration, workers int) *LocalAger {
	if workers <= 0 {
		workers = defaultLocalWkrs
	}
	return &LocalAger{r: r, after: after, workers: workers,
		jobs: make(chan recoverJob, workers*localQueuePerWkr), inq: make(map[types.Hash]struct{})}
}

// Start launches the scan loop and the workers.
func (a *LocalAger) Start(ctx context.Context) {
	a.startOnce.Do(func() {
		ctx, a.cancel = context.WithCancel(ctx)
		for i := 0; i < a.workers; i++ {
			a.wg.Add(1)
			go a.work(ctx)
		}
		a.wg.Add(1)
		go a.loop(ctx)
	})
}

// Stop stops the loop and waits for the workers.
func (a *LocalAger) Stop() {
	a.stopOnce.Do(func() {
		if a.cancel != nil {
			a.cancel()
		}
		a.wg.Wait()
	})
}

func (a *LocalAger) loop(ctx context.Context) {
	defer a.wg.Done()
	tick := a.after / 4
	if tick < localTickMin {
		tick = localTickMin
	}
	if tick > localTickMax {
		tick = localTickMax
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	logT := time.NewTicker(localLogEvery)
	defer logT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			a.Tick(now)
		case <-logT.C:
			q, rec, att, sk := a.Stats()
			if q > 0 {
				log.Info("frame local recovery by age", "afterMs", a.after.Milliseconds(), "queued", q, "recovered", rec,
					"attestedFirst", att, "skipped", sk, "lateMsMean", float64(a.lateUs.Load())/float64(q)/1e3)
			}
		}
	}
}

// Tick queues, oldest first, the frames admitted at least after before now
// that still need their senders, as many as the queue holds; it returns how
// many it queued. Exported for tests (the loop calls it every tick).
func (a *LocalAger) Tick(now time.Time) int {
	free := cap(a.jobs) - len(a.jobs)
	if free <= 0 {
		return 0
	}
	cutoff := now.Add(-a.after).UnixNano()
	a.mu.Lock()
	frames := a.r.st.AgedUnready(cutoff, free, func(h types.Hash) bool {
		_, in := a.inq[h]
		return in
	})
	for _, f := range frames {
		a.inq[f.H] = struct{}{}
	}
	a.mu.Unlock()
	n := 0
	for _, f := range frames {
		select {
		case a.jobs <- recoverJob{h: f.H, raw: f.Raw, local: true, aged: true}:
			n++
			a.queued.Add(1)
			if info := a.r.st.RecoverInfoOf(f.H); info.Stored && info.Age > a.after {
				a.lateUs.Add(uint64((info.Age - a.after).Microseconds()))
			}
		default:
			a.done(f.H)
		}
	}
	return n
}

func (a *LocalAger) done(h types.Hash) {
	a.mu.Lock()
	delete(a.inq, h)
	a.mu.Unlock()
}

func (a *LocalAger) work(ctx context.Context) {
	defer a.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-a.jobs:
			a.run(j)
		}
	}
}

// run recovers one queued frame and returns the outcome.
func (a *LocalAger) run(j recoverJob) int {
	o := a.r.recoverOne(j)
	switch o {
	case outcomeLocal:
		a.recovered.Add(1)
	case outcomeAttested:
		a.attested.Add(1)
	default:
		a.skipped.Add(1)
	}
	a.done(j.h)
	return o
}

// Stats returns the frames queued by age, recovered here, answered by an
// attestation by their turn, and skipped (recovered, running or claimed
// elsewhere first, gone, or not decodable).
func (a *LocalAger) Stats() (queued, recovered, attested, skipped uint64) {
	return a.queued.Load(), a.recovered.Load(), a.attested.Load(), a.skipped.Load()
}
