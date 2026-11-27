// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/params"
)

// G5: on Profile 1 a direct consensus stream is bounded by the consensus
// message cap, not by the (64 MiB) block gossip cap.
func TestHotstuffAdapterProfile1StreamCap(t *testing.T) {
	p1 := &params.ChainConfig{ChainID: big.NewInt(1), N42Profile: params.N42Profile1}
	a := newHotstuffP2PAdapterFor(nil, p1)
	require.Equal(t, int64(hotstuff.MaxConsensusWireMsg), a.streamLimit)

	_, err := readRotorStreamLimit(bytes.NewReader(make([]byte, hotstuff.MaxConsensusWireMsg+1)), a.streamLimit)
	require.Error(t, err)
	data, err := readRotorStreamLimit(bytes.NewReader(make([]byte, hotstuff.MaxConsensusWireMsg)), a.streamLimit)
	require.NoError(t, err)
	require.Len(t, data, hotstuff.MaxConsensusWireMsg)

	legacy := newHotstuffP2PAdapterFor(nil, &params.ChainConfig{ChainID: big.NewInt(1)})
	require.Zero(t, legacy.streamLimit, "legacy chain keeps the gossip cap")
}

// B15 + §3: applyProfile1Limits raises the block bound to maxBlockBytes and
// pins the anchor mode; a legacy chain is left untouched.
func TestApplyProfile1Limits(t *testing.T) {
	gossip, chunk := encoder.MaxGossipSize, encoder.MaxBlockChunkSize
	t.Cleanup(func() { encoder.MaxGossipSize, encoder.MaxBlockChunkSize = gossip, chunk })

	t.Cleanup(func() { p2p.ConfigureProfile1Topics(false) })

	applyProfile1Limits(&params.ChainConfig{ChainID: big.NewInt(1)})
	require.Equal(t, gossip, encoder.MaxGossipSize)
	require.Equal(t, chunk, encoder.MaxBlockChunkSize)
	require.True(t, p2p.IsGossipTopic(p2p.BlobSidecarTopicFormat), "legacy chain keeps blob_sidecar")

	// The anchor pin is process-wide and cannot be undone; no other test of
	// this package depends on the anchor mode.
	cfg := &params.ChainConfig{ChainID: big.NewInt(1), N42Profile: params.N42Profile1,
		HotStuff: &params.HotStuffConfig{MaxBlockBytes: 64 << 20}}
	applyProfile1Limits(cfg)
	require.Equal(t, uint64(64<<20), encoder.MaxGossipSize)
	require.Equal(t, uint64(64<<20), encoder.MaxBlockChunkSize)
	require.Equal(t, uint64(64<<20), encoder.MaxWireMessageSize())
	require.Equal(t, syncanchor.Enforce, syncanchor.Current())
	// G2/AM34: only the G2 topics on Profile 1.
	require.False(t, p2p.IsGossipTopic(p2p.BlobSidecarTopicFormat))
	require.False(t, p2p.IsGossipTopic(p2p.ZKProofTopicFormat))
	require.True(t, p2p.IsGossipTopic(p2p.HotStuffConsensusTopicFormat))
}

// fakeCohortP2P satisfies p2p.P2P; cohortPublisher never calls it.
type fakeCohortP2P struct{ p2p.P2P }

// G2/AM34: on Profile 1 the cohort relay gets no gossip layer, so it neither
// subscribes to nor publishes on the cohort topics; other chains keep it.
func TestCohortPublisherProfile1(t *testing.T) {
	t.Cleanup(func() { p2p.ConfigureProfile1Topics(false) })
	pub := fakeCohortP2P{}
	require.Nil(t, cohortPublisher(nil))
	require.NotNil(t, cohortPublisher(pub))
	p2p.ConfigureProfile1Topics(true)
	require.Nil(t, cohortPublisher(pub))
	require.False(t, p2p.IsGossipTopic(p2p.MobileCohortRevealTopicFormat))
	require.True(t, p2p.IsGossipTopic(p2p.MobilePacketTopicFormat))
	require.True(t, p2p.IsGossipTopic(p2p.MobileRegistrationTopicFormat))
}
