// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// TestService_LeaderKeepsGossipVoteBurst100 is gate 5a over a real
// gossipsub transport: the leader of a 100-member set has proposed, and the
// other 99 members' Round 1 votes arrive on the consensus topic in one burst
// while the leader's engine is busy (here: its lock is held for the length
// of the burst, as a vote batch verification or an import event holds it in
// the field). go-libp2p-pubsub drops a message when the subscription queue
// is full; with the library's 32-message default the leader lost most of
// the burst and never formed a PrepareQC. Every vote must reach the engine.
func TestService_LeaderKeepsGossipVoteBurst100(t *testing.T) {
	if testing.Short() {
		t.Skip("100-member gossip burst")
	}
	const n = 100
	setup := newTestSetup(t, n)
	view := ViewNumber(1)
	leader := int(LeaderForView(view, setup.vs))
	p0, p1 := hsTMocknetPair(t)

	const gossipTopic = "/n42/hotstuff/gossip"
	s := hsTNewWiredService(t, setup, leader, p0, gossipTopic, "/n42/hotstuff/rpc")
	s.wg.Add(1)
	go s.subscribeMessages()
	t.Cleanup(func() { s.cancel(); s.wg.Wait() })

	fullTopic := gossipTopic + s.engineEncodingSuffix()
	topic, err := p1.joinTopic(fullTopic)
	if err != nil {
		t.Fatal(err)
	}
	hsTWaitForTopicPeer(t, topic)

	eng := s.engine.Engine()
	if eng.CurrentView() != view {
		t.Fatalf("engine at view %d, want %d", eng.CurrentView(), view)
	}
	block := types.Hash{0x51, 0x82}
	if err := eng.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: block}); err != nil {
		t.Fatal(err)
	}

	enc := &encoder.SszNetworkEncoder{}
	msgs := make([][]byte, 0, n-1)
	for i := 0; i < n; i++ {
		if i == leader {
			continue
		}
		v := &Vote{View: view, BlockHash: block, Voter: ValidatorIndex(i),
			Signature: setup.keys[i].Sign(eng.voteSigningMessage(view, block)).Marshal()}
		raw, err := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: v})
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := enc.EncodeGossip(&buf, &rawSSZMarshaler{data: raw}); err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, buf.Bytes())
	}

	// Busy leader: hold the engine lock while the whole burst is delivered,
	// then release it and let the subscription loop drain.
	eng.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, m := range msgs {
		if err := p1.PublishToTopic(ctx, fullTopic, m); err != nil {
			eng.mu.Unlock()
			t.Fatal(err)
		}
	}
	time.Sleep(500 * time.Millisecond) // the burst is in flight / queued
	eng.mu.Unlock()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		eng.mu.Lock()
		received, formed := eng.voteStats.received, eng.prepareQC != nil
		eng.mu.Unlock()
		if formed && received == n-1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	t.Fatalf("leader received %d of %d gossip votes, PrepareQC formed %v (quorum %d)",
		eng.voteStats.received, n-1, eng.prepareQC != nil, setup.vs.QuorumSize())
}
