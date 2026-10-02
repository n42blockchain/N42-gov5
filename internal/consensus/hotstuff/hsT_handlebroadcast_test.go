// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestService_HandleBroadcast_NilGuards covers the early-return guards: a nil
// message and a nil P2P transport.
func TestService_HandleBroadcast_NilGuards(t *testing.T) {
	s := &Service{}
	s.handleBroadcast(EngineOutput{}, time.Time{}) // nil Message

	p0, _ := hsTMocknetPair(t)
	_ = p0
	s2 := &Service{p2p: nil}
	s2.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{}}}, time.Time{})
}

// TestService_HandleBroadcast_PlainGossip covers the ordinary gossip publish
// path for a non-Proposal, non-Timeout message over a real mocknet
// transport.
func TestService_HandleBroadcast_PlainGossip(t *testing.T) {
	p0, _ := hsTMocknetPair(t)
	s := &Service{
		p2p:         p0,
		ctx:         context.Background(),
		gossipTopic: "/n42/hotstuff/gossip",
		rotor:       NewRotor(3),
	}
	s.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1}}}, time.Time{})
}

// TestService_HandleBroadcast_DecideTriggersH2V4Publish covers the Decide +
// h2V4Identity branch, which spawns publishH2V4Decide.
func TestService_HandleBroadcast_DecideTriggersH2V4Publish(t *testing.T) {
	p0, _ := hsTMocknetPair(t)
	identity := H2V4ChainIdentity{ChainID: 1}
	s := &Service{
		p2p:          p0,
		ctx:          context.Background(),
		gossipTopic:  "/n42/hotstuff/gossip",
		rotor:        NewRotor(3),
		h2V4Identity: &identity,
	}
	decide := &Decide{View: 1, BlockHash: types.Hash{0x01}, CommitQC: GenesisQC()}
	s.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgDecide, Payload: decide}}, time.Time{})
	// publishH2V4Decide runs on its own goroutine; give it a moment to finish
	// so coverage/race runs don't race past process exit.
	time.Sleep(50 * time.Millisecond)
}

// TestService_HandleBroadcast_TimeoutNoQuorumGateSkipsReadiness covers the
// MsgTimeout branch's quorumSize<=1 fast path (timeoutPublishPeerTarget
// returns 0, so no WithReadiness wait is installed and the publish proceeds
// immediately over the real mocknet transport).
func TestService_HandleBroadcast_TimeoutNoQuorumGateSkipsReadiness(t *testing.T) {
	setup := newTestSetup(t, 1)
	p0, _ := hsTMocknetPair(t)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		engine:      h,
		p2p:         p0,
		ctx:         context.Background(),
		gossipTopic: "/n42/hotstuff/gossip",
		rotor:       NewRotor(3),
	}

	done := make(chan struct{})
	go func() {
		s.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{View: 1, HighQC: GenesisQC()}}}, time.Time{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handleBroadcast(MsgTimeout) did not return in time")
	}
}

// TestNotifyBlockChecked_NilEngineIsNoop and friends cover the nil-engine
// guard branches of the simple Notify* passthroughs, independent of the
// engine-present cases already covered elsewhere.
func TestNotifyBlockChecked_NilEngineIsNoop(t *testing.T) {
	h := New(nil, nil) // InitEngine never called: Engine() returns nil
	s := &Service{engine: h}
	s.NotifyBlockChecked(types.Hash{0x01}, types.Hash{0x02}) // must not panic
}

func TestNotifyBlockHeaderKnown_NilEngineIsNoop(t *testing.T) {
	h := New(nil, nil)
	s := &Service{engine: h}
	s.NotifyBlockHeaderKnown(types.Hash{0x01}, types.Hash{0x02}, 5) // must not panic
}

func TestNotifyBlockExecuted_NilEngineIsNoop(t *testing.T) {
	h := New(nil, nil)
	s := &Service{
		engine:          h,
		notifiedImports: make(map[types.Hash]struct{}),
	}
	s.NotifyBlockExecuted(types.Hash{0x01}, types.Hash{0x02}, types.Hash{0x03}, 1, nil) // must not panic
}

// TestPqcPhase covers both branches of the pure rxStamps.pqcPhase derivation.
func TestPqcPhase(t *testing.T) {
	var r rxStamps
	if got := r.pqcPhase(); got.OK {
		t.Fatalf("pqcPhase() on an unmeasured rxStamps = %+v, want OK=false", got)
	}
	r.pqcOK = true
	r.pqcRx2Arr = 10 * time.Millisecond
	r.pqcRxAtMs = 123
	r.pqcVia = "gossip"
	got := r.pqcPhase()
	if !got.OK || got.RxAtMs != 123 || got.Via != "gossip" || got.Rx2Arr.D != 10*time.Millisecond {
		t.Fatalf("pqcPhase() = %+v, unexpected", got)
	}
}

// TestValidatorSetForView_EpochsDisabledReturnsCurrentSet covers the
// epochLength==0 fast path.
func TestValidatorSetForView_EpochsDisabledReturnsCurrentSet(t *testing.T) {
	setup := newTestSetup(t, 4)
	em := NewEpochManager(setup.vs)
	if got := em.ValidatorSetForView(10); got != setup.vs {
		t.Fatalf("ValidatorSetForView with epochs disabled = %v, want the static set", got)
	}
}
