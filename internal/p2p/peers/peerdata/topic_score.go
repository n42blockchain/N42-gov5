// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package peerdata

// TopicScoreSnapshot is a peer's gossip score for one topic, as reported by
// libp2p's pubsub scorer.
//
// This used to be a generated protobuf message. It was never serialized:
// nothing sent it over a wire or wrote it to disk, and it is read back only
// through GossipScorer.GossipData in the same process that filled it in. A
// generated message brought reflection metadata, a mutex and the whole
// protobuf runtime along for a struct that is four numbers.
type TopicScoreSnapshot struct {
	// TimeInMesh is how long the peer has been in the gossip mesh, in
	// milliseconds.
	TimeInMesh uint64
	// FirstMessageDeliveries counts messages this peer delivered to us first.
	FirstMessageDeliveries float32
	// MeshMessageDeliveries counts deliveries within the mesh message
	// delivery window, i.e. first and near-first deliveries.
	MeshMessageDeliveries float32
	// InvalidMessageDeliveries counts invalid messages received from the peer
	// on this topic.
	InvalidMessageDeliveries float32
}
