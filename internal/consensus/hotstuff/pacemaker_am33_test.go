// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"
)

// TestBackoffGroupSize pins AM33's g = min(f+1, 3), f = floor((n-1)/3).
func TestBackoffGroupSize(t *testing.T) {
	for _, c := range []struct {
		n    int
		want uint32
	}{{-1, 1}, {0, 1}, {1, 1}, {3, 1}, {4, 2}, {6, 2}, {7, 3}, {10, 3}, {100, 3}, {512, 3}} {
		if got := BackoffGroupSize(c.n); got != c.want {
			t.Errorf("n=%d: g=%d, want %d", c.n, got, c.want)
		}
	}
}

// TestPacemakerAM33Backoff: the timeout doubles once per g consecutive
// timeouts, capped at max (AM33).
func TestPacemakerAM33Backoff(t *testing.T) {
	s := time.Second
	for _, c := range []struct {
		n    int
		want []time.Duration // c = 0, 1, 2, ...
	}{
		{0, []time.Duration{24 * s, 48 * s, 96 * s, 96 * s}}, // unset: g = 1
		{1, []time.Duration{24 * s, 48 * s, 96 * s, 96 * s}},
		{4, []time.Duration{24 * s, 24 * s, 48 * s, 48 * s, 96 * s, 96 * s, 96 * s}},
		{7, []time.Duration{24 * s, 24 * s, 24 * s, 48 * s, 48 * s, 48 * s, 96 * s, 96 * s}},
		{100, []time.Duration{24 * s, 24 * s, 24 * s, 48 * s, 48 * s, 48 * s, 96 * s}},
	} {
		p := NewPacemaker(24000, 96000)
		if c.n != 0 {
			p.SetCommitteeSize(c.n)
		}
		for k, want := range c.want {
			if got := p.TimeoutDuration(uint32(k)); got != want {
				t.Errorf("n=%d c=%d: %v, want %v", c.n, k, got, want)
			}
		}
		if got := p.TimeoutDuration(^uint32(0)); got != 96*s {
			t.Errorf("n=%d c=max: %v, want the cap", c.n, got)
		}
	}
}

// TestPacemakerAM33KeepsAdaptiveBase: the group applies to the adaptive base
// max(base, 2*p95) unchanged.
func TestPacemakerAM33KeepsAdaptiveBase(t *testing.T) {
	p := NewPacemaker(1000, 60000)
	p.SetCommitteeSize(4)
	for i := 0; i < 10; i++ {
		p.ObserveCommitLatency(3 * time.Second)
	}
	for k, want := range []time.Duration{6 * time.Second, 6 * time.Second, 12 * time.Second, 12 * time.Second, 24 * time.Second} {
		if got := p.TimeoutDuration(uint32(k)); got != want {
			t.Errorf("c=%d: %v, want %v", k, got, want)
		}
	}
}

// TestEngineSetsPacemakerCommittee: the engine hands its validator-set size
// to the pacemaker, so its timer uses g = 3 at n = 7.
func TestEngineSetsPacemakerCommittee(t *testing.T) {
	setup := newTestSetup(t, 7)
	engine, _ := newTestEngine(t, setup, 0)
	pm := engine.Pacemaker()
	if a, b := pm.TimeoutDuration(0), pm.TimeoutDuration(2); a != b {
		t.Fatalf("n=7: c=2 gives %v, c=0 %v; want equal (g=3)", b, a)
	}
	if a, b := pm.TimeoutDuration(0), pm.TimeoutDuration(3); b != 2*a {
		t.Fatalf("n=7: c=3 gives %v, want 2x %v", b, a)
	}
}
