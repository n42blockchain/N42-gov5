package hotstuff

import (
	"sync/atomic"
	"testing"
)

// quorumGateFetcher offers the authenticated gate probe next to the legacy
// highest-peer HeightBehind, which here reports a liar's absurd gap.
type quorumGateFetcher struct {
	retryTestFetcher
	quorumArg atomic.Int64
	quorumGap atomic.Uint64
}

func (f *quorumGateFetcher) HeightBehindQuorum(q int) uint64 {
	f.quorumArg.Store(int64(q))
	return f.quorumGap.Load()
}

// The leader gate must use the f+1 quorum probe, never the single highest
// advertised height (sync review S0a / C2).
func TestHeightBehindUsesQuorumProbe(t *testing.T) {
	setup := newTestSetup(t, 7)
	e, _ := newTestEngine(t, setup, 0)
	s := NewService(&HotStuff{engine: e}, nil, nil, "test", "test")
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })
	f := new(quorumGateFetcher)
	f.behind.Store(1_000_000) // what one lying peer made the old gate report
	s.SetBlockFetcher(f)

	if got := s.heightBehind(); got != 0 {
		t.Fatalf("heightBehind() = %d, want 0 from the quorum probe", got)
	}
	if q := f.quorumArg.Load(); q != 3 {
		t.Fatalf("quorum argument = %d, want f+1 = 3 for 7 validators", q)
	}
	f.quorumGap.Store(40)
	if got := s.heightBehind(); got != 40 {
		t.Fatalf("heightBehind() = %d, want the proven gap 40", got)
	}
}

// A fetcher without the probe keeps the old signal.
func TestHeightBehindFallsBackWithoutQuorumProbe(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	s := NewService(&HotStuff{engine: e}, nil, nil, "test", "test")
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })
	f := new(retryTestFetcher)
	f.behind.Store(9)
	s.SetBlockFetcher(f)
	if got := s.heightBehind(); got != 9 {
		t.Fatalf("heightBehind() = %d, want 9", got)
	}
}
