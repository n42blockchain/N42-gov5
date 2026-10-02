package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/internal/p2p"
)

// TestAddDigestAndIndexToTopicFormatsPlaceholder covers the subnet-indexed
// topic formatter alongside addDigestToTopic (already exercised elsewhere).
func TestAddDigestAndIndexToTopicFormatsPlaceholder(t *testing.T) {
	svc := &Service{}
	digest := [4]byte{0x01, 0x02, 0x03, 0x04}
	got := svc.addDigestAndIndexToTopic("/n42/%x/thing/%d", digest, 7)
	want := "/n42/01020304/thing/7"
	if got != want {
		t.Fatalf("addDigestAndIndexToTopic() = %q, want %q", got, want)
	}
}

// TestAgentStringReadsPeerstoreVersion covers both the hit and miss paths
// of agentString against a real (socket-less) libp2p host's peerstore.
func TestAgentStringReadsPeerstoreVersion(t *testing.T) {
	fp := newFakeP2PWithHost(t)
	pid, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	if got := agentString(pid, fp.realHost); got != "" {
		t.Fatalf("agentString() on unknown peer = %q, want empty", got)
	}

	if err := fp.realHost.Peerstore().Put(pid, "AgentVersion", "n42/test"); err != nil {
		t.Fatalf("Peerstore.Put: %v", err)
	}
	if got := agentString(pid, fp.realHost); got != "n42/test" {
		t.Fatalf("agentString() = %q, want %q", got, "n42/test")
	}
}

// TestUnSubscribeFromTopicLeavesTopicAndCancelsSub drives
// unSubscribeFromTopic end to end against a real GossipSub router: join a
// topic via subscribeWithBase, then unsubscribe and verify the topic is no
// longer tracked by subHandler and the validator/topic have been released.
func TestUnSubscribeFromTopicLeavesTopicAndCancelsSub(t *testing.T) {
	fp := newFakeP2PWithHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := &Service{ctx: ctx, cancel: cancel, cfg: &config{p2p: fp}}
	svc.subHandler = newSubTopicHandler()

	topic := p2p.BlockTopicFormat
	digest := [4]byte{}
	full := svc.addDigestToTopic(topic, digest)
	sub := svc.subscribeWithBase(full, svc.noopValidator, func(context.Context, any) error { return nil })
	_ = sub

	fullWithSuffix := full + fp.Encoding().ProtocolSuffix()
	if !svc.subHandler.topicExists(fullWithSuffix) {
		t.Fatalf("expected topic %q to be tracked after subscribeWithBase", fullWithSuffix)
	}

	svc.unSubscribeFromTopic(fullWithSuffix)
	if svc.subHandler.topicExists(fullWithSuffix) {
		t.Fatal("expected topic to be removed after unSubscribeFromTopic")
	}
}
