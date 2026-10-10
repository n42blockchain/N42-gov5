// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

type lagGateProducer struct {
	mu      sync.Mutex
	parents []types.Hash
}

func (p *lagGateProducer) TriggerBlockProduction(h types.Hash) {
	p.mu.Lock()
	p.parents = append(p.parents, h)
	p.mu.Unlock()
}
func (p *lagGateProducer) CommitToCanonical(types.Hash) error { return nil }
func (p *lagGateProducer) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.parents)
}

type lagGateFetcher struct {
	mu      sync.Mutex
	applied bool
	fetched []types.Hash
}

func (f *lagGateFetcher) FetchBlockByHash(h types.Hash) {
	f.mu.Lock()
	f.fetched = append(f.fetched, h)
	f.mu.Unlock()
}
func (f *lagGateFetcher) CatchUp()                             {}
func (f *lagGateFetcher) HeightBehind() uint64                 { return 0 }
func (f *lagGateFetcher) BlockApplied(types.Hash, uint64) bool { return f.applied }

// newLeaderService returns a service whose engine leads its current view.
func newLeaderService(t *testing.T) (*Service, *lagGateProducer, *lagGateFetcher) {
	t.Helper()
	setup := newTestSetup(t, 4)
	for i := range setup.validators {
		h := New(nil, nil)
		h.Authorize(setup.validators[i].Address, setup.keys[i])
		if err := h.InitEngine(setup.validators, setup.f); err != nil {
			t.Fatal(err)
		}
		if !h.Engine().IsCurrentLeader() {
			continue
		}
		producer := &lagGateProducer{}
		fetcher := &lagGateFetcher{applied: true}
		return &Service{
			engine: h, db: memdb.NewTestDB(t), ctx: context.Background(),
			blockProducer: producer, blockFetcher: fetcher,
			pendingExecutions: make(map[types.Hash]struct{}),
			notifiedImports:   make(map[types.Hash]struct{}),
		}, producer, fetcher
	}
	t.Fatal("no validator leads the first view")
	return nil, nil, nil
}

// TestLagGateParksUntilNMinusTwoExecutes (P2a item c): under deferred depth
// 2 a leader does not need its parent applied -- the committed-parent and
// applied-parent gates are replaced -- but it may not propose until it has
// executed the block its header reports. The view parks on that block and
// resumes when its import lands.
func TestLagGateParksUntilNMinusTwoExecutes(t *testing.T) {
	s, producer, _ := newLeaderService(t)
	parent := types.Hash{0x21}
	grand := &block.Header{Number: uint256.NewInt(19), Difficulty: uint256.NewInt(0)}
	target := hsTWriteHeader(t, s, grand)
	var executed atomic.Bool
	s.SetLagGate(func(p types.Hash) (bool, types.Hash, error) {
		if p != parent {
			t.Errorf("gate asked about %x", p[:4])
		}
		return executed.Load(), target, nil
	})
	// A committed-but-unexecuted streak would block the old gates; the lag
	// gate does not consult them.
	s.unexecutedCommitFailures = 200

	view := s.engine.Engine().CurrentView()
	s.triggerBlockProduction(view, parent)
	if producer.count() != 0 {
		t.Fatal("produced before N-2 executed")
	}
	s.pendingMu.Lock()
	parked := s.deferredProduce.parent == parent && s.deferredProduce.waitFor == target
	s.pendingMu.Unlock()
	if !parked {
		t.Fatal("view not parked on the lagged block")
	}
	// An unrelated import does not resume it.
	s.NotifyBlockImported(types.Hash{0x99}, types.Hash{})
	if producer.count() != 0 {
		t.Fatal("resumed on an unrelated import")
	}
	executed.Store(true)
	s.NotifyBlockImported(target, types.Hash{})
	deadline := time.Now().Add(2 * time.Second)
	for producer.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if producer.count() != 1 || producer.parents[0] != parent {
		t.Fatalf("production on the lagged import: %v", producer.parents)
	}
}

// TestAvailableCommitIsNotReportedUnexecuted: a committed block held but not
// executed yet is neither refetched nor counted toward the unexecuted-commit
// streak (P2a: execution runs behind consensus by design).
func TestAvailableCommitIsNotReportedUnexecuted(t *testing.T) {
	s, _, fetcher := newLeaderService(t)
	h := types.Hash{0x31}
	s.SetAvailabilityProbe(func(x types.Hash) bool { return x == h })
	fetcher.applied = false
	s.handleOutput(EngineOutput{Type: OutputExecuteBlock, Hash: h}, time.Now())
	if len(fetcher.fetched) != 0 {
		t.Fatalf("available block fetched again: %v", fetcher.fetched)
	}
	if !s.availableHere(h) || s.availableHere(types.Hash{0x32}) {
		t.Fatal("probe")
	}
}

// TestLagGateZeroParentResumes: the first views after a reseed have no
// LockedQC block (zero parent: build on the canonical head). A view parked
// on the lag gate with that zero parent resumes when the awaited block
// imports, and the zero parent is never fetched.
func TestLagGateZeroParentResumes(t *testing.T) {
	s, producer, fetcher := newLeaderService(t)
	target := hsTWriteHeader(t, s, &block.Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(0)})
	var executed atomic.Bool
	s.SetLagGate(func(types.Hash) (bool, types.Hash, error) { return executed.Load(), target, nil })
	s.triggerBlockProduction(s.engine.Engine().CurrentView(), types.Hash{})
	if producer.count() != 0 || len(fetcher.fetched) != 0 {
		t.Fatal("produced or fetched before the lagged block executed")
	}
	executed.Store(true)
	s.NotifyBlockImported(target, types.Hash{})
	deadline := time.Now().Add(2 * time.Second)
	for producer.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if producer.count() != 1 || producer.parents[0] != (types.Hash{}) {
		t.Fatalf("zero-parent view did not resume: %v", producer.parents)
	}
}
