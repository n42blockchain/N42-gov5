// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

func TestTopicDeconstructorValid(t *testing.T) {
	prefix, message, version, err := TopicDeconstructor(RPCStatusTopicV1)
	if err != nil {
		t.Fatalf("TopicDeconstructor: %v", err)
	}
	if prefix != protocolPrefix || message != StatusMessageName || version != SchemaVersionV1 {
		t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)", prefix, message, version, protocolPrefix, StatusMessageName, SchemaVersionV1)
	}
}

func TestTopicDeconstructorInvalidPrefix(t *testing.T) {
	if _, _, _, err := TopicDeconstructor("/not-rpc/status/1"); err == nil {
		t.Fatal("expected an error for an unrecognized protocol prefix")
	}
}

func TestTopicDeconstructorInvalidMessage(t *testing.T) {
	if _, _, _, err := TopicDeconstructor(protocolPrefix + "/not_a_message/1"); err == nil {
		t.Fatal("expected an error for an unrecognized message name")
	}
}

func TestTopicDeconstructorInvalidVersion(t *testing.T) {
	if _, _, _, err := TopicDeconstructor(protocolPrefix + StatusMessageName + "/not-a-version"); err == nil {
		t.Fatal("expected an error for an unrecognized schema version")
	}
}

func TestRPCTopicAccessors(t *testing.T) {
	topic := RPCTopic(RPCPingTopicV1)
	if got := topic.ProtocolPrefix(); got != protocolPrefix {
		t.Fatalf("ProtocolPrefix() = %q, want %q", got, protocolPrefix)
	}
	if got := topic.MessageType(); got != PingMessageName {
		t.Fatalf("MessageType() = %q, want %q", got, PingMessageName)
	}
	if got := topic.Version(); got != SchemaVersionV1 {
		t.Fatalf("Version() = %q, want %q", got, SchemaVersionV1)
	}

	bad := RPCTopic("/garbage")
	if got := bad.ProtocolPrefix(); got != "" {
		t.Fatalf("ProtocolPrefix() on a bad topic = %q, want empty", got)
	}
	if got := bad.MessageType(); got != "" {
		t.Fatalf("MessageType() on a bad topic = %q, want empty", got)
	}
	if got := bad.Version(); got != "" {
		t.Fatalf("Version() on a bad topic = %q, want empty", got)
	}
}

func TestTopicFromMessage(t *testing.T) {
	got, err := TopicFromMessage(StatusMessageName)
	if err != nil {
		t.Fatalf("TopicFromMessage: %v", err)
	}
	if got != RPCStatusTopicV1 {
		t.Fatalf("TopicFromMessage(status) = %q, want %q", got, RPCStatusTopicV1)
	}

	if _, err := TopicFromMessage("/not_a_real_message"); err == nil {
		t.Fatal("expected an error for an unregistered message name")
	}
}

func TestExtractGossipDigest(t *testing.T) {
	digest, err := ExtractGossipDigest("/n42/deadbeef/block")
	if err != nil {
		t.Fatalf("ExtractGossipDigest: %v", err)
	}
	if digest != [4]byte{0xde, 0xad, 0xbe, 0xef} {
		t.Fatalf("digest = %x, want deadbeef", digest)
	}
}

func TestExtractGossipDigestErrors(t *testing.T) {
	cases := []string{
		"",
		"short",
		"/n4",             // too short to even hold the prefix
		"/eth2/aabbccdd/x", // wrong root prefix
		"/n42/nothingafter", // no second slash
		"/n42/zzzzzzzz/block", // not hex
		"/n42/aabb/block",     // wrong digest length
	}
	for _, c := range cases {
		if _, err := ExtractGossipDigest(c); err == nil {
			t.Errorf("ExtractGossipDigest(%q): expected an error", c)
		}
	}
}

func TestConvertTopicScoresSkipsNilAndConverts(t *testing.T) {
	src := map[string]*pubsub.TopicScoreSnapshot{
		"topic-nil": nil,
		"topic-a": {
			TimeInMesh:               2 * time.Second,
			FirstMessageDeliveries:   1.5,
			MeshMessageDeliveries:    2.5,
			InvalidMessageDeliveries: 3.5,
		},
	}
	out := convertTopicScores(src)
	if _, ok := out["topic-nil"]; ok {
		t.Fatal("expected the nil snapshot to be skipped")
	}
	got, ok := out["topic-a"]
	if !ok {
		t.Fatal("expected topic-a to be present")
	}
	if got.TimeInMesh != 2000 {
		t.Fatalf("TimeInMesh = %d, want 2000 (ms)", got.TimeInMesh)
	}
	if got.FirstMessageDeliveries != 1.5 || got.MeshMessageDeliveries != 2.5 || got.InvalidMessageDeliveries != 3.5 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}
