// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
)

// TestSubscribersReadyCountsSubscribersNotMesh is the n=20 HotStuff case:
// 19 peers subscribed, a GossipSub mesh of 8, timeout target quorum-1 = 13.
// The predicate only sees subscribers, so it must be ready; MinTopicSize(13)
// would compare against the mesh (8) and never be.
func TestSubscribersReadyCountsSubscribersNotMesh(t *testing.T) {
	const (
		subscribed = 19
		mesh       = 8
		target     = 13
	)
	if mesh >= target {
		t.Fatal("test setup: mesh must be below target")
	}
	if !subscribersReady(subscribed, subscribed, target) {
		t.Fatalf("subscribersReady(%d, %d, %d) = false, want true", subscribed, subscribed, target)
	}
	// n=100: 99 subscribed, target 66.
	if !subscribersReady(99, 99, 66) {
		t.Fatal("subscribersReady(99, 99, 66) = false, want true")
	}
}

func TestSubscriberTarget(t *testing.T) {
	tests := []struct {
		target, pubsubPeers, want int
	}{
		{target: 0, pubsubPeers: 5, want: 0},
		{target: -1, pubsubPeers: 5, want: 0},
		{target: 13, pubsubPeers: 19, want: 13},
		{target: 13, pubsubPeers: 5, want: 5}, // capped at connected pubsub peers
		{target: 13, pubsubPeers: 0, want: 1}, // never publish into nobody early
		{target: 2, pubsubPeers: 2, want: 2},
	}
	for _, tt := range tests {
		if got := subscriberTarget(tt.target, tt.pubsubPeers); got != tt.want {
			t.Errorf("subscriberTarget(%d, %d) = %d, want %d", tt.target, tt.pubsubPeers, got, tt.want)
		}
	}
	if subscribersReady(4, 19, 13) {
		t.Error("subscribersReady(4, 19, 13) = true, want false (startup fragment)")
	}
}

func TestWaitForSubscribers(t *testing.T) {
	t.Run("ready once subscribers arrive", func(t *testing.T) {
		calls := 0
		count := func() (int, int) {
			calls++
			if calls < 3 {
				return 4, 19
			}
			return 19, 19
		}
		ready, err := waitForSubscribers(context.Background(), 5*time.Second, 13, count)
		if err != nil || !ready {
			t.Fatalf("waitForSubscribers = (%v, %v), want (true, nil)", ready, err)
		}
	})
	t.Run("deadline returns not ready without error", func(t *testing.T) {
		count := func() (int, int) { return 4, 19 }
		start := time.Now()
		ready, err := waitForSubscribers(context.Background(), 300*time.Millisecond, 13, count)
		if err != nil || ready {
			t.Fatalf("waitForSubscribers = (%v, %v), want (false, nil)", ready, err)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("waitForSubscribers took %v, want about 300ms", el)
		}
	})
	t.Run("context end returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		count := func() (int, int) { return 0, 19 }
		ready, err := waitForSubscribers(ctx, time.Minute, 13, count)
		if ready || !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForSubscribers = (%v, %v), want (false, context.Canceled)", ready, err)
		}
	})
}

// TestPublishToTopicWhenSubscribedAboveMeshSize runs 16 real gossipsub nodes
// on a mocknet. The publisher is subscribed (so it has a mesh, held near
// D=8) and waits for 13 subscribers: the publish must complete and reach a
// peer well before the 20 s bound.
func TestPublishToTopicWhenSubscribedAboveMeshSize(t *testing.T) {
	if testing.Short() {
		t.Skip("16-node mocknet")
	}
	const n = 16
	mn, err := mocknet.FullMeshConnected(n)
	if err != nil {
		t.Fatalf("mocknet.FullMeshConnected: %v", err)
	}
	t.Cleanup(func() { _ = mn.Close() })

	const topic = "/n42/msg/shard/2"
	hosts := mn.Hosts()
	svcs := make([]*Service, n)
	for i, h := range hosts {
		svcs[i] = newTestServiceOnHost(t, h)
	}
	pubSub, err := svcs[0].SubscribeToTopic(topic)
	if err != nil {
		t.Fatalf("SubscribeToTopic(publisher): %v", err)
	}
	defer pubSub.Cancel()
	recv, err := svcs[1].SubscribeToTopic(topic)
	if err != nil {
		t.Fatalf("SubscribeToTopic(1): %v", err)
	}
	defer recv.Cancel()
	for i := 2; i < n; i++ {
		sub, err := svcs[i].SubscribeToTopic(topic)
		if err != nil {
			t.Fatalf("SubscribeToTopic(%d): %v", i, err)
		}
		defer sub.Cancel()
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svcs[0].PublishToTopicWhenSubscribed(ctx, topic, []byte("timeout"), 13, 20*time.Second); err != nil {
		t.Fatalf("PublishToTopicWhenSubscribed: %v", err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("publish waited %v; subscriber readiness should hold within seconds", el)
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	msg, err := recv.Next(rctx)
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	if string(msg.Data) != "timeout" {
		t.Fatalf("receiver got %q", msg.Data)
	}
}
