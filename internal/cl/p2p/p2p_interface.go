// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/p2p. Import paths rewritten to N42's in-repo
// equivalents (internal/p2p/discover etc.) per the #34 caplin-merge-plan.

//go:build n42el

package p2p

import (
	"github.com/n42blockchain/N42/internal/p2p/discover"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/metrics"
)

type P2PManager interface {
	Pubsub() *pubsub.PubSub
	Host() host.Host
	BandwidthCounter() *metrics.BandwidthCounter
	UDPv5Listener() *discover.UDPv5
	UpdateENRAttSubnets(subnetIndex int, on bool)
	UpdateENRSyncNets(subnetIndex int, on bool)
}
