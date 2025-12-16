package p2p

import (
	"context"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"google.golang.org/protobuf/proto"
)

// P2P represents the full p2p interface composed of all of the sub-interfaces.
// This is the main entry point for p2p functionality in the N42 node.
//
// Implementations:
//   - *Service: The concrete p2p service implementation in service.go
//
// Sub-interfaces:
//   - Broadcaster: Message broadcasting via gossipsub
//   - SetStreamHandler: Stream protocol handling
//   - PubSubProvider: Access to the underlying pubsub instance
//   - PubSubTopicUser: Topic join/leave/publish/subscribe operations
//   - SenderEncoder: Message encoding and sending to specific peers
//   - PeerManager: Peer lifecycle management (disconnect, ENR, discovery)
//   - ConnectionHandler: Connection/disconnection event handling
//   - PeersProvider: Access to peer status information
//   - PingProvider: Ping/pong protocol for liveness checking
//
// Usage:
//
//	p2pService, err := p2p.NewService(ctx, genesisHash, cfg, nodeCfg)
//	p2pService.Start()
//	defer p2pService.Stop()
//
// For sync-specific operations, use the SyncP2P interface instead.
type P2P interface {
	Broadcaster
	SetStreamHandler
	PubSubProvider
	PubSubTopicUser
	SenderEncoder
	PeerManager
	ConnectionHandler
	PeersProvider
	PingProvider

	Start()
	Stop() error
	GetConfig() *conf.P2PConfig
}

// Broadcaster broadcasts messages to peers over the p2p pubsub protocol.
type Broadcaster interface {
	Broadcast(context.Context, proto.Message) error
}

// SetStreamHandler configures p2p to handle streams of a certain topic ID.
type SetStreamHandler interface {
	SetStreamHandler(topic string, handler network.StreamHandler)
}

// PubSubTopicUser provides way to join, use and leave PubSub topics.
type PubSubTopicUser interface {
	JoinTopic(topic string, opts ...pubsub.TopicOpt) (*pubsub.Topic, error)
	LeaveTopic(topic string) error
	PublishToTopic(ctx context.Context, topic string, data []byte, opts ...pubsub.PubOpt) error
	SubscribeToTopic(topic string, opts ...pubsub.SubOpt) (*pubsub.Subscription, error)
}

// ConnectionHandler configures p2p to handle connections with a peer.
type ConnectionHandler interface {
	AddConnectionHandler(f func(ctx context.Context, id peer.ID) error,
		j func(ctx context.Context, id peer.ID) error)
	AddDisconnectionHandler(f func(ctx context.Context, id peer.ID) error)
	connmgr.ConnectionGater
}

// SenderEncoder allows sending functionality from libp2p as well as encoding for requests and responses.
type SenderEncoder interface {
	EncodingProvider
	Sender
}

// EncodingProvider provides p2p network encoding.
type EncodingProvider interface {
	Encoding() encoder.NetworkEncoding
}

// PubSubProvider provides the p2p pubsub protocol.
type PubSubProvider interface {
	PubSub() *pubsub.PubSub
}

// PeerManager abstracts some peer management methods from libp2p.
type PeerManager interface {
	Disconnect(peer.ID) error
	PeerID() peer.ID
	Host() host.Host
	ENR() *enr.Record
	DiscoveryAddresses() ([]multiaddr.Multiaddr, error)
	RefreshENR()
	AddPingMethod(reqFunc func(ctx context.Context, id peer.ID) error)
}

// Sender abstracts the sending functionality from libp2p.
type Sender interface {
	Send(context.Context, interface{}, string, peer.ID) (network.Stream, error)
}

// PeersProvider abstracts obtaining our current list of known peers status.
type PeersProvider interface {
	Peers() *peers.Status
}

// PingProvider returns the metadata related information for the local peer.
type PingProvider interface {
	GetPing() *sync_pb.Ping
	IncSeqNumber()
}

// =============================================================================
// Compile-time interface checks
// =============================================================================

// Compile-time check: Service must implement P2P
var _ P2P = (*Service)(nil)
