package hotstuff

import (
	"testing"
)

// TestService_SimpleSetters covers the small field-assignment setters that
// wire optional collaborators (block producer, fetcher, slashing executor,
// epoch schedule, peer refresh callback, H2V4 identity) into the Service.
// None of these have observable getters, so the test just confirms the
// stored fields match what was set.
func TestService_SimpleSetters(t *testing.T) {
	s := &Service{}

	s.SetSlashingExecutor(&SlashingExecutor{})
	if s.slashingExecutor == nil {
		t.Fatalf("SetSlashingExecutor: not stored")
	}

	sched := &EpochSchedule{}
	s.SetEpochSchedule(sched)
	if s.epochSchedule != sched {
		t.Fatalf("SetEpochSchedule: not stored")
	}

	called := false
	s.SetPeerRefreshFn(func() { called = true })
	if s.peerRefreshFn == nil {
		t.Fatalf("SetPeerRefreshFn: not stored")
	}
	s.peerRefreshFn()
	if !called {
		t.Fatalf("expected the stored callback to be the one passed in")
	}

	identity := H2V4ChainIdentity{ChainID: 42}
	s.SetH2V4Identity(identity)
	if s.h2V4Identity == nil || s.h2V4Identity.ChainID != 42 {
		t.Fatalf("SetH2V4Identity: not stored correctly, got %+v", s.h2V4Identity)
	}
}

// TestNewService covers the constructor's field wiring and default
// persistInterval.
func TestNewService(t *testing.T) {
	h := New(nil, nil)
	s := NewService(h, nil, nil, "gossip-topic", "rpc-topic")
	if s.engine != h {
		t.Fatalf("expected engine to be the passed-in HotStuff")
	}
	if s.gossipTopic != "gossip-topic" || s.rpcTopic != "rpc-topic" {
		t.Fatalf("unexpected topics: gossip=%q rpc=%q", s.gossipTopic, s.rpcTopic)
	}
	if s.persistInterval != 10 {
		t.Fatalf("expected default persistInterval=10, got %d", s.persistInterval)
	}
	if s.rotor == nil {
		t.Fatalf("expected a non-nil rotor")
	}
	if s.pendingExecutions == nil || s.notifiedImports == nil {
		t.Fatalf("expected initialized tracking maps")
	}
}
