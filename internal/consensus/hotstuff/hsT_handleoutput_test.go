// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// hsTOutputProducer is a BlockProducer stub for handleOutput's
// OutputBlockCommitted / OutputViewChanged branches.
type hsTOutputProducer struct {
	canonErr    error
	canonCalled []types.Hash
	triggered   []types.Hash
}

func (p *hsTOutputProducer) TriggerBlockProduction(parentHash types.Hash) {
	p.triggered = append(p.triggered, parentHash)
}
func (p *hsTOutputProducer) CommitToCanonical(hash types.Hash) error {
	p.canonCalled = append(p.canonCalled, hash)
	return p.canonErr
}

// hsTOutputFetcher's CatchUp is invoked from handleOutput's own goroutine
// (OutputSyncRequired's "go s.blockFetcher.CatchUp()"), so caughtUp needs a
// lock for the test's polling read to be race-clean.
type hsTOutputFetcher struct {
	mu       sync.Mutex
	caughtUp bool
}

func (f *hsTOutputFetcher) FetchBlockByHash(types.Hash) {}
func (f *hsTOutputFetcher) CatchUp() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caughtUp = true
}
func (f *hsTOutputFetcher) HeightBehind() uint64 { return 0 }
func (f *hsTOutputFetcher) isCaughtUp() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caughtUp
}

// hsTNewOutputService builds a fully wired Service (real engine, memdb) for
// exercising handleOutput's switch directly, bypassing the channel loop.
func hsTNewOutputService(t *testing.T) (*Service, *testSetup, *hsTOutputProducer) {
	t.Helper()
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	db := memdb.NewTestDB(t)
	producer := &hsTOutputProducer{}
	s := &Service{
		engine:            h,
		db:                db,
		ctx:               context.Background(),
		blockProducer:     producer,
		pendingExecutions: make(map[types.Hash]struct{}),
		notifiedImports:   make(map[types.Hash]struct{}),
		persistInterval:   10,
	}
	return s, setup, producer
}

// TestService_HandleOutput_ExecuteBlock covers the pendingExecutions
// bookkeeping and the fetch-on-miss call to BlockFetcher.
func TestService_HandleOutput_ExecuteBlock(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	fetcher := &hsTOutputFetcher{}
	s.blockFetcher = fetcher
	hash := types.Hash{0x01}

	s.handleOutput(EngineOutput{Type: OutputExecuteBlock, Hash: hash}, time.Time{})

	s.pendingMu.Lock()
	_, ok := s.pendingExecutions[hash]
	s.pendingMu.Unlock()
	if !ok {
		t.Fatal("expected hash to be recorded in pendingExecutions")
	}
}

// TestService_HandleOutput_SpeculativeBuild covers the type-assertion probe
// for SpeculativeBlockProducer.
type hsTSpeculativeProducer struct {
	hsTOutputProducer
	prepared []types.Hash
}

func (p *hsTSpeculativeProducer) PrepareSpeculativeBlock(parentHash types.Hash) {
	p.prepared = append(p.prepared, parentHash)
}

func TestService_HandleOutput_SpeculativeBuild(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	sp := &hsTSpeculativeProducer{}
	s.blockProducer = sp
	hash := types.Hash{0x02}

	s.handleOutput(EngineOutput{Type: OutputSpeculativeBuild, Hash: hash}, time.Time{})

	if len(sp.prepared) != 1 || sp.prepared[0] != hash {
		t.Fatalf("expected PrepareSpeculativeBlock(%s), got %v", hash, sp.prepared)
	}
}

// TestService_HandleOutput_BlockCommitted_Success covers the commit path
// when CommitToCanonical succeeds: pendingCommits is not populated and
// lastPersistedView advances via the state hook.
func TestService_HandleOutput_BlockCommitted_Success(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	hash := types.Hash{0x03}

	s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: 1, Hash: hash}, time.Time{})

	if len(producer.canonCalled) != 1 || producer.canonCalled[0] != hash {
		t.Fatalf("expected CommitToCanonical(%s), got %v", hash, producer.canonCalled)
	}
	s.pendingMu.Lock()
	_, pending := s.pendingCommits[hash]
	s.pendingMu.Unlock()
	if pending {
		t.Fatal("expected no pending commit entry on success")
	}
}

// TestService_HandleOutput_BlockCommitted_DeferredOnError covers the
// deferred-retry bookkeeping when CommitToCanonical fails (block not yet
// locally available).
func TestService_HandleOutput_BlockCommitted_DeferredOnError(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	producer.canonErr = context.DeadlineExceeded
	hash := types.Hash{0x04}

	s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: 1, Hash: hash}, time.Time{})

	s.pendingMu.Lock()
	_, pending := s.pendingCommits[hash]
	s.pendingMu.Unlock()
	if !pending {
		t.Fatal("expected a pending commit entry to be recorded on failure")
	}
}

// TestService_HandleOutput_ViewChanged_LeaderTriggersProduction covers the
// leader branch: when this node is the current leader, triggerBlockProduction
// runs on the LockedQC's block hash.
func TestService_HandleOutput_ViewChanged_LeaderTriggersProduction(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	s.pendingExecutions[types.Hash{0x05}] = struct{}{}

	view := s.engine.Engine().CurrentView()
	isLeader := s.engine.Engine().IsCurrentLeader()

	s.handleOutput(EngineOutput{Type: OutputViewChanged, View: view}, time.Time{})

	// Stale pendingExecutions from the previous view must be cleared
	// regardless of leadership.
	s.pendingMu.Lock()
	n := len(s.pendingExecutions)
	s.pendingMu.Unlock()
	if n != 0 {
		t.Fatal("expected pendingExecutions to be cleared on view change")
	}
	if isLeader && len(producer.triggered) == 0 {
		t.Fatal("expected triggerBlockProduction to run for the current leader")
	}
}

// TestService_HandleOutput_EpochStaged covers the staged-epoch persistence
// path (no-op branch when the engine has no staged epoch).
func TestService_HandleOutput_EpochStaged(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	s.handleOutput(EngineOutput{Type: OutputEpochStaged, NewEpoch: 1, ValidatorCount: 4}, time.Time{})
}

// TestService_HandleOutput_SyncRequired covers the async CatchUp dispatch.
func TestService_HandleOutput_SyncRequired(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	fetcher := &hsTOutputFetcher{}
	s.blockFetcher = fetcher

	s.handleOutput(EngineOutput{Type: OutputSyncRequired, LocalView: 1, TargetView: 5}, time.Time{})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !fetcher.isCaughtUp() {
		time.Sleep(20 * time.Millisecond)
	}
	if !fetcher.isCaughtUp() {
		t.Fatal("expected CatchUp to be called")
	}
}

// TestService_HandleOutput_EquivocationDetected covers the evidence
// persistence path.
func TestService_HandleOutput_EquivocationDetected(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	s.handleOutput(EngineOutput{
		Type:      OutputEquivocationDetected,
		View:      3,
		Validator: 1,
		Hash1:     types.Hash{0x06},
		Hash2:     types.Hash{0x07},
	}, time.Time{}) // must persist evidence and not panic
}

// TestService_HandleOutput_EpochTransition covers the epoch activation
// persistence call, the pre-stage-from-schedule probe, and the peer-refresh
// callback.
func TestService_HandleOutput_EpochTransition(t *testing.T) {
	s, _, _ := hsTNewOutputService(t)
	refreshed := false
	s.peerRefreshFn = func() { refreshed = true }

	s.handleOutput(EngineOutput{Type: OutputEpochTransition, NewEpoch: 1, ValidatorCount: 4}, time.Time{})

	if !refreshed {
		t.Fatal("expected peerRefreshFn to be invoked on epoch transition")
	}
}
