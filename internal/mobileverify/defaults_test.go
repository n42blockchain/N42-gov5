package mobileverify

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestNewCertStoreDefaultsNonPositiveMax(t *testing.T) {
	s := NewCertStore(0)
	if s.max != 512 {
		t.Fatalf("max = %d, want default 512 for a non-positive input", s.max)
	}
	s = NewCertStore(-5)
	if s.max != 512 {
		t.Fatalf("max = %d, want default 512 for a negative input", s.max)
	}
}

func TestNewWindowManagerDefaultsNonPositiveWindow(t *testing.T) {
	wm := NewWindowManager(NewRegistry(), nil, 0, NewCertStore(10))
	if wm.window != 45*time.Second {
		t.Fatalf("window = %v, want default 45s for a non-positive input", wm.window)
	}
	wm2 := NewWindowManager(NewRegistry(), nil, -time.Second, NewCertStore(10))
	if wm2.window != 45*time.Second {
		t.Fatalf("window = %v, want default 45s for a negative input", wm2.window)
	}
}

func TestNewPacketCacheDefaultsZeroWindow(t *testing.T) {
	c := NewPacketCache(0)
	if c.window != 256 {
		t.Fatalf("window = %d, want default 256 for a zero input", c.window)
	}
}

func TestCollectorIndicesSortedAscending(t *testing.T) {
	c := NewCollector(NewRegistry(), types.Hash{}, 1)
	c.byIndex[MobileIndex(3)] = nil
	c.byIndex[MobileIndex(1)] = nil
	c.byIndex[MobileIndex(2)] = nil

	got := c.Indices()
	want := []MobileIndex{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("Indices() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Indices() = %v, want %v", got, want)
		}
	}
}

func TestCohortCoordinatorSetBanThresholdFn(t *testing.T) {
	c := &CohortCoordinator{}
	called := false
	c.SetBanThresholdFn(func() int {
		called = true
		return 3
	})
	if c.banThresholdFn == nil {
		t.Fatal("SetBanThresholdFn did not install the threshold function")
	}
	if got := c.banThresholdFn(); got != 3 || !called {
		t.Fatalf("installed fn() = %d (called=%v), want 3 (called=true)", got, called)
	}
}
