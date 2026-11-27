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

// TestSyncInfoTCJumpKeepsConsecutiveTimeouts: adopting a TC from SyncInfo
// moves the view but does not reset the consecutive-timeout count (AM33);
// a QC-based jump still does.
func TestSyncInfoTCJumpKeepsConsecutiveTimeouts(t *testing.T) {
	setup := newTestSetup(t, 7)
	engine, _ := newTestEngine(t, setup, 0)
	engine.roundState.Timeout()
	engine.roundState.Timeout()
	if got := engine.ConsecutiveTimeouts(); got != 2 {
		t.Fatalf("setup: %d timeouts", got)
	}
	tc := buildTestTC(t, setup, 20, 0)
	netTimeout := &TimeoutMessage{
		View:      21,
		HighQC:    GenesisQC(),
		Sender:    1,
		Signature: setup.keys[1].Sign(TimeoutSigningMessage(21)).Marshal(),
		HighTC:    tc,
	}
	if err := engine.ProcessEvent(ConsensusEvent{
		Type: EventMessage,
		Msg:  ConsensusMsg{Type: MsgTimeout, Payload: netTimeout},
	}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := engine.CurrentView(); got != 21 {
		t.Fatalf("view %d, want 21 (TC jump)", got)
	}
	if got := engine.ConsecutiveTimeouts(); got != 2 {
		t.Fatalf("TC jump changed consecutive timeouts to %d, want 2 kept", got)
	}
}

// TestRestoreStateArmsBaseTimerKeepsCounter: after a restart c comes back from
// the snapshot, the first view timer is base_eff, and later arms use
// timeout(c) (AM33 clarification (a)).
func TestRestoreStateArmsBaseTimerKeepsCounter(t *testing.T) {
	setup := newTestSetup(t, 4) // n = 4: g = 2; base 1 s, max 10 s
	engine, _ := newTestEngine(t, setup, 0)
	engine.RestoreState(ViewNumber(9), QuorumCertificate{}, QuorumCertificate{}, 4)

	if got := engine.ConsecutiveTimeouts(); got != 4 {
		t.Fatalf("restored c = %d, want 4", got)
	}
	if left := time.Until(engine.pacemaker.Deadline()); left > 1100*time.Millisecond || left <= 0 {
		t.Fatalf("first timer after restart %v, want base_eff 1s (not timeout(4) = 4s)", left)
	}
	engine.resetPacemaker(ViewNumber(10))
	if left := time.Until(engine.pacemaker.Deadline()); left < 3900*time.Millisecond {
		t.Fatalf("later arm %v, want timeout(c=4) = 4s", left)
	}
}
