// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p"
)

// AM34 item 4: a Profile-1 node does not subscribe to blob_sidecar (not in
// G2); a node off the profile still does.
func TestRegisterSubscribersBlobSidecarProfile1(t *testing.T) {
	t.Cleanup(func() { p2p.ConfigureProfile1Topics(false) })
	for _, p1 := range []bool{false, true} {
		p2p.ConfigureProfile1Topics(p1)
		fp := newFakeP2PWithHost(t)
		svc, err := NewService(context.Background(), WithP2P(fp), WithChainService(newServiceTestChain()))
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		digest, err := svc.currentForkDigest()
		if err != nil {
			svc.cancel()
			t.Fatalf("currentForkDigest: %v", err)
		}
		suffix := fp.Encoding().ProtocolSuffix()
		blob := svc.addDigestToTopic(p2p.BlobSidecarTopicFormat, digest) + suffix
		block := svc.addDigestToTopic(p2p.BlockTopicFormat, digest) + suffix
		if got := svc.subHandler.topicExists(blob); got == p1 {
			t.Errorf("profile1=%v: blob_sidecar subscribed = %v", p1, got)
		}
		if !svc.subHandler.topicExists(block) {
			t.Errorf("profile1=%v: block topic not subscribed", p1)
		}
		svc.cancel()
	}
}
