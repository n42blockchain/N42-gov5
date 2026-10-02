// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import "testing"

func TestAverage(t *testing.T) {
	if got := average(nil); got != 0 {
		t.Fatalf("average(nil) = %v, want 0", got)
	}
	if got := average([]float64{}); got != 0 {
		t.Fatalf("average([]) = %v, want 0", got)
	}
	if got := average([]float64{2, 4, 6}); got != 4 {
		t.Fatalf("average([2,4,6]) = %v, want 4", got)
	}
}

func TestAgentFromPid(t *testing.T) {
	s := newTestService(t)
	store := s.Host().Peerstore()
	pid := s.host.ID()

	// No AgentVersion recorded yet.
	if got := agentFromPid(pid, store); got != "unknown" {
		t.Fatalf("agentFromPid (no record) = %q, want unknown", got)
	}

	if err := store.Put(pid, "AgentVersion", "Lighthouse/v4.0.0"); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	if got := agentFromPid(pid, store); got != "lighthouse" {
		t.Fatalf("agentFromPid (lighthouse) = %q, want lighthouse", got)
	}

	if err := store.Put(pid, "AgentVersion", "some-other-client/1.0"); err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	if got := agentFromPid(pid, store); got != "unknown" {
		t.Fatalf("agentFromPid (unrecognized) = %q, want unknown", got)
	}

	if err := store.Put(pid, "AgentVersion", 12345); err != nil {
		t.Fatalf("store.Put (wrong type): %v", err)
	}
	if got := agentFromPid(pid, store); got != "unknown" {
		t.Fatalf("agentFromPid (non-string value) = %q, want unknown", got)
	}
}

func TestUpdateMetricsNoPanicWithNoPeers(t *testing.T) {
	s := newTestService(t)
	// Exercises the full metrics-collection path against an empty peer set.
	s.updateMetrics()
}
