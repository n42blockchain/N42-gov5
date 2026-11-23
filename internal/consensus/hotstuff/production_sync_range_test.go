package hotstuff

import (
	"sync/atomic"
	"testing"
	"time"
)

// rangeModeFetcher adds the S5 sync-mode probe to the retry fetcher.
type rangeModeFetcher struct {
	retryTestFetcher
	inRange atomic.Bool
	probes  atomic.Int64
}

func (f *rangeModeFetcher) SyncProductionPaused() bool {
	f.probes.Add(1)
	return f.inRange.Load()
}

// A leader whose sync layer is in Range mode does not produce, even with an
// applied parent and no height gap; back in Live it produces on the same
// parent.
func TestLeaderDoesNotProduceInSyncRangeMode(t *testing.T) {
	f, _, _, producer := newProductionRetryFixture(t)
	s := f.svc
	fetcher := new(rangeModeFetcher)
	fetcher.applied.Store(true)
	s.SetBlockFetcher(fetcher)
	f.setApplied(t, 14527898, f.locked)

	fetcher.inRange.Store(true)
	s.triggerBlockProduction(4, f.locked)
	select {
	case <-producer.parents:
		t.Fatal("produced a block in Range mode")
	case <-time.After(100 * time.Millisecond):
	}
	if fetcher.probes.Load() == 0 {
		t.Fatal("the leader gate never asked the sync layer")
	}

	fetcher.inRange.Store(false)
	s.triggerBlockProduction(4, f.locked)
	select {
	case parent := <-producer.parents:
		if parent != f.locked {
			t.Fatal("produced on a different parent")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no production after leaving Range mode")
	}
}

// A fetcher without the probe never pauses production (pipeline off).
func TestSyncProductionPausedWithoutProbe(t *testing.T) {
	f, _, _, _ := newProductionRetryFixture(t)
	if f.svc.syncProductionPaused() {
		t.Fatal("a fetcher without the probe paused production")
	}
}
