// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import "sync/atomic"

// profile1Topics records that the node runs a Profile-1 chain, where a client
// subscribes only to the standard's G2 topic list (AM34 item 4). Set once at
// start-up, before the p2p and sync services start.
var profile1Topics atomic.Bool

// profile1ExcludedTopics are registered topics that are not in G2 and so are
// neither subscribed to nor accepted from peers on Profile 1:
//   - blob_sidecar: Profile 1 carries no blobs (B9);
//   - zk_proof: no service joins it and it has no scoring branch, so it must
//     not be advertised.
var profile1ExcludedTopics = map[string]struct{}{
	BlobSidecarTopicFormat: {},
	ZKProofTopicFormat:     {},
}

// ConfigureProfile1Topics restricts (enabled) or restores (disabled) the
// recognised topic set to the Profile-1 G2 list. Chains outside the profile
// never enable it and keep the full registry.
func ConfigureProfile1Topics(enabled bool) {
	profile1Topics.Store(enabled)
}

// topicAllowed reports whether a registered topic format may be used under
// the current profile.
func topicAllowed(format string) bool {
	if !profile1Topics.Load() {
		return true
	}
	_, excluded := profile1ExcludedTopics[format]
	return !excluded
}
