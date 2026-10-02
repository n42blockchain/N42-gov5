// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/leakybucket"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
)

// newTestService builds a real, minimal Service around an in-process libp2p
// host with no listen addresses — no sockets are opened. It exercises the
// same construction path as NewService (gater, pubsub, peer status) without
// discovery, dialing or persistence, so it is safe to build many of these in
// one test binary run.
func newTestService(t *testing.T) *Service {
	t.Helper()

	cfg := &conf.P2PConfig{
		MaxPeers:     30,
		MinSyncPeers: 0,
		NoDiscovery:  true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := &Service{
		ctx:            ctx,
		cancel:         cancel,
		cfg:            cfg,
		joinedTopics:   make(map[string]*pubsub.Topic),
		genesisHash:    types.BytesToHash([]byte{0x01, 0x02, 0x03}),
		protectedPeers: make(map[peer.ID]struct{}),
	}
	s.isPreGenesis.Store(true)

	var err error
	s.addrFilter, err = configureFilter(cfg)
	if err != nil {
		t.Fatalf("configureFilter: %v", err)
	}
	s.ipLimiter = leakybucket.NewCollector(ipLimit, ipBurst, 30*time.Second, true)

	h, err := libp2p.New(libp2p.NoListenAddrs, libp2p.ConnectionGater(s))
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	s.host = h

	setPubSubParameters()
	gs, err := pubsub.NewGossipSub(ctx, h, s.pubsubOptions()...)
	if err != nil {
		t.Fatalf("pubsub.NewGossipSub: %v", err)
	}
	s.pubsub = gs

	s.peers = peers.NewStatus(ctx, &peers.StatusConfig{
		PeerLimit: cfg.MaxPeers,
		ScorerParams: &scorers.Config{
			BadResponsesScorerConfig: &scorers.BadResponsesScorerConfig{
				Threshold:     maxBadResponses,
				DecayInterval: 10 * time.Minute,
			},
		},
	})

	return s
}
