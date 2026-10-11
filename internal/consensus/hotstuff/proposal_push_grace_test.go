// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

// graceTestP2P records when a message reached the publisher.
type graceTestP2P struct {
	published chan time.Time
}

func (p *graceTestP2P) PublishToTopic(context.Context, string, []byte, ...pubsub.PubOpt) error {
	p.published <- time.Now()
	return nil
}

func (p *graceTestP2P) SubscribeToTopic(string, ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, nil
}

func (p *graceTestP2P) Encoding() encoder.NetworkEncoding { return &encoder.SszNetworkEncoder{} }

// proposalPublishLatency hands a Proposal broadcast to the output handler and
// returns the time until it reaches the publisher.
func proposalPublishLatency(t testing.TB, s *Service, p2p *graceTestP2P) time.Duration {
	t.Helper()
	msg := &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{View: 7, BlockHash: types.Hash{0x07}}}
	start := time.Now()
	s.handleOutput(EngineOutput{Type: OutputBroadcast, Message: msg, Hash: types.Hash{0x07}}, time.Time{})
	select {
	case at := <-p2p.published:
		return at.Sub(start)
	case <-time.After(5 * time.Second):
		t.Fatal("proposal never published")
		return 0
	}
}

// TestProposalPushGrace: under vote on availability the leader publishes its
// Proposal as soon as the output loop hands it over; without it the legacy
// 50 ms pause stays. Reports the medians of both (the A/B of the change).
func TestProposalPushGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p2p := &graceTestP2P{published: make(chan time.Time, 1)}
	s := &Service{p2p: p2p, ctx: ctx, gossipTopic: "hs"}

	median := func(on bool) time.Duration {
		restore := block.SetVoteOnAvailabilityForTest(on)
		defer restore()
		var ds []time.Duration
		for i := 0; i < 5; i++ {
			ds = append(ds, proposalPublishLatency(t, s, p2p))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	legacy := median(false)
	avail := median(true)
	t.Logf("proposal emit -> publish median: legacy %v, availability %v", legacy, avail)
	if legacy < 40*time.Millisecond {
		t.Fatalf("legacy mode lost its push grace: %v", legacy)
	}
	if avail > 20*time.Millisecond {
		t.Fatalf("availability mode still pauses the proposal: %v", avail)
	}
	for _, c := range []struct {
		on   bool
		want time.Duration
	}{{false, 0}, {true, 100 * time.Millisecond}} {
		restore := block.SetVoteOnAvailabilityForTest(c.on)
		g := fetchOnMissGrace()
		restore()
		if g != c.want {
			t.Fatalf("fetch-on-miss grace (availability %v) = %v, want %v", c.on, g, c.want)
		}
	}
}

// countingFetcher counts fetch-on-miss requests.
type countingFetcher struct {
	hsTNotifyFetcher
	fetches atomic.Int32
}

func (f *countingFetcher) FetchBlockByHash(types.Hash) { f.fetches.Add(1) }

// TestScheduleFetchOnMiss: the deferred fetch fires for a block still missing
// after the grace and is dropped for one that arrived (no longer pending, or
// held available) in the meantime.
func TestScheduleFetchOnMiss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &countingFetcher{}
	missing, arrived, held := types.Hash{0x01}, types.Hash{0x02}, types.Hash{0x03}
	s := &Service{
		ctx:               ctx,
		blockFetcher:      f,
		pendingExecutions: map[types.Hash]struct{}{missing: {}, arrived: {}, held: {}},
	}
	s.availableProbe = func(h types.Hash) bool { return h == held }

	s.scheduleFetchOnMiss(missing, 5*time.Millisecond)
	s.scheduleFetchOnMiss(arrived, 5*time.Millisecond)
	s.scheduleFetchOnMiss(held, 5*time.Millisecond)
	s.pendingMu.Lock()
	delete(s.pendingExecutions, arrived) // imported before the grace ran out
	s.pendingMu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for f.fetches.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := f.fetches.Load(); got != 1 {
		t.Fatalf("fetches = %d, want 1 (only the block still missing)", got)
	}
}
