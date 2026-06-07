// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel (import path rewritten to internal/cl/p2p).

//go:build n42el

package sentinel

import (
	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/cl/p2p"
)

type SentinelConfig struct {
	p2p.P2PConfig

	MaxInboundTrafficPerPeer     datasize.ByteSize
	MaxOutboundTrafficPerPeer    datasize.ByteSize
	AdaptableTrafficRequirements bool

	EnableBlocks       bool
	SubscribeAllTopics bool // Capture all topics
	ActiveIndicies     uint64
}
