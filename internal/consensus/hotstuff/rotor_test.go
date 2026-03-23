// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/types"
)

// newTestValidatorSet creates a validator set with n dummy validators.
func newTestValidatorSet(n int) *ValidatorSet {
	validators := make([]ValidatorInfo, n)
	for i := 0; i < n; i++ {
		var addr types.Address
		addr[0] = byte(i + 1)
		validators[i] = ValidatorInfo{Address: addr}
	}
	f := uint32((n - 1) / 3)
	return NewValidatorSet(validators, f)
}

// mockDirectSender records direct sends for testing.
type mockDirectSender struct {
	sends atomic.Int64
}

func (m *mockDirectSender) SendRawDirect(_ context.Context, _ []byte, _ string, _ peer.ID) error {
	m.sends.Add(1)
	return nil
}

func TestRotor_SelectRelays_Basic(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)
	leader := ValidatorIndex(0)

	assignments := rotor.SelectRelays(1, vs, leader)
	if len(assignments) != 3 {
		t.Fatalf("expected 3 relay assignments, got %d", len(assignments))
	}

	for _, a := range assignments {
		if a.RelayIndex == leader {
			t.Errorf("leader %d should not be a relay", leader)
		}
	}

	// All non-leader validators should be covered.
	covered := make(map[ValidatorIndex]bool)
	for _, a := range assignments {
		for _, target := range a.Targets {
			covered[target] = true
		}
	}
	for i := 0; i < int(vs.Len()); i++ {
		idx := ValidatorIndex(i)
		if idx == leader {
			continue
		}
		if !covered[idx] {
			t.Errorf("validator %d not assigned to any relay", idx)
		}
	}
}

func TestRotor_SelectRelays_Deterministic(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)

	a1 := rotor.SelectRelays(42, vs, 0)
	a2 := rotor.SelectRelays(42, vs, 0)

	if len(a1) != len(a2) {
		t.Fatal("non-deterministic relay count")
	}
	for i := range a1 {
		if a1[i].RelayIndex != a2[i].RelayIndex {
			t.Fatalf("relay %d differs: %d vs %d", i, a1[i].RelayIndex, a2[i].RelayIndex)
		}
	}
}

func TestRotor_SelectRelays_DifferentViews(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)

	a1 := rotor.SelectRelays(1, vs, 0)
	a2 := rotor.SelectRelays(2, vs, 0)

	same := true
	for i := range a1 {
		if a1[i].RelayIndex != a2[i].RelayIndex {
			same = false
			break
		}
	}
	if same {
		t.Log("Warning: views 1 and 2 produced identical relay sets (unlikely but possible)")
	}
}

func TestRotor_SelectRelays_SmallSet(t *testing.T) {
	rotor := NewRotor(3)

	vs1 := newTestValidatorSet(1)
	if a := rotor.SelectRelays(1, vs1, 0); a != nil {
		t.Errorf("expected nil for single validator, got %d assignments", len(a))
	}

	vs2 := newTestValidatorSet(2)
	a := rotor.SelectRelays(1, vs2, 0)
	if len(a) != 1 {
		t.Fatalf("expected 1 relay for 2 validators, got %d", len(a))
	}
}

func TestRotor_SelectRelays_LargeSet(t *testing.T) {
	rotor := NewRotor(5)
	vs := newTestValidatorSet(100)
	leader := ValidatorIndex(50)

	assignments := rotor.SelectRelays(999, vs, leader)
	if len(assignments) != 5 {
		t.Fatalf("expected 5 relays, got %d", len(assignments))
	}

	covered := make(map[ValidatorIndex]bool)
	for _, a := range assignments {
		for _, target := range a.Targets {
			covered[target] = true
		}
	}
	if len(covered) != 99 {
		t.Errorf("expected 99 validators covered, got %d", len(covered))
	}
}

func TestRotor_IsRelay(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)

	assignments := rotor.SelectRelays(1, vs, 0)
	for _, a := range assignments {
		if !rotor.IsRelay(1, vs, 0, a.RelayIndex) {
			t.Errorf("validator %d should be relay for view 1", a.RelayIndex)
		}
	}

	if rotor.IsRelay(1, vs, 0, 0) {
		t.Error("leader should not be a relay")
	}
}

func TestRotor_PeerRegistry(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)

	addr, _ := vs.GetAddress(1)
	testPeer := peer.ID("test-peer-1")

	// Initially no peer registered.
	if _, ok := rotor.LookupPeer(vs, 1); ok {
		t.Error("should not find unregistered peer")
	}

	// Register and lookup.
	rotor.RegisterValidator(addr, testPeer)
	if rotor.RegisteredCount() != 1 {
		t.Errorf("expected 1 registered, got %d", rotor.RegisteredCount())
	}

	pid, ok := rotor.LookupPeer(vs, 1)
	if !ok || pid != testPeer {
		t.Errorf("expected peer %s, got %s (ok=%v)", testPeer, pid, ok)
	}

	// Unregister.
	rotor.UnregisterValidator(addr)
	if rotor.RegisteredCount() != 0 {
		t.Error("expected 0 registered after unregister")
	}
}

func TestRotor_BroadcastViaRelays_DirectSend(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)

	// Register peers for all non-leader validators.
	for i := 1; i < int(vs.Len()); i++ {
		addr, _ := vs.GetAddress(ValidatorIndex(i))
		rotor.RegisterValidator(addr, peer.ID("peer-"+string(rune('A'+i))))
	}

	sender := &mockDirectSender{}
	var gossipCalled bool
	gossipFn := func(data []byte) error {
		gossipCalled = true
		return nil
	}

	err := rotor.BroadcastViaRelays(
		context.Background(), 1, vs, 0,
		sender, "/rpc/hotstuff_direct/1", gossipFn,
		[]byte("test-proposal"),
	)
	if err != nil {
		t.Fatalf("broadcast failed: %v", err)
	}

	// Should have sent directly to 3 relay nodes.
	if sender.sends.Load() != 3 {
		t.Errorf("expected 3 direct sends, got %d", sender.sends.Load())
	}

	// Gossip should also be called as safety net.
	if !gossipCalled {
		t.Error("gossip fallback should be called as safety net")
	}
}

func TestRotor_BroadcastViaRelays_NoPeers_FallsBack(t *testing.T) {
	rotor := NewRotor(3)
	vs := newTestValidatorSet(7)
	// No peers registered.

	sender := &mockDirectSender{}
	var gossipCalled bool
	gossipFn := func(data []byte) error {
		gossipCalled = true
		return nil
	}

	err := rotor.BroadcastViaRelays(
		context.Background(), 1, vs, 0,
		sender, "/rpc/test", gossipFn,
		[]byte("test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if sender.sends.Load() != 0 {
		t.Error("should not have sent directly when no peers registered")
	}
	if !gossipCalled {
		t.Error("should fall back to gossip when no peers registered")
	}
}

func TestRotor_ForwardToTargets(t *testing.T) {
	rotor := NewRotor(2)
	vs := newTestValidatorSet(6)

	// Register all peers.
	for i := 0; i < int(vs.Len()); i++ {
		addr, _ := vs.GetAddress(ValidatorIndex(i))
		rotor.RegisterValidator(addr, peer.ID("peer-"+string(rune('A'+i))))
	}

	sender := &mockDirectSender{}

	// Find a relay for view 1.
	assignments := rotor.SelectRelays(1, vs, 0)
	if len(assignments) == 0 {
		t.Fatal("no relay assignments")
	}

	relay := assignments[0]
	rotor.ForwardToTargets(
		context.Background(), 1, vs, 0, relay.RelayIndex,
		sender, "/rpc/test", []byte("relayed-data"),
	)

	// Should have forwarded to targets (excluding self).
	expectedForwards := len(relay.Targets) - 1 // minus self
	if int(sender.sends.Load()) != expectedForwards {
		t.Errorf("expected %d forwards, got %d", expectedForwards, sender.sends.Load())
	}
}

func TestRotor_Disabled(t *testing.T) {
	rotor := NewRotor(3)
	rotor.SetEnabled(false)

	if rotor.Enabled() {
		t.Error("rotor should be disabled")
	}

	vs := newTestValidatorSet(7)
	var gossipCalled bool
	gossipFn := func(data []byte) error {
		gossipCalled = true
		return nil
	}

	err := rotor.BroadcastViaRelays(
		context.Background(), 1, vs, 0,
		nil, "", gossipFn, []byte("test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !gossipCalled {
		t.Error("disabled rotor should use gossip fallback")
	}
}

func TestRotor_Stats(t *testing.T) {
	rotor := NewRotor(3)
	ds, rl, fb := rotor.Stats()
	if ds != 0 || rl != 0 || fb != 0 {
		t.Error("initial stats should be zero")
	}
}
