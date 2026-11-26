// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestProfile1LeaderCountsWireVotes100 is the gate-5 shape in process: a
// profile-1 leader of a 100-member set proposes, then receives Round 1
// votes the way the gossip topic delivers them (encoded, decoded, handed to
// ProcessEvent one by one) from validators 30..98 plus 0..29. It must form
// the PrepareQC as soon as the quorum (67) is in, whatever the order.
func TestProfile1LeaderCountsWireVotes100(t *testing.T) {
	const n = 100
	id := H2V4ChainIdentity{ChainID: 1144, GenesisHash: types.Hash{0x42}}
	net := v6DerivedNet(t, "n42-profile1-vote-intake", n)
	view := ViewNumber(3)
	leader := int(LeaderForViewWithTenure(view, net.vs, v6LeaderTenure))
	e, ch := net.engine(t, id, leader)
	v6AtView(t, e, ch, view)

	block := types.Hash{0x51, 0x82}
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: block}); err != nil {
		t.Fatal(err)
	}
	proposed := false
	for _, o := range drainOutputs(ch) {
		if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
			proposed = true
		}
	}
	if !proposed {
		t.Fatal("leader did not propose")
	}

	pre := H2V4VoteSigningMessage(id, view, block)
	var order []int
	for i := 30; i < 99; i++ { // the topic voters first, as in gate 5
		order = append(order, i)
	}
	for i := 0; i < 30; i++ {
		if i != leader {
			order = append(order, i)
		}
	}
	quorum := net.vs.QuorumSize()
	for k, i := range order {
		v := &Vote{View: view, BlockHash: block, Voter: ValidatorIndex(i), Signature: net.keys[i].Sign(pre).Marshal()}
		wire, err := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: v})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := DecodeConsensusMsg(wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: *msg}); err != nil {
			t.Fatalf("vote %d (voter %d): %v", k, i, err)
		}
		got := false
		for _, o := range drainOutputs(ch) {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgPrepareQC {
				got = true
			}
		}
		// The leader's own vote counts, so quorum-1 remote votes complete it.
		if k+1 < quorum-1 && got {
			t.Fatalf("PrepareQC after %d remote votes, quorum %d", k+1, quorum)
		}
		if k+1 == quorum-1 {
			if !got {
				t.Fatalf("no PrepareQC after %d remote votes + the leader's own (quorum %d); intake %+v",
					k+1, quorum, e.voteStats)
			}
			return
		}
	}
}
