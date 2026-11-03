package hotstuff

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

type retryTestFetcher struct {
	applied atomic.Bool
	behind  atomic.Uint64
}

func (*retryTestFetcher) FetchBlockByHash(types.Hash) {}
func (*retryTestFetcher) CatchUp()                    {}
func (f *retryTestFetcher) HeightBehind() uint64      { return f.behind.Load() }
func (f *retryTestFetcher) BlockApplied(types.Hash, uint64) bool {
	return f.applied.Load()
}

type retryTestProducer struct{ parents chan types.Hash }

func (p *retryTestProducer) TriggerBlockProduction(parent types.Hash) { p.parents <- parent }
func (*retryTestProducer) CommitToCanonical(types.Hash) error         { return nil }

func newProductionRetryFixture(t *testing.T) (*parentAppliedFixture, *ConsensusEngine, *retryTestFetcher, *retryTestProducer) {
	t.Helper()
	f := newParentAppliedFixture(t)
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.roundState.AdvanceView(4) // validator 0 leads view 4
	e.roundState.UpdateLockedQC(&QuorumCertificate{View: 3, BlockHash: f.locked})
	fetcher := new(retryTestFetcher)
	producer := &retryTestProducer{parents: make(chan types.Hash, 8)}
	s := NewService(&HotStuff{engine: e}, nil, f.svc.db, "test", "test")
	s.SetBlockFetcher(fetcher)
	s.SetBlockProducer(producer)
	f.svc = s
	f.setApplied(t, 14527897, f.committed)
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })
	return f, e, fetcher, producer
}

// Reproduce the fleet's ordering: execution notification, new leader deferred
// while persistence is in flight, then the deduplicated persisted import. No
// new view/timeout is needed to resume production, and execution alone cannot
// release the gate.
func TestLeaderRetriesAfterDeduplicatedPersistedImport(t *testing.T) {
	f, _, fetcher, producer := newProductionRetryFixture(t)
	s := f.svc
	s.NotifyBlockExecuted(f.locked, types.Hash{}, f.committed, 14527898, nil)
	s.triggerBlockProduction(4, f.locked)
	if len(producer.parents) != 0 || s.pendingProduction == nil {
		t.Fatal("unpersisted parent must defer production")
	}
	s.NotifyBlockImported(f.locked, types.Hash{})
	if len(s.productionRetry) != 0 {
		t.Fatal("unapplied notification woke production")
	}
	s.wg.Add(1)
	go s.processOutputs()
	f.setApplied(t, 14527898, f.locked)
	fetcher.applied.Store(true)
	s.NotifyBlockImported(f.locked, types.Hash{})
	select {
	case parent := <-producer.parents:
		if parent != f.locked {
			t.Fatal("retry used a different parent")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("persisted import did not resume the same leader view")
	}
	for i := 0; i < 20; i++ {
		s.NotifyBlockImported(f.locked, types.Hash{})
	}
	select {
	case <-producer.parents:
		t.Fatal("duplicate imports triggered production twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProductionRetryRejectsStaleAuthority(t *testing.T) {
	for _, change := range []string{"view", "lock", "leader", "removed", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			f, e, fetcher, producer := newProductionRetryFixture(t)
			s := f.svc
			s.triggerBlockProduction(4, f.locked)
			f.setApplied(t, 14527898, f.locked)
			fetcher.applied.Store(true)
			switch change {
			case "view":
				e.roundState.AdvanceView(5)
			case "lock":
				e.roundState.UpdateLockedQC(&QuorumCertificate{View: 4, BlockHash: f.sibling})
			case "leader":
				e.myIndex = 1
			case "removed":
				e.removed = true
			case "cancelled":
				s.cancel()
			}
			s.retryBlockProduction()
			if len(producer.parents) != 0 || s.pendingProduction != nil {
				t.Fatal("stale production authority survived retry")
			}
		})
	}
}

func TestProductionRetryRepeatsSyncGates(t *testing.T) {
	f, _, fetcher, producer := newProductionRetryFixture(t)
	s := f.svc
	s.triggerBlockProduction(4, f.locked)
	// Even a spurious wakeup cannot bypass the parent marker.
	s.retryBlockProduction()
	if len(producer.parents) != 0 || s.pendingProduction == nil {
		t.Fatal("retry bypassed parent persistence")
	}
	f.setApplied(t, 14527898, f.locked)
	fetcher.applied.Store(true)
	fetcher.behind.Store(blockProductionSyncGate + 1)
	s.retryBlockProduction()
	if len(producer.parents) != 0 || s.pendingProduction == nil {
		t.Fatal("retry bypassed height synchronization")
	}
	fetcher.behind.Store(0)
	s.retryBlockProduction()
	s.retryBlockProduction()
	if len(producer.parents) != 1 || s.pendingProduction != nil {
		t.Fatal("ready retry must trigger exactly once")
	}
}
