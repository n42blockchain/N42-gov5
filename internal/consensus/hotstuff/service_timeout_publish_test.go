// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sync"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// subscribedFakeP2P records how handleBroadcast publishes. Its
// subscriber-gated publish models a topic with n-1 subscribers and a
// GossipSub mesh of 8: it is ready as soon as minSubscribers <= n-1, which is
// what p2p.Service's subscriber count does. A mesh-sized readiness check
// (pubsub.MinTopicSize) would never pass for minSubscribers > 8.
type subscribedFakeP2P struct {
	subscribers int

	mu           sync.Mutex
	plainOpts    []int // len(opts) of each PublishToTopic call
	gatedTargets []int
	gatedWaits   []time.Duration
}

func (f *subscribedFakeP2P) PublishToTopic(_ context.Context, _ string, _ []byte, opts ...pubsub.PubOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plainOpts = append(f.plainOpts, len(opts))
	return nil
}

func (f *subscribedFakeP2P) SubscribeToTopic(string, ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, nil
}

func (f *subscribedFakeP2P) Encoding() encoder.NetworkEncoding { return &encoder.SszNetworkEncoder{} }

func (f *subscribedFakeP2P) PublishToTopicWhenSubscribed(ctx context.Context, _ string, _ []byte, minSubscribers int, maxWait time.Duration) error {
	f.mu.Lock()
	f.gatedTargets = append(f.gatedTargets, minSubscribers)
	f.gatedWaits = append(f.gatedWaits, maxWait)
	f.mu.Unlock()
	if f.subscribers >= minSubscribers {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(maxWait):
		return nil
	}
}

// TestService_HandleBroadcast_TimeoutLargeValidatorSetPublishes is the n=20 /
// n=100 regression for the timeout-gossip halt: quorum-1 is far above the
// GossipSub mesh size, and the timeout must still be published promptly via
// the subscriber-gated path, with no mesh-based readiness option.
func TestService_HandleBroadcast_TimeoutLargeValidatorSetPublishes(t *testing.T) {
	for _, n := range []int{20, 100} {
		setup := newTestSetup(t, n)
		h := New(nil, nil)
		h.Authorize(setup.validators[0].Address, setup.keys[0])
		if err := h.InitEngine(setup.validators, setup.f); err != nil {
			t.Fatal(err)
		}
		fake := &subscribedFakeP2P{subscribers: n - 1}
		s := &Service{
			engine:      h,
			p2p:         fake,
			ctx:         context.Background(),
			gossipTopic: "/n42/hotstuff/gossip",
			rotor:       NewRotor(3),
		}
		want := timeoutPublishPeerTarget(setup.vs.QuorumSize())
		if want <= 8 {
			t.Fatalf("n=%d: target %d does not exceed the mesh size; test is vacuous", n, want)
		}

		done := make(chan struct{})
		go func() {
			s.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{View: 1, HighQC: GenesisQC()}}}, time.Time{})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("n=%d: handleBroadcast(MsgTimeout) blocked", n)
		}

		fake.mu.Lock()
		if len(fake.gatedTargets) != 1 || fake.gatedTargets[0] != want {
			t.Errorf("n=%d: gated publish targets = %v, want [%d]", n, fake.gatedTargets, want)
		}
		if len(fake.gatedWaits) == 1 && fake.gatedWaits[0] != timeoutPublishMaxWait {
			t.Errorf("n=%d: gated wait = %v, want %v", n, fake.gatedWaits[0], timeoutPublishMaxWait)
		}
		if len(fake.plainOpts) != 0 {
			t.Errorf("n=%d: timeout also went through PublishToTopic (opts %v)", n, fake.plainOpts)
		}
		fake.mu.Unlock()
	}
}

// TestService_HandleBroadcast_VoteSkipsSubscriberGate: only timeouts wait for
// subscribers; other broadcasts publish plainly with no readiness option.
func TestService_HandleBroadcast_VoteSkipsSubscriberGate(t *testing.T) {
	fake := &subscribedFakeP2P{}
	s := &Service{
		p2p:         fake,
		ctx:         context.Background(),
		gossipTopic: "/n42/hotstuff/gossip",
		rotor:       NewRotor(3),
	}
	s.handleBroadcast(EngineOutput{Message: &ConsensusMsg{Type: MsgVote, Payload: &Vote{View: 1}}}, time.Time{})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.gatedTargets) != 0 || len(fake.plainOpts) != 1 || fake.plainOpts[0] != 0 {
		t.Fatalf("gated %v plain %v, want one plain publish with no options", fake.gatedTargets, fake.plainOpts)
	}
}
