package hotstuff

import (
	"bytes"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

func gossipVoteBytes(t *testing.T, setup *testSetup, view ViewNumber, voter int) []byte {
	t.Helper()
	hash := setup.validators[voter].Address.Hash()
	vote := &Vote{View: view, BlockHash: hash, Voter: ValidatorIndex(voter),
		Signature: setup.keys[voter].Sign(SigningMessage(view, hash)).Marshal()}
	raw, err := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: vote})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := (&encoder.SszNetworkEncoder{}).EncodeGossip(&buf, &rawSSZMarshaler{data: raw}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A gossiped vote does not hold the gossip loop while the engine lock is
// taken: it waits on its own goroutine and is processed once the lock frees.
func TestGossipVoteLaneDoesNotBlockLoop(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, _ := hsTMocknetPair(t)
	s := hsTNewWiredService(t, setup, 0, p0, "/n42/hotstuff/gossip", "/n42/hotstuff/rpc")
	eng := s.engine.Engine()
	data := gossipVoteBytes(t, setup, eng.CurrentView(), 1)
	enc := &encoder.SszNetworkEncoder{}

	eng.mu.Lock()
	returned := make(chan struct{})
	go func() {
		s.dispatchGossipMessage(data, enc, "", time.Time{})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		eng.mu.Unlock()
		t.Fatal("dispatch blocked on the engine lock")
	}
	if len(s.voteLane) != 1 {
		eng.mu.Unlock()
		t.Fatalf("vote lane holds %d, want the waiting vote", len(s.voteLane))
	}
	eng.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for len(s.voteLane) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the vote was not processed after the lock freed")
		}
		time.Sleep(time.Millisecond)
	}
}

// With every slot taken the vote is processed on the loop, as without the
// lane.
func TestGossipVoteLaneFullProcessesInline(t *testing.T) {
	setup := newTestSetup(t, 4)
	p0, _ := hsTMocknetPair(t)
	s := hsTNewWiredService(t, setup, 0, p0, "/n42/hotstuff/gossip", "/n42/hotstuff/rpc")
	eng := s.engine.Engine()
	for i := 0; i < cap(s.voteLane); i++ {
		s.voteLane <- struct{}{}
	}
	data := gossipVoteBytes(t, setup, eng.CurrentView(), 1)

	eng.mu.Lock()
	returned := make(chan struct{})
	go func() {
		s.dispatchGossipMessage(data, &encoder.SszNetworkEncoder{}, "", time.Time{})
		close(returned)
	}()
	select {
	case <-returned:
		eng.mu.Unlock()
		t.Fatal("a vote past the lane's bound did not wait on the loop")
	case <-time.After(100 * time.Millisecond):
	}
	eng.mu.Unlock()
	<-returned
}
