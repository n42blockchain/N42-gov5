// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"fmt"
	"testing"
)

// g2Topics is the standard's G2 list: consensus, block, transaction_v2,
// /n42/h2/4 and mobileverify_{packet,registration}.
var g2Topics = []string{
	HotStuffConsensusTopicFormat,
	BlockTopicFormat,
	TransactionTopicFormat,
	H2V4Topic,
	MobilePacketTopicFormat,
	MobileRegistrationTopicFormat,
}

func wireTopic(format string) string {
	if format == H2V4Topic {
		return format + "/" + encoderSuffix()
	}
	return fmt.Sprintf(format, [4]byte{0x18, 0x8e, 0x9d, 0x93}) + "/" + encoderSuffix()
}

// AM34 item 4: on Profile 1 blob_sidecar and zk_proof are neither recognised
// nor accepted from peers; every G2 topic still is. Off the profile both
// stay as before.
func TestProfile1TopicsExcludeNonG2(t *testing.T) {
	t.Cleanup(func() { ConfigureProfile1Topics(false) })
	service := &Service{}
	excluded := []string{BlobSidecarTopicFormat, ZKProofTopicFormat}

	for _, p1 := range []bool{false, true} {
		ConfigureProfile1Topics(p1)
		for _, topic := range excluded {
			if got := IsGossipTopic(topic); got == p1 {
				t.Errorf("profile1=%v: IsGossipTopic(%s) = %v", p1, topic, got)
			}
			if got := service.CanSubscribe(wireTopic(topic)); got == p1 {
				t.Errorf("profile1=%v: CanSubscribe(%s) = %v", p1, topic, got)
			}
			inAll := false
			for _, a := range AllTopics() {
				inAll = inAll || a == topic
			}
			if inAll == p1 {
				t.Errorf("profile1=%v: AllTopics contains %s = %v", p1, topic, inAll)
			}
		}
		for _, topic := range g2Topics {
			if !IsGossipTopic(topic) || !service.CanSubscribe(wireTopic(topic)) {
				t.Errorf("profile1=%v: G2 topic %s not usable", p1, topic)
			}
		}
	}
}

// On Profile 1 every G2 topic has a scoring branch, and the excluded
// zk_proof (which has none) is no longer reachable.
func TestProfile1G2TopicsAllScored(t *testing.T) {
	t.Cleanup(func() { ConfigureProfile1Topics(false) })
	ConfigureProfile1Topics(true)
	service := &Service{}
	for _, topic := range g2Topics {
		if _, err := service.topicScoreParams(wireTopic(topic)); err != nil {
			t.Errorf("G2 topic %s has no scoring branch: %v", topic, err)
		}
	}
	for _, topic := range AllTopics() {
		if topic == ZKProofTopicFormat {
			t.Fatal("zk_proof still registered on Profile 1")
		}
	}
}
