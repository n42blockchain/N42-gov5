package hotstuff

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

type fakeBlockFetcher struct {
	mu          sync.Mutex
	fetchedHash types.Hash
	fetched     bool
}

func (f *fakeBlockFetcher) FetchBlockByHash(hash types.Hash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = true
	f.fetchedHash = hash
}
func (f *fakeBlockFetcher) CatchUp()             {}
func (f *fakeBlockFetcher) HeightBehind() uint64 { return 0 }

type fakeSpeculativeProducer struct {
	mu       sync.Mutex
	prepared bool
	parent   types.Hash
}

func (f *fakeSpeculativeProducer) PrepareSpeculativeBlock(parentHash types.Hash) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = true
	f.parent = parentHash
}

func (f *fakeSpeculativeProducer) TriggerBlockProduction(types.Hash)  {}
func (f *fakeSpeculativeProducer) CommitToCanonical(types.Hash) error { return nil }

// TestHandleOutput_ExecuteBlock covers the OutputExecuteBlock branch: the
// hash is tracked as a pending execution and, when a fetcher is wired, a
// fetch-on-miss is triggered.
func TestHandleOutput_ExecuteBlock(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	fetcher := &fakeBlockFetcher{}
	s := &Service{
		engine:            h,
		blockFetcher:      fetcher,
		pendingExecutions: make(map[types.Hash]struct{}),
	}

	hash := types.Hash{0x11}
	s.handleOutput(EngineOutput{Type: OutputExecuteBlock, Hash: hash}, time.Time{})

	s.pendingMu.Lock()
	_, tracked := s.pendingExecutions[hash]
	s.pendingMu.Unlock()
	if !tracked {
		t.Fatalf("expected hash tracked in pendingExecutions")
	}
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if !fetcher.fetched || fetcher.fetchedHash != hash {
		t.Fatalf("expected FetchBlockByHash called with %x, got fetched=%v hash=%x", hash, fetcher.fetched, fetcher.fetchedHash)
	}
}

// TestHandleOutput_SpeculativeBuild covers the OutputSpeculativeBuild branch
// when the block producer implements SpeculativeBlockProducer.
func TestHandleOutput_SpeculativeBuild(t *testing.T) {
	prod := &fakeSpeculativeProducer{}
	s := &Service{blockProducer: prod, pendingExecutions: make(map[types.Hash]struct{})}

	hash := types.Hash{0x22}
	s.handleOutput(EngineOutput{Type: OutputSpeculativeBuild, Hash: hash}, time.Time{})

	prod.mu.Lock()
	defer prod.mu.Unlock()
	if !prod.prepared || prod.parent != hash {
		t.Fatalf("expected PrepareSpeculativeBlock called with %x, got prepared=%v parent=%x", hash, prod.prepared, prod.parent)
	}
}

// TestHandleOutput_SpeculativeBuild_NoProducer covers the type-assertion
// failure branch (no producer, or one that does not implement the optional
// interface) being a silent no-op.
func TestHandleOutput_SpeculativeBuild_NoProducer(t *testing.T) {
	s := &Service{pendingExecutions: make(map[types.Hash]struct{})}
	s.handleOutput(EngineOutput{Type: OutputSpeculativeBuild, Hash: types.Hash{0x33}}, time.Time{})
}
