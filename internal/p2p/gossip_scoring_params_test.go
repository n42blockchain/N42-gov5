// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"math"
	"testing"
)

func TestTopicScoreParamsBranches(t *testing.T) {
	s := &Service{}
	digest := "a2d2ff5d"

	cases := []struct {
		name  string
		topic string
	}{
		{"block", "/n42/" + digest + "/" + GossipBlockMessage + "/ssz_snappy"},
		{"exit", "/n42/" + digest + "/" + GossipExitMessage + "/ssz_snappy"},
		{"blob", "/n42/" + digest + "/" + GossipBlobSidecarMessage + "/ssz_snappy"},
		{"data_column", "/n42/" + digest + "/" + GossipDataColumnMessage + "/ssz_snappy"},
		{"hotstuff", "/n42/" + digest + "/" + GossipHotStuffConsensusMessage + "/ssz_snappy"},
		{"h2v4", H2V4Topic + "/ssz_snappy"},
		{"messaging", "/n42/msg/shard/0"},
		{"mobile_packet", "/n42/" + digest + "/" + GossipMobilePacketMessage + "/ssz_snappy"},
		{"mobile_registration", "/n42/" + digest + "/" + GossipMobileRegistrationMessage + "/ssz_snappy"},
		{"mobile_cohort_index", "/n42/" + digest + "/" + GossipMobileCohortIndexMessage + "/ssz_snappy"},
		{"mobile_cohort_reveal", "/n42/" + digest + "/" + GossipMobileCohortRevealMessage + "/ssz_snappy"},
		{"mobile_cohort_cert", "/n42/" + digest + "/" + GossipMobileCohortCertMessage + "/ssz_snappy"},
		{"transaction", "/n42/" + digest + "/" + GossipTransactionMessage + "/ssz_snappy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			params, err := s.topicScoreParams(c.topic)
			if err != nil {
				t.Fatalf("topicScoreParams(%q): %v", c.topic, err)
			}
			if params == nil {
				t.Fatal("params is nil")
			}
			if params.TopicWeight <= 0 {
				t.Errorf("TopicWeight = %v, want > 0", params.TopicWeight)
			}
		})
	}

	if _, err := s.topicScoreParams("/n42/unregistered/topic/ssz_snappy"); err == nil {
		t.Fatal("expected an error for an unrecognized topic")
	}
}

func TestBlockVsMessagingTopicWeights(t *testing.T) {
	block := blockTopicParams()
	msg := messagingTopicParams()
	if block.TopicWeight <= msg.TopicWeight {
		t.Fatalf("block weight %v should exceed messaging weight %v", block.TopicWeight, msg.TopicWeight)
	}
	if msg.TopicWeight != 0.1 {
		t.Fatalf("messagingTopicParams weight = %v, want 0.1", msg.TopicWeight)
	}
}

func TestHotstuffTopicParamsHigherDeliveryCap(t *testing.T) {
	block := blockTopicParams()
	hs := hotstuffConsensusTopicParams()
	if hs.FirstMessageDeliveriesCap <= block.FirstMessageDeliveriesCap {
		t.Fatalf("hotstuff cap %v should exceed block cap %v", hs.FirstMessageDeliveriesCap, block.FirstMessageDeliveriesCap)
	}
	if hs.TopicWeight != block.TopicWeight {
		t.Fatalf("hotstuff weight %v should match block weight %v (both critical)", hs.TopicWeight, block.TopicWeight)
	}
}

func TestVoluntaryExitTopicParams(t *testing.T) {
	p := voluntaryExitTopicParams()
	if p.TopicWeight != voluntaryExitWeight {
		t.Fatalf("TopicWeight = %v, want %v", p.TopicWeight, voluntaryExitWeight)
	}
	if p.InvalidMessageDeliveriesWeight >= 0 {
		t.Fatal("invalid message deliveries weight should be negative (a penalty)")
	}
}

func TestPeerScoringParams(t *testing.T) {
	params, thresholds := peerScoringParams()
	if params == nil || thresholds == nil {
		t.Fatal("peerScoringParams returned nil")
	}
	if params.AppSpecificScore == nil {
		t.Fatal("AppSpecificScore function is nil")
	}
	if got := params.AppSpecificScore(""); got != 0 {
		t.Fatalf("AppSpecificScore(\"\") = %v, want 0", got)
	}
	if thresholds.GossipThreshold >= 0 {
		t.Fatal("GossipThreshold should be negative")
	}
	if params.Topics == nil {
		t.Fatal("Topics map should be initialized, not nil")
	}
}

func TestScoreDecayMonotonic(t *testing.T) {
	short := scoreDecay(tenBlocks)
	long := scoreDecay(oneHundredBlocks)
	// A longer total duration means slower decay (closer to 1 per step).
	if !(long > short) {
		t.Fatalf("scoreDecay(100 blocks)=%v should be greater (slower decay) than scoreDecay(10 blocks)=%v", long, short)
	}
	for _, d := range []float64{short, long} {
		if d <= 0 || d >= 1 {
			t.Fatalf("decay factor %v out of (0,1) range", d)
		}
	}
}

func TestInMeshCapAndBlockDuration(t *testing.T) {
	if oneBlockDuration().Seconds() != 8 {
		t.Fatalf("oneBlockDuration = %v, want 8s", oneBlockDuration())
	}
	want := float64(3600 / 8)
	if inMeshCap() != want {
		t.Fatalf("inMeshCap() = %v, want %v", inMeshCap(), want)
	}
}

func TestLogGossipParametersHandlesNil(t *testing.T) {
	// Must not panic on a nil params pointer.
	logGossipParameters("some/topic", nil)
}

func TestLogGossipParametersWithValue(t *testing.T) {
	// Must not panic when logging a populated struct via reflection.
	logGossipParameters("some/topic", blockTopicParams())
}

func TestScoreDecayMatchesFormula(t *testing.T) {
	d := scoreDecay(twentyBlocks)
	numBlocks := twentyBlocks / oneBlockDuration()
	want := math.Pow(decayToZero, 1/float64(numBlocks))
	if d != want {
		t.Fatalf("scoreDecay = %v, want %v", d, want)
	}
}
