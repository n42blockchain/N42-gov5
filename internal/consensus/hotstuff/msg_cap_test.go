// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/params"
)

func gossipBytes(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := (encoder.SszNetworkEncoder{}).EncodeGossip(&buf, &rawSSZMarshaler{data: bytes.Repeat([]byte{0x5a}, n)}); err != nil {
		t.Fatalf("encode %d bytes: %v", n, err)
	}
	return buf.Bytes()
}

func capService(profile1 bool) *Service {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	if profile1 {
		cfg.N42Profile = params.N42Profile1
	}
	return &Service{engine: &HotStuff{chainConfig: cfg}}
}

// G5: a Profile-1 node refuses a consensus message whose decoded size is
// above MAX_CONSENSUS_MSG = 64 KiB and accepts one at the cap.
func TestDecodeConsensusGossipProfile1Cap(t *testing.T) {
	s := capService(true)
	enc := encoder.SszNetworkEncoder{}
	at := &rawSSZMarshaler{}
	if err := s.decodeConsensusGossip(enc, gossipBytes(t, MaxConsensusMsg), at); err != nil {
		t.Fatalf("message at the cap refused: %v", err)
	}
	if len(at.data) != MaxConsensusMsg {
		t.Fatalf("decoded %d bytes, want %d", len(at.data), MaxConsensusMsg)
	}
	if err := s.decodeConsensusGossip(enc, gossipBytes(t, MaxConsensusMsg+1), &rawSSZMarshaler{}); err == nil {
		t.Fatal("message one byte above the cap accepted")
	}
}

// Outside the profile the shared gossip cap still applies, as before.
func TestDecodeConsensusGossipLegacyUnchanged(t *testing.T) {
	s := capService(false)
	out := &rawSSZMarshaler{}
	if err := s.decodeConsensusGossip(encoder.SszNetworkEncoder{}, gossipBytes(t, MaxConsensusMsg+1), out); err != nil {
		t.Fatalf("legacy chain refused a message under the gossip cap: %v", err)
	}
}

func TestMaxConsensusWireMsgCoversCap(t *testing.T) {
	if MaxConsensusWireMsg < MaxConsensusMsg {
		t.Fatalf("wire bound %d below decoded cap %d", MaxConsensusWireMsg, MaxConsensusMsg)
	}
}
