// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package sentinelproto is a hand-written, in-process shim of erigon's
// node/gointerfaces/sentinelproto (generated from
// node/interfaces/p2psentinel/sentinel.proto).
//
// Caplin's embedded EL-driving path (#34, B+ block-gossip-first) runs the
// sentinel and its consumer (cl/rpc.BeaconRpcP2P) in the SAME process and
// wires them via direct.SentinelClientDirect — there is no gRPC wire hop.
// So, like depshim/typesproto, we keep the real protobuf/reflection machinery
// out of the n42el dependency graph and provide only plain Go structs plus the
// SentinelClient/SentinelServer interfaces the ported code references.
//
// Two deliberate simplifications vs the .proto:
//   - Status.{FinalizedRoot,HeadRoot} are common.Hash here (not the H256
//     uint64-quad wrapper); the only callers (cl/rpc, cl/sentinel) are ported
//     alongside this shim, so we drop the gointerfaces H256 conversion layer.
//   - grpc.{CallOption,ClientStream,ServerStream} are kept so the ported
//     direct client / sentinel service compile unchanged; grpc is already a
//     first-class N42 dependency (lib/kv/remotedbserver).

//go:build n42el

package sentinelproto

import (
	"context"

	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"google.golang.org/grpc"
)

// EmptyMessage is the unit return for fire-and-forget RPCs.
type EmptyMessage struct{}

// SubscriptionData filters which gossip topics SubscribeGossip streams.
type SubscriptionData struct {
	Filter *string
}

func (m *SubscriptionData) GetFilter() string {
	if m == nil || m.Filter == nil {
		return ""
	}
	return *m.Filter
}

// Peer describes a connected libp2p peer.
type Peer struct {
	Pid          string
	State        string
	Direction    string
	Address      string
	Enr          string
	AgentVersion string
	EnodeId      string
}

func (m *Peer) GetPid() string {
	if m == nil {
		return ""
	}
	return m.Pid
}

// PeersInfoRequest optionally filters PeersInfo by direction/state.
type PeersInfoRequest struct {
	Direction *string
	State     *string
}

// PeersInfoResponse is the full peer listing.
type PeersInfoResponse struct {
	Peers []*Peer
}

// GossipData is one received (or to-be-published) gossip message.
type GossipData struct {
	Data     []byte // SSZ-encoded payload
	Name     string // topic name (e.g. "beacon_block")
	Peer     *Peer  // origin peer (nil when publishing)
	SubnetId *uint64
}

func (m *GossipData) GetData() []byte {
	if m == nil {
		return nil
	}
	return m.Data
}
func (m *GossipData) GetName() string {
	if m == nil {
		return ""
	}
	return m.Name
}
func (m *GossipData) GetPeer() *Peer {
	if m == nil {
		return nil
	}
	return m.Peer
}
func (m *GossipData) GetSubnetId() uint64 {
	if m == nil || m.SubnetId == nil {
		return 0
	}
	return *m.SubnetId
}

// Status is the peer-filtering status (eth2 Status message fields). Roots are
// common.Hash here — see the package doc for why we drop the H256 wrapper.
type Status struct {
	ForkDigest            uint32 // 4-byte fork digest packed into uint32
	FinalizedRoot         common.Hash
	FinalizedEpoch        uint64
	HeadRoot              common.Hash
	HeadSlot              uint64
	EarliestAvailableSlot *uint64 // fulu EIP-7594
}

// PeerCount summarizes peer states.
type PeerCount struct {
	Active        uint64
	Connected     uint64
	Disconnected  uint64
	Connecting    uint64
	Disconnecting uint64
}

func (m *PeerCount) GetActive() uint64 {
	if m == nil {
		return 0
	}
	return m.Active
}

// RequestData is an outbound req/resp request (e.g. beacon_blocks_by_range).
type RequestData struct {
	Data  []byte // SSZ-encoded request payload
	Topic string // req/resp protocol topic
}

// RequestDataWithPeer pins a request to a specific peer.
type RequestDataWithPeer struct {
	Data  []byte
	Topic string
	Pid   string
}

// ResponseData is the req/resp reply.
type ResponseData struct {
	Data  []byte // prefix-stripped SSZ-encoded reply
	Error bool   // did the peer signal an error
	Peer  *Peer
}

func (m *ResponseData) GetData() []byte {
	if m == nil {
		return nil
	}
	return m.Data
}
func (m *ResponseData) GetError() bool {
	if m == nil {
		return false
	}
	return m.Error
}
func (m *ResponseData) GetPeer() *Peer {
	if m == nil {
		return nil
	}
	return m.Peer
}

// Metadata is the eth2 node metadata (seq + attnets/syncnets bitfields).
type Metadata struct {
	Seq      uint64
	Attnets  string
	Syncnets string
}

// IdentityResponse describes the local node.
type IdentityResponse struct {
	Pid                string
	Enr                string
	P2PAddresses       []string
	DiscoveryAddresses []string
	Metadata           *Metadata
}

// RequestSubscribeExpiry extends a subnet subscription's lifetime.
type RequestSubscribeExpiry struct {
	Topic          string
	ExpiryUnixSecs uint64
}

// Sentinel_SubscribeGossipClient is the streaming-client half of
// SubscribeGossip (named to match erigon's generated type so ported code
// referencing it compiles unchanged).
type Sentinel_SubscribeGossipClient interface {
	Recv() (*GossipData, error)
	grpc.ClientStream
}

// Sentinel_SubscribeGossipServer is the streaming-server half of
// SubscribeGossip.
type Sentinel_SubscribeGossipServer interface {
	Send(*GossipData) error
	grpc.ServerStream
}

// SentinelClient is the consumer-facing sentinel API (cl/rpc.BeaconRpcP2P
// holds one). In-process it is satisfied by direct.SentinelClientDirect.
type SentinelClient interface {
	SetSubscribeExpiry(ctx context.Context, in *RequestSubscribeExpiry, opts ...grpc.CallOption) (*EmptyMessage, error)
	SubscribeGossip(ctx context.Context, in *SubscriptionData, opts ...grpc.CallOption) (Sentinel_SubscribeGossipClient, error)
	SendRequest(ctx context.Context, in *RequestData, opts ...grpc.CallOption) (*ResponseData, error)
	SetStatus(ctx context.Context, in *Status, opts ...grpc.CallOption) (*EmptyMessage, error)
	GetPeers(ctx context.Context, in *EmptyMessage, opts ...grpc.CallOption) (*PeerCount, error)
	BanPeer(ctx context.Context, in *Peer, opts ...grpc.CallOption) (*EmptyMessage, error)
	UnbanPeer(ctx context.Context, in *Peer, opts ...grpc.CallOption) (*EmptyMessage, error)
	PenalizePeer(ctx context.Context, in *Peer, opts ...grpc.CallOption) (*EmptyMessage, error)
	RewardPeer(ctx context.Context, in *Peer, opts ...grpc.CallOption) (*EmptyMessage, error)
	PublishGossip(ctx context.Context, in *GossipData, opts ...grpc.CallOption) (*EmptyMessage, error)
	Identity(ctx context.Context, in *EmptyMessage, opts ...grpc.CallOption) (*IdentityResponse, error)
	PeersInfo(ctx context.Context, in *PeersInfoRequest, opts ...grpc.CallOption) (*PeersInfoResponse, error)
	SendPeerRequest(ctx context.Context, in *RequestDataWithPeer, opts ...grpc.CallOption) (*ResponseData, error)
}

// SentinelServer is the producer side, implemented by cl/sentinel/service.
// direct.SentinelClientDirect adapts a SentinelServer into a SentinelClient.
type SentinelServer interface {
	SetSubscribeExpiry(context.Context, *RequestSubscribeExpiry) (*EmptyMessage, error)
	SubscribeGossip(*SubscriptionData, Sentinel_SubscribeGossipServer) error
	SendRequest(context.Context, *RequestData) (*ResponseData, error)
	SetStatus(context.Context, *Status) (*EmptyMessage, error)
	GetPeers(context.Context, *EmptyMessage) (*PeerCount, error)
	BanPeer(context.Context, *Peer) (*EmptyMessage, error)
	UnbanPeer(context.Context, *Peer) (*EmptyMessage, error)
	PenalizePeer(context.Context, *Peer) (*EmptyMessage, error)
	RewardPeer(context.Context, *Peer) (*EmptyMessage, error)
	PublishGossip(context.Context, *GossipData) (*EmptyMessage, error)
	Identity(context.Context, *EmptyMessage) (*IdentityResponse, error)
	PeersInfo(context.Context, *PeersInfoRequest) (*PeersInfoResponse, error)
	SendPeerRequest(context.Context, *RequestDataWithPeer) (*ResponseData, error)
}
