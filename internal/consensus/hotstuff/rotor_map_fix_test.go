package hotstuff

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func directSendService(t *testing.T, setup *testSetup) (*Service, *Rotor) {
	t.Helper()
	p0, p1 := hsTMocknetPair(t)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	const rpcTopic = "/n42/hotstuff/rpc"
	p1.SetStreamHandler(rpcTopic, func([]byte, peer.ID) {})
	rotor := NewRotor(3)
	rotor.RegisterValidator(setup.validators[1].Address, p1.h.ID())
	return &Service{engine: h, p2p: p0, ctx: context.Background(),
		gossipTopic: "/n42/hotstuff/gossip", rpcTopic: rpcTopic, rotor: rotor}, rotor
}

// A successful direct send keeps the target's mapping under the fix, so the
// commit vote after a prepare vote goes direct too; without it the mapping
// is dropped.
func TestRotorMapFixKeepsMappingAfterDirectSend(t *testing.T) {
	for _, fix := range []bool{false, true} {
		restore := SetRotorMapFixForTest(fix)
		setup := newTestSetup(t, 4)
		s, rotor := directSendService(t, setup)
		view := s.engine.Engine().CurrentView()
		s.handleSendToValidator(EngineOutput{Type: OutputSendToValidator, Target: 1,
			Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: view}}}, time.Time{})
		if direct, _ := rotor.VoteStats(); direct != 1 {
			t.Fatalf("fix=%v: direct sends %d, want 1", fix, direct)
		}
		if _, ok := rotor.LookupPeer(setup.vs, 1); ok != fix {
			t.Fatalf("fix=%v: mapping kept after a delivered send = %v", fix, ok)
		}
		s.handleSendToValidator(EngineOutput{Type: OutputSendToValidator, Target: 1,
			Message: &ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{View: view}}}, time.Time{})
		want := int64(1)
		if fix {
			want = 2
		}
		if direct, _ := rotor.VoteStats(); direct != want {
			t.Fatalf("fix=%v: direct sends after the commit vote %d, want %d", fix, direct, want)
		}
		restore()
	}
}

// A failed direct send still drops the mapping under the fix.
func TestRotorMapFixDropsMappingAfterFailedSend(t *testing.T) {
	defer SetRotorMapFixForTest(true)()
	setup := newTestSetup(t, 4)
	s, rotor := directSendService(t, setup)
	rotor.RegisterValidator(setup.validators[2].Address, peer.ID("not-a-connected-peer"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.ctx = ctx
	s.handleSendToValidator(EngineOutput{Type: OutputSendToValidator, Target: 2,
		Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1}}}, time.Time{})
	if _, ok := rotor.LookupPeer(setup.vs, 2); ok {
		t.Fatal("mapping kept after a failed send")
	}
}

// Under the fix a Proposal forwarded on a Rotor stream does not map its
// proposer to the relay; the same Proposal by gossip still teaches the
// mapping, as does any other message on a Rotor stream.
func TestRotorMapFixSkipsRelayedProposal(t *testing.T) {
	for _, fix := range []bool{false, true} {
		restore := SetRotorMapFixForTest(fix)
		svc, setup := newRotorWiringService(t, 4)
		relay := peer.ID("relay")
		prop := &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 1000, Proposer: 1}}
		svc.handleConsensusMsg(prop, relay, time.Time{}, time.Time{}, "rotor")
		if _, ok := svc.rotor.LookupPeer(setup.vs, 1); ok == fix {
			t.Fatalf("fix=%v: relayed proposal learned = %v", fix, ok)
		}
		svc.handleConsensusMsg(prop, relay, time.Time{}, time.Time{}, "gossip")
		if _, ok := svc.rotor.LookupPeer(setup.vs, 1); !ok {
			t.Fatalf("fix=%v: gossiped proposal not learned", fix)
		}
		vote := &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1000, Voter: 2}}
		svc.handleConsensusMsg(vote, relay, time.Time{}, time.Time{}, "rotor")
		if _, ok := svc.rotor.LookupPeer(setup.vs, 2); !ok {
			t.Fatalf("fix=%v: vote on a Rotor stream not learned", fix)
		}
		restore()
	}
}
