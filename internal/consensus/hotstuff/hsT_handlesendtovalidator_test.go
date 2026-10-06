// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TestService_HandleSendToValidator_NoRotorFallsBackToGossip covers the
// simplest path: rotor disabled/nil, so the message goes straight to the
// gossip fallback via handleBroadcast.
func TestService_HandleSendToValidator_NoRotorFallsBackToGossip(t *testing.T) {
	p0, _ := hsTMocknetPair(t)
	s := &Service{
		p2p:         p0,
		ctx:         context.Background(),
		gossipTopic: "/n42/hotstuff/gossip",
		rotor:       nil,
	}
	s.handleSendToValidator(EngineOutput{
		Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1}},
		Target:  1,
	}, time.Time{})
}

// TestService_HandleSendToValidator_DirectDeliverySucceeds covers the Rotor
// direct-send success path end to end over a real mocknet stream: the
// message is delivered via SendRawBytes, RecordVoteDirect fires, and the
// gossip fallback still runs afterward (always-also-gossip policy).
func TestService_HandleSendToValidator_DirectDeliverySucceeds(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, p1 := hsTMocknetPair(t)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}

	const rpcTopic = "/n42/hotstuff/rpc"
	received := make(chan struct{}, 1)
	p1.SetStreamHandler(rpcTopic, func(data []byte, from peer.ID) {
		select {
		case received <- struct{}{}:
		default:
		}
	})

	rotor := NewRotor(3)
	rotor.SetEnabled(true)
	rotor.RegisterValidator(setup.validators[1].Address, p1.h.ID())

	s := &Service{
		engine:      h,
		p2p:         p0,
		ctx:         context.Background(),
		gossipTopic: "/n42/hotstuff/gossip",
		rpcTopic:    rpcTopic,
		rotor:       rotor,
	}

	s.handleSendToValidator(EngineOutput{
		Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: h.Engine().CurrentView()}},
		Target:  1,
	}, time.Time{})

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("direct stream delivery was not observed")
	}

	direct, _ := rotor.VoteStats()
	if direct != 1 {
		t.Fatalf("VoteStats direct = %d, want 1", direct)
	}
}
