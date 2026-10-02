// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"fmt"
	"strings"
	"testing"
)

// TestTopicFormatsCombineProtocolAndMessage checks that every *TopicFormat
// constant is built from GossipProtocolAndDigest plus its matching message
// suffix, so a rename of either half cannot silently desync them.
func TestTopicFormatsCombineProtocolAndMessage(t *testing.T) {
	cases := []struct {
		format, message string
	}{
		{BlockTopicFormat, GossipBlockMessage},
		{ExitBlockTopicFormat, GossipExitMessage},
		{TransactionTopicFormat, GossipTransactionMessage},
		{BlobSidecarTopicFormat, GossipBlobSidecarMessage},
		{DataColumnTopicFormat, GossipDataColumnMessage},
		{HotStuffConsensusTopicFormat, GossipHotStuffConsensusMessage},
		{ZKProofTopicFormat, GossipZKProofMessage},
		{MobilePacketTopicFormat, GossipMobilePacketMessage},
		{MobileRegistrationTopicFormat, GossipMobileRegistrationMessage},
		{MobileCohortIndexTopicFormat, GossipMobileCohortIndexMessage},
		{MobileCohortRevealTopicFormat, GossipMobileCohortRevealMessage},
		{MobileCohortCertTopicFormat, GossipMobileCohortCertMessage},
	}
	for _, c := range cases {
		want := GossipProtocolAndDigest + c.message
		if c.format != want {
			t.Errorf("format %q, want %q", c.format, want)
		}
		formatted := fmt.Sprintf(c.format, []byte{0xde, 0xad, 0xbe, 0xef})
		if !strings.HasPrefix(formatted, "/n42/deadbeef/") {
			t.Errorf("formatted topic %q missing expected prefix", formatted)
		}
		if !strings.HasSuffix(formatted, "/"+c.message) {
			t.Errorf("formatted topic %q missing message suffix %q", formatted, c.message)
		}
	}
}

func TestH2V4TopicIsNotDigestScoped(t *testing.T) {
	if H2V4Topic != "/n42/"+GossipH2V4Message {
		t.Fatalf("H2V4Topic = %q", H2V4Topic)
	}
}

func TestGossipMessageFormatShards(t *testing.T) {
	if GossipMessageFormat != GossipMessagePrefix+"%d" {
		t.Fatalf("GossipMessageFormat %q is not built from GossipMessagePrefix", GossipMessageFormat)
	}
	for shard := 0; shard < 8; shard++ {
		topic := fmt.Sprintf(GossipMessageFormat, shard)
		if !strings.HasPrefix(topic, GossipMessagePrefix) {
			t.Errorf("shard %d topic %q missing prefix %q", shard, topic, GossipMessagePrefix)
		}
		want := fmt.Sprintf("/n42/msg/shard/%d", shard)
		if topic != want {
			t.Errorf("shard %d topic = %q, want %q", shard, topic, want)
		}
	}
}

func TestStoreQueryProtocolUnderMessagingPrefix(t *testing.T) {
	if !strings.HasPrefix(StoreQueryProtocol, "/n42/msg/") {
		t.Fatalf("StoreQueryProtocol %q is not under the messaging prefix", StoreQueryProtocol)
	}
	if StoreQueryProtocol != "/n42/msg/store_query/1.0.0" {
		t.Fatalf("StoreQueryProtocol = %q", StoreQueryProtocol)
	}
}
