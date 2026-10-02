// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"strings"
	"testing"

	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestCanSubscribeH2V4CrossClientTopic(t *testing.T) {
	service := &Service{}
	if !service.CanSubscribe(H2V4Topic + "/ssz_snappy") {
		t.Fatal("canonical H2-v4 topic was rejected")
	}
	if service.CanSubscribe("/n42/h2/5/ssz_snappy") {
		t.Fatal("unknown H2 protocol version was accepted")
	}
	if _, err := service.topicScoreParams(H2V4Topic + "/ssz_snappy"); err != nil {
		t.Fatalf("H2-v4 topic has no scoring parameters: %v", err)
	}
}

func TestCanSubscribeMessagingPrefix(t *testing.T) {
	service := &Service{}
	for _, topic := range []string{
		"/n42/msg/shard/0",
		"/n42/msg/shard/7",
		"/n42/msg/store_query/1.0.0",
		"/n42/msg/anything/else",
	} {
		if !service.CanSubscribe(topic) {
			t.Errorf("messaging topic %q was rejected", topic)
		}
	}
}

func TestCanSubscribeGossipTopicShapes(t *testing.T) {
	service := &Service{}
	digest := "a2d2ff5d"

	if !service.CanSubscribe("/n42/" + digest + "/" + GossipBlockMessage + "/" + encoderSuffix()) {
		t.Fatal("well-formed block topic was rejected")
	}
	// Wrong number of path segments.
	if service.CanSubscribe("/n42/" + digest + "/" + GossipBlockMessage) {
		t.Fatal("topic missing the encoding suffix was accepted")
	}
	// Wrong root.
	if service.CanSubscribe("/eth2/" + digest + "/" + GossipBlockMessage + "/" + encoderSuffix()) {
		t.Fatal("topic with a foreign root was accepted")
	}
	// Wrong encoding suffix.
	if service.CanSubscribe("/n42/" + digest + "/" + GossipBlockMessage + "/ssz") {
		t.Fatal("topic with an unrecognised encoding was accepted")
	}
	// Message type not in the registry.
	if service.CanSubscribe("/n42/" + digest + "/not_a_real_message/" + encoderSuffix()) {
		t.Fatal("unregistered message type was accepted")
	}
}

func encoderSuffix() string {
	return "ssz_snappy"
}

func TestScanfcheckRejectsMismatchedFormats(t *testing.T) {
	if _, err := scanfcheck("/n42/deadbeef/block", BlockTopicFormat); err != nil {
		t.Fatalf("scanfcheck rejected a matching input: %v", err)
	}
	if _, err := scanfcheck("/n42/not-hex/block", BlockTopicFormat); err == nil {
		t.Fatal("scanfcheck accepted a non-hex digest")
	}
	if _, err := scanfcheck("short", BlockTopicFormat); err == nil {
		t.Fatal("scanfcheck accepted a short input")
	}
}

func TestFilterIncomingSubscriptionsLimitsAndFilters(t *testing.T) {
	service := &Service{}

	goodTopic := "/n42/msg/shard/0"
	badTopic := "/n42/unknown/topic/here"

	subs := []*pubsubpb.RPC_SubOpts{
		{Topicid: &goodTopic},
		{Topicid: &badTopic},
	}
	filtered, err := service.FilterIncomingSubscriptions(peer.ID(""), subs)
	if err != nil {
		t.Fatalf("FilterIncomingSubscriptions: %v", err)
	}
	if len(filtered) != 1 || filtered[0].GetTopicid() != goodTopic {
		t.Fatalf("filtered = %v, want only %q", filtered, goodTopic)
	}

	// Exceeding the subscription limit is rejected outright regardless of
	// topic validity.
	tooMany := make([]*pubsubpb.RPC_SubOpts, pubsubSubscriptionRequestLimit+1)
	for i := range tooMany {
		tooMany[i] = &pubsubpb.RPC_SubOpts{Topicid: &goodTopic}
	}
	if _, err := service.FilterIncomingSubscriptions(peer.ID(""), tooMany); err == nil {
		t.Fatal("expected an error when the subscription count exceeds the limit")
	}
}

// Sanity check that the messaging prefix really is a literal prefix of the
// shard format, as the comment in topics.go claims.
func TestGossipMessagePrefixIsLiteralPrefix(t *testing.T) {
	if !strings.HasPrefix(GossipMessageFormat, GossipMessagePrefix) {
		t.Fatalf("GossipMessageFormat %q does not start with GossipMessagePrefix %q", GossipMessageFormat, GossipMessagePrefix)
	}
}
