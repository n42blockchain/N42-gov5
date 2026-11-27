// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/params"
)

func vtfProfile1Config() *params.ChainConfig {
	return &params.ChainConfig{ChainID: big.NewInt(1), N42Profile: params.N42Profile1}
}

// vtfService wraps engine e in a profile-1 Service without starting it.
func vtfService(t *testing.T, e *ConsensusEngine) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Service{engine: &HotStuff{chainConfig: vtfProfile1Config(), engine: e}, ctx: ctx, cancel: cancel}
}

func vtfGossip(t *testing.T, msg *ConsensusMsg) []byte {
	t.Helper()
	raw, err := EncodeConsensusMsg(msg)
	if err != nil {
		t.Fatal(err)
	}
	return vtfSnappy(t, raw)
}

func vtfSnappy(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := (&encoder.SszNetworkEncoder{}).EncodeGossip(&buf, &rawSSZMarshaler{data: raw}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func vtfPubsubMsg(data []byte, local bool) *pubsub.Message {
	return &pubsub.Message{Message: &pb.Message{Data: data}, Local: local}
}

func vtfVote(setup *testSetup, e *ConsensusEngine, view ViewNumber, voter ValidatorIndex, hash types.Hash) *ConsensusMsg {
	sig := setup.keys[voter].Sign(e.voteSigningMessage(view, hash)).Marshal()
	return &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: view, BlockHash: hash, Voter: voter, Signature: sig}}
}

func vtfFullQC(setup *testSetup, view ViewNumber) QuorumCertificate {
	return QuorumCertificate{View: view, BlockHash: types.Hash{0x11}, AggregateSignature: make([]byte, 96),
		Signers: make([]bool, setup.vs.Len())}
}

// The field checks are decode-level: the view bound everywhere, and on
// profile 1 the signer index, the signature length and the certificate width.
func TestCheckConsensusMsgFields(t *testing.T) {
	setup := newTestSetup(t, 4)
	sig := make([]byte, 96)
	full := vtfFullQC(setup, 3)
	narrow := full
	narrow.Signers = make([]bool, 3)
	maxQC := full
	maxQC.View = MaxCertView
	cases := []struct {
		name string
		msg  *ConsensusMsg
		p1   bool
		ok   bool
	}{
		{"vote", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 3, Signature: sig}}, true, true},
		{"vote view 2^64-1", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: MaxCertView, Voter: 1, Signature: sig}}, false, false},
		{"vote voter outside set", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 4, Signature: sig}}, true, false},
		{"vote voter outside set, legacy", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 4, Signature: sig}}, false, true},
		{"vote short signature", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 1, Signature: sig[:95]}}, true, false},
		{"commit vote empty signature", &ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{View: 4, Voter: 1}}, true, false},
		{"vote piggybacked TC of any width", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 1, Signature: sig,
			HighTC: &TimeoutCertificate{View: 3, Signers: make([]bool, 1)}}}, true, true},
		{"vote piggybacked TC 2^64-1", &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 4, Voter: 1, Signature: sig,
			HighTC: &TimeoutCertificate{View: MaxCertView}}}, true, false},
		{"proposal genesis justify", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 1, Proposer: 0, Signature: sig,
			JustifyQC: GenesisQC()}}, true, true},
		{"proposal justify", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 4, Proposer: 0, Signature: sig,
			JustifyQC: full}}, true, true},
		{"proposal justify narrow", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 4, Proposer: 0, Signature: sig,
			JustifyQC: narrow}}, true, false},
		{"proposal justify 2^64-1", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 4, Proposer: 0, Signature: sig,
			JustifyQC: maxQC}}, false, false},
		{"proposal prepare QC narrow", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 4, Proposer: 0, Signature: sig,
			JustifyQC: full, PrepareQC: &narrow}}, true, false},
		{"prepare QC msg", &ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{View: 3, QC: full}}, true, true},
		{"timeout high QC any width", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{View: 4, Sender: 2, Signature: sig,
			HighQC: narrow}}, true, true},
		{"timeout high QC 2^64-1", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{View: 4, Sender: 2, Signature: sig,
			HighQC: maxQC}}, true, false},
		{"new view", &ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{View: 5, Leader: 1, Signature: sig,
			TimeoutCert: TimeoutCertificate{View: 4, Signers: make([]bool, 4), HighQC: full}}}, true, true},
		{"new view TC narrow", &ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{View: 5, Leader: 1, Signature: sig,
			TimeoutCert: TimeoutCertificate{View: 4, Signers: make([]bool, 2), HighQC: full}}}, true, false},
		{"decide", &ConsensusMsg{Type: MsgDecide, Payload: &Decide{View: 3, CommitQC: full}}, true, true},
		{"decide 2^64-1", &ConsensusMsg{Type: MsgDecide, Payload: &Decide{View: MaxCertView, CommitQC: full}}, true, false},
	}
	for _, c := range cases {
		var p1 *ValidatorSet
		if c.p1 {
			p1 = setup.vs
		}
		err := checkConsensusMsgFields(c.msg, p1)
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// AM36 on the consensus topic: malformed -> Reject, out of range -> Reject,
// well formed -> Accept with the decoded message handed to the handler.
func TestConsensusTopicValidatorRejectsAndAccepts(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	e.SetProfile1(setup.vs)
	s := vtfService(t, e)
	late := make(chan lateGossip, 4)
	val := s.consensusTopicValidator(&encoder.SszNetworkEncoder{}, late)
	ctx := context.Background()
	view := e.CurrentView()

	if r := val(ctx, "p", vtfPubsubMsg([]byte{0xde, 0xad, 0xbe, 0xef}, false)); r != pubsub.ValidationReject {
		t.Fatalf("garbage: %v, want reject", r)
	}
	if r := val(ctx, "p", vtfPubsubMsg(vtfSnappy(t, []byte{0x09, 0x00}), false)); r != pubsub.ValidationReject {
		t.Fatalf("undecodable envelope: %v, want reject", r)
	}
	maxView := vtfVote(setup, e, view, 1, types.Hash{0x22})
	maxView.Payload.(*Vote).View = MaxCertView
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, maxView), false)); r != pubsub.ValidationReject {
		t.Fatalf("view 2^64-1: %v, want reject", r)
	}
	outside := vtfVote(setup, e, view, 1, types.Hash{0x22})
	outside.Payload.(*Vote).Voter = 9
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, outside), false)); r != pubsub.ValidationReject {
		t.Fatalf("voter outside the set: %v, want reject", r)
	}

	good := vtfVote(setup, e, view, 1, types.Hash{0x22})
	m := vtfPubsubMsg(vtfGossip(t, good), false)
	if r := val(ctx, "p", m); r != pubsub.ValidationAccept {
		t.Fatalf("valid vote: %v, want accept", r)
	}
	dg, ok := m.ValidatorData.(*decodedGossip)
	if !ok || dg.msg == nil || dg.msg.Type != MsgVote {
		t.Fatalf("validator data %#v, want the decoded vote", m.ValidatorData)
	}
	// A bad signature is not a decode-level failure: the handler verifies it.
	bad := vtfVote(setup, e, view, 1, types.Hash{0x22})
	bad.Payload.(*Vote).Signature = setup.keys[2].Sign([]byte("other")).Marshal()
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, bad), false)); r != pubsub.ValidationAccept {
		t.Fatalf("well-formed vote with a wrong signature: %v, want accept (BLS stays in the handler)", r)
	}
	// Future views are forwarded: the engine buffers or view-jumps on them.
	future := vtfVote(setup, e, view+FutureViewWindow+5, 1, types.Hash{0x22})
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, future), false)); r != pubsub.ValidationAccept {
		t.Fatalf("future vote: %v, want accept", r)
	}
	if len(late) != 0 {
		t.Fatalf("%d messages queued as late, want 0", len(late))
	}
}

// Ignore cases: no engine yet, and stale messages. A stale message inside the
// late window is queued for the engine; beyond it, dropped. Decide and
// NewView are handled at any view and so accepted; a message this node
// published itself is never judged by view.
func TestConsensusTopicValidatorIgnoresStale(t *testing.T) {
	setup := newTestSetup(t, 4)
	ctx := context.Background()
	enc := &encoder.SszNetworkEncoder{}

	noEngine := &Service{engine: &HotStuff{chainConfig: vtfProfile1Config()}}
	val := noEngine.consensusTopicValidator(enc, make(chan lateGossip, 1))
	e0, _ := newTestEngine(t, setup, 0)
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, vtfVote(setup, e0, 1, 1, types.Hash{1})), false)); r != pubsub.ValidationIgnore {
		t.Fatalf("no engine: %v, want ignore", r)
	}

	e, _ := newTestEngine(t, setup, 0)
	e.SetProfile1(setup.vs)
	const current = ViewNumber(20)
	e.mu.Lock()
	if err := e.advanceToView(current); err != nil {
		e.mu.Unlock()
		t.Fatal(err)
	}
	e.mu.Unlock()
	s := vtfService(t, e)
	late := make(chan lateGossip, 4)
	val = s.consensusTopicValidator(enc, late)

	recent := vtfVote(setup, e, current-2, 1, types.Hash{2})
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, recent), false)); r != pubsub.ValidationIgnore {
		t.Fatalf("late vote: %v, want ignore", r)
	}
	if len(late) != 1 {
		t.Fatalf("late vote queued %d times, want 1", len(late))
	}
	<-late
	old := vtfVote(setup, e, current-lateVoteWindow-1, 1, types.Hash{2})
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, old), false)); r != pubsub.ValidationIgnore {
		t.Fatalf("vote beyond the late window: %v, want ignore", r)
	}
	if len(late) != 0 {
		t.Fatal("vote beyond the late window queued")
	}
	full := vtfFullQC(setup, 3)
	decide := &ConsensusMsg{Type: MsgDecide, Payload: &Decide{View: 3, BlockHash: full.BlockHash, CommitQC: full}}
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, decide), false)); r != pubsub.ValidationAccept {
		t.Fatalf("stale Decide: %v, want accept", r)
	}
	if r := val(ctx, "p", vtfPubsubMsg(vtfGossip(t, recent), true)); r != pubsub.ValidationAccept {
		t.Fatalf("own stale vote: %v, want accept", r)
	}
}

// AM34 item 6 survives the Ignore: a conflicting commit vote that arrives
// after the leader left its view is withheld from forwarding and from the
// subscription, but still reaches the late-vote check and is proven.
func TestConsensusTopicValidatorLateVoteStillProven(t *testing.T) {
	setup, e, out, _ := lateCommitLeader(t)
	e.SetProfile1(setup.vs)
	s := vtfService(t, e)
	late := make(chan lateGossip, 4)
	val := s.consensusTopicValidator(&encoder.SszNetworkEncoder{}, late)
	s.wg.Add(1)
	go s.lateGossipLoop(late)
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })

	ev := lateCommitEvent(e, setup, 1, 0, lateHashB)
	data := vtfGossip(t, &ev.Msg)
	if r := val(context.Background(), "p", vtfPubsubMsg(data, false)); r != pubsub.ValidationIgnore {
		t.Fatalf("late conflicting commit vote: %v, want ignore", r)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := len(out)
		e.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := commitEvidence(t, out); len(got) != 1 {
		t.Fatalf("got %d evidence outputs, want 1", len(got))
	}
}

// vtfP2P is hsTP2P plus the pubsub router, so the Service registers its
// topic validator on it as it does on the node's p2p service.
type vtfP2P struct{ *hsTP2P }

func (p vtfP2P) PubSub() *pubsub.PubSub { return p.ps }

// End to end over gossipsub on a line a - b - c: b runs the profile-1
// service. A malformed message from a is neither delivered on b nor
// forwarded to c; a valid vote is forwarded to c and handled on b once.
func TestConsensusTopicValidateThenForward(t *testing.T) {
	setup := newTestSetup(t, 4)
	mn := mocknet.New()
	t.Cleanup(func() { _ = mn.Close() })
	var hosts [3]*hsTP2P
	for i := range hosts {
		h, err := mn.GenPeer()
		if err != nil {
			t.Fatal(err)
		}
		hosts[i] = newHsTP2P(t, h)
	}
	if err := mn.LinkAll(); err != nil {
		t.Fatal(err)
	}
	a, b, c := hosts[0], hosts[1], hosts[2]
	for _, pair := range [][2]*hsTP2P{{a, b}, {b, c}} {
		if _, err := mn.ConnectPeers(pair[0].h.ID(), pair[1].h.ID()); err != nil {
			t.Fatal(err)
		}
	}

	const gossipTopic = "/n42/hotstuff/gossip"
	view := ViewNumber(1)
	leader := int(LeaderForView(view, setup.vs))
	s := hsTNewWiredService(t, setup, leader, vtfP2P{b}, gossipTopic, "/n42/hotstuff/rpc")
	s.engine.chainConfig = vtfProfile1Config()
	eng := s.engine.Engine()
	eng.SetProfile1(setup.vs)
	s.wg.Add(1)
	go s.subscribeMessages()
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })

	fullTopic := gossipTopic + s.engineEncodingSuffix()
	csub, err := c.SubscribeToTopic(fullTopic)
	if err != nil {
		t.Fatal(err)
	}
	atopic, err := a.joinTopic(fullTopic)
	if err != nil {
		t.Fatal(err)
	}
	hsTWaitForTopicPeer(t, atopic)
	ctopic, _ := c.joinTopic(fullTopic)
	hsTWaitForTopicPeer(t, ctopic)
	// b forwards along its mesh, which gossipsub builds on its heartbeat.
	time.Sleep(1500 * time.Millisecond)

	block := types.Hash{0x51, 0x82}
	if err := eng.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: block}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.PublishToTopic(ctx, fullTopic, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	voter := ValidatorIndex((leader + 1) % 4)
	good := vtfGossip(t, vtfVote(setup, eng, view, voter, block))
	if err := a.PublishToTopic(ctx, fullTopic, good); err != nil {
		t.Fatal(err)
	}

	got, err := csub.Next(ctx)
	if err != nil {
		t.Fatalf("c did not receive the valid vote: %v", err)
	}
	if !bytes.Equal(got.Data, good) {
		t.Fatalf("c received %x first, want the valid vote (the malformed message was forwarded)", got.Data)
	}
	if got.ReceivedFrom != peer.ID(b.h.ID()) {
		t.Fatalf("c received from %s, want b", got.ReceivedFrom)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	if extra, err := csub.Next(short); err == nil {
		t.Fatalf("c received a second message %x", extra.Data)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		eng.mu.Lock()
		received := eng.voteStats.received
		eng.mu.Unlock()
		if received >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	eng.mu.Lock()
	received := eng.voteStats.received
	eng.mu.Unlock()
	if received != 1 {
		t.Fatalf("b's engine received %d votes, want 1", received)
	}
	if n := s.decodeFailures.Load(); n != 1 {
		t.Fatalf("decode failures %d, want 1 (the malformed message, once)", n)
	}
}

// AM36 on the H2-v4 Decide topic: malformed or non-Decide -> Reject; another
// chain's Decide -> Ignore; a well-formed Decide -> Accept with the decoded
// envelope handed over, which the handler verifies and commits once.
func TestDecideTopicValidator(t *testing.T) {
	setup := newTestSetup(t, 4)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	chain := &followerChain{missing: map[types.Hash]bool{}}
	f, err := NewDecideFollower(newFollowerTestEngine(t, setup), chain, noopFollowerP2P{}, identity)
	if err != nil {
		t.Fatal(err)
	}
	val := f.decideTopicValidator()
	ctx := context.Background()

	if r := val(ctx, "p", vtfPubsubMsg([]byte{0x01, 0x02}, false)); r != pubsub.ValidationReject {
		t.Fatalf("garbage: %v, want reject", r)
	}
	foreign := H2V4ChainIdentity{ChainID: 95, GenesisHash: identity.GenesisHash}
	if r := val(ctx, "p", vtfPubsubMsg(followerDecideGossip(t, setup, foreign, 4, repeatedHash(0x03), []int{0, 1, 2}), false)); r != pubsub.ValidationIgnore {
		t.Fatalf("another chain's Decide: %v, want ignore", r)
	}
	vote := vtfVote(setup, &ConsensusEngine{}, 4, 1, repeatedHash(0x03))
	notDecide, err := EncodeH2V4Gossip(H2V4Envelope{Identity: identity, Message: vote})
	if err != nil {
		t.Fatal(err)
	}
	if r := val(ctx, "p", vtfPubsubMsg(notDecide, false)); r != pubsub.ValidationReject {
		t.Fatalf("vote on the Decide topic: %v, want reject", r)
	}
	full := vtfFullQC(setup, 4)
	mismatch, err := EncodeH2V4Gossip(H2V4Envelope{Identity: identity,
		Message: &ConsensusMsg{Type: MsgDecide, Payload: &Decide{View: 5, BlockHash: full.BlockHash, CommitQC: full}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := val(ctx, "p", vtfPubsubMsg(mismatch, false)); r != pubsub.ValidationReject {
		t.Fatalf("Decide not matching its CommitQC: %v, want reject", r)
	}

	b1 := repeatedHash(0x01)
	m := vtfPubsubMsg(followerDecideGossip(t, setup, identity, 2, b1, []int{0, 1, 2}), false)
	if r := val(ctx, "p", m); r != pubsub.ValidationAccept {
		t.Fatalf("valid Decide: %v, want accept", r)
	}
	env, ok := m.ValidatorData.(*H2V4Envelope)
	if !ok || env == nil {
		t.Fatalf("validator data %#v, want the decoded envelope", m.ValidatorData)
	}
	if err := f.handleEnvelope(env); err != nil {
		t.Fatal(err)
	}
	if len(chain.committed) != 1 || chain.committed[0] != b1 {
		t.Fatalf("commits %v, want b1 once", chain.committed)
	}
}
