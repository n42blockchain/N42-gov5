// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
)

// None of gossipTracer's methods are observable via a public getter (the
// backing collectors are unexported Prometheus vectors), so these tests only
// confirm each method — including its nil/empty guards — runs without
// panicking across the whole pubsub.RawTracer surface.
func TestGossipTracerNoPanics(t *testing.T) {
	s := newTestService(t)
	g := gossipTracer{host: s.host}
	pid := s.host.ID()
	topic := "/n42/msg/shard/0"

	g.AddPeer(pid, "proto")
	g.RemovePeer(pid)
	g.OnNewOutboundStream(pid, "proto")
	g.OnClosedOutboundStream(pid)
	g.Join(topic)
	g.Leave(topic)
	g.Graft(pid, topic)
	g.Prune(pid, topic)
	g.ThrottlePeer(pid)

	// Message-shaped callbacks: nil message and nil topic must be no-ops,
	// not panics.
	g.ValidateMessage(nil)
	g.DeliverMessage(nil)
	g.RejectMessage(nil, "reason")
	g.DuplicateMessage(nil)
	g.UndeliverableMessage(nil)

	msgNoTopic := &pubsub.Message{Message: &pubsubpb.Message{}}
	g.ValidateMessage(msgNoTopic)
	g.DeliverMessage(msgNoTopic)
	g.RejectMessage(msgNoTopic, "reason")
	g.DuplicateMessage(msgNoTopic)
	g.UndeliverableMessage(msgNoTopic)

	withTopic := topic
	msg := &pubsub.Message{Message: &pubsubpb.Message{Topic: &withTopic}}
	g.ValidateMessage(msg)
	g.DeliverMessage(msg)
	g.RejectMessage(msg, "reason")
	g.DuplicateMessage(msg)
	g.UndeliverableMessage(msg)

	// RPC callbacks, with and without a control message.
	bareRPC := &pubsub.RPC{RPC: pubsubpb.RPC{}}
	g.RecvRPC(bareRPC)
	g.SendRPC(bareRPC, pid)
	g.DropRPC(bareRPC, pid)

	full := &pubsub.RPC{RPC: pubsubpb.RPC{
		Subscriptions: []*pubsubpb.RPC_SubOpts{{}},
		Control: &pubsubpb.ControlMessage{
			Graft: []*pubsubpb.ControlGraft{{}},
			Prune: []*pubsubpb.ControlPrune{{}},
			Ihave: []*pubsubpb.ControlIHave{{}},
			Iwant: []*pubsubpb.ControlIWant{{}},
		},
	}}
	g.RecvRPC(full)
	g.SendRPC(full, pid)
	g.DropRPC(full, pid)
}

func TestRecordRPCMetricsNilControl(t *testing.T) {
	// recordRPCMetrics must tolerate a nil Control without panicking.
	recordRPCMetrics(pubsubRPCSubRecv, pubsubRPCRecv, &pubsub.RPC{RPC: pubsubpb.RPC{}})
}
