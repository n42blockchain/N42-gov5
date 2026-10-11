// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// slowCommitProducer is a producer whose CommitToCanonical takes the chain
// writer for a while, and which records when a build is triggered.
type slowCommitProducer struct {
	commitTook time.Duration
	mu         sync.Mutex
	committed  []types.Hash
	triggered  chan time.Time
}

func (p *slowCommitProducer) TriggerBlockProduction(types.Hash) { p.triggered <- time.Now() }
func (p *slowCommitProducer) CommitToCanonical(h types.Hash) error {
	time.Sleep(p.commitTook)
	p.mu.Lock()
	p.committed = append(p.committed, h)
	p.mu.Unlock()
	return nil
}

// newLeaderCommitService builds a started-like Service whose node leads the
// engine's current view, with a lag gate that is always ready.
func newLeaderCommitService(t *testing.T, async bool, p *slowCommitProducer) (*Service, context.CancelFunc) {
	t.Helper()
	setup := newTestSetup(t, 4)
	leader := LeaderForView(1, setup.vs)
	h := New(nil, nil)
	h.Authorize(setup.validators[leader].Address, setup.keys[leader])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		engine:            h,
		db:                memdb.NewTestDB(t),
		ctx:               ctx,
		cancel:            cancel,
		blockProducer:     p,
		persistInterval:   1 << 30,
		pendingExecutions: make(map[types.Hash]struct{}),
		notifiedImports:   make(map[types.Hash]struct{}),
	}
	s.SetLagGate(func(types.Hash) (bool, types.Hash, error) { return true, types.Hash{}, nil })
	if !h.Engine().IsCurrentLeader() {
		t.Skip("test validator does not lead the engine's current view")
	}
	if async {
		s.commitCh = make(chan commitJob, commitQueueLen)
		s.wg.Add(1)
		go s.commitLoop()
	}
	return s, func() { cancel(); s.wg.Wait() }
}

// TestCommitLoopUnblocksLeaderTrigger is the A/B of the commit loop: the
// output loop handles a commit whose canonicalization takes 80 ms, then the
// view change; the time to the leader's build trigger is measured inline (A)
// and with the commit loop (B). Commits stay in order either way.
func TestCommitLoopUnblocksLeaderTrigger(t *testing.T) {
	run := func(async bool) time.Duration {
		p := &slowCommitProducer{commitTook: 80 * time.Millisecond, triggered: make(chan time.Time, 8)}
		s, stop := newLeaderCommitService(t, async, p)
		defer stop()
		var ds []time.Duration
		for i := 1; i <= 3; i++ {
			start := time.Now()
			s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: ViewNumber(i), Hash: types.Hash{byte(i)}}, time.Time{})
			s.handleOutput(EngineOutput{Type: OutputViewChanged, View: ViewNumber(i + 1)}, time.Time{})
			select {
			case at := <-p.triggered:
				ds = append(ds, at.Sub(start))
			case <-time.After(5 * time.Second):
				t.Fatal("no build trigger")
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			p.mu.Lock()
			n := len(p.committed)
			p.mu.Unlock()
			if n == 3 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.committed) != 3 || p.committed[0] != (types.Hash{1}) || p.committed[1] != (types.Hash{2}) || p.committed[2] != (types.Hash{3}) {
			t.Fatalf("commits %v, want 1,2,3 in order", p.committed)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[1]
	}
	inline := run(false)
	async := run(true)
	t.Logf("commit -> leader build trigger median: inline %v, commit loop %v", inline, async)
	if inline < 70*time.Millisecond {
		t.Fatalf("inline trigger %v did not wait for the commit", inline)
	}
	if async > 20*time.Millisecond {
		t.Fatalf("commit loop trigger %v still waits for the commit", async)
	}
}
