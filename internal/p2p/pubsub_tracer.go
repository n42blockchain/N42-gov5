package p2p

import (
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/prometheus/client_golang/prometheus"
)

// Compile-time check that gossipTracer implements pubsub.RawTracer.
var _ = pubsub.RawTracer(gossipTracer{})

// gossipTracer implements pubsub.RawTracer to collect Prometheus metrics
// for messages received and broadcasted through GossipSub.
type gossipTracer struct {
	host host.Host
}

func (g gossipTracer) AddPeer(_ peer.ID, _ protocol.ID) {}
func (g gossipTracer) RemovePeer(_ peer.ID)              {}

// OnNewOutboundStream / OnClosedOutboundStream are required by
// pubsub.RawTracer as of go-libp2p-pubsub v0.16.0. No metric is recorded —
// they mirror upstream's own gossipTracer no-ops for outbound-stream lifecycle.
func (g gossipTracer) OnNewOutboundStream(_ peer.ID, _ protocol.ID) {}
func (g gossipTracer) OnClosedOutboundStream(_ peer.ID)             {}

func (g gossipTracer) Join(topic string) {
	pubsubTopicsActive.WithLabelValues(topic).Set(1)
}

func (g gossipTracer) Leave(topic string) {
	pubsubTopicsActive.WithLabelValues(topic).Set(0)
}

func (g gossipTracer) Graft(_ peer.ID, topic string) {
	pubsubTopicsGraft.WithLabelValues(topic).Inc()
}

func (g gossipTracer) Prune(_ peer.ID, topic string) {
	pubsubTopicsPrune.WithLabelValues(topic).Inc()
}

func (g gossipTracer) ValidateMessage(msg *pubsub.Message) {
	if msg == nil || msg.Topic == nil {
		return
	}
	pubsubMessageValidate.WithLabelValues(*msg.Topic).Inc()
}

func (g gossipTracer) DeliverMessage(msg *pubsub.Message) {
	if msg == nil || msg.Topic == nil {
		return
	}
	pubsubMessageDeliver.WithLabelValues(*msg.Topic).Inc()
}

func (g gossipTracer) RejectMessage(msg *pubsub.Message, _ string) {
	if msg == nil || msg.Topic == nil {
		return
	}
	pubsubMessageReject.WithLabelValues(*msg.Topic).Inc()
}

func (g gossipTracer) DuplicateMessage(msg *pubsub.Message) {
	if msg == nil || msg.Topic == nil {
		return
	}
	pubsubMessageDuplicate.WithLabelValues(*msg.Topic).Inc()
}

func (g gossipTracer) UndeliverableMessage(msg *pubsub.Message) {
	if msg == nil || msg.Topic == nil {
		return
	}
	pubsubMessageUndeliverable.WithLabelValues(*msg.Topic).Inc()
}

func (g gossipTracer) ThrottlePeer(p peer.ID) {
	agent := agentFromPid(p, g.host.Peerstore())
	pubsubPeerThrottle.WithLabelValues(agent).Inc()
}

func (g gossipTracer) RecvRPC(rpc *pubsub.RPC) {
	recordRPCMetrics(pubsubRPCSubRecv, pubsubRPCRecv, rpc)
}

func (g gossipTracer) SendRPC(rpc *pubsub.RPC, _ peer.ID) {
	recordRPCMetrics(pubsubRPCSubSent, pubsubRPCSent, rpc)
}

func (g gossipTracer) DropRPC(rpc *pubsub.RPC, _ peer.ID) {
	recordRPCMetrics(pubsubRPCSubDrop, pubsubRPCDrop, rpc)
}

// recordRPCMetrics records subscription and control message metrics from an RPC.
func recordRPCMetrics(subCounter prometheus.Counter, controlGauge *prometheus.CounterVec, rpc *pubsub.RPC) {
	subCounter.Add(float64(len(rpc.Subscriptions)))
	if rpc.Control != nil {
		controlGauge.WithLabelValues("graft").Add(float64(len(rpc.Control.Graft)))
		controlGauge.WithLabelValues("prune").Add(float64(len(rpc.Control.Prune)))
		controlGauge.WithLabelValues("ihave").Add(float64(len(rpc.Control.Ihave)))
		controlGauge.WithLabelValues("iwant").Add(float64(len(rpc.Control.Iwant)))
	}
}
