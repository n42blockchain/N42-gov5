package peerdata

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/n42blockchain/N42/proto/msg_proto"
	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/common/utils"
)

var (
	// ErrPeerUnknown is returned when there is an attempt to obtain data from a peer that is not known.
	ErrPeerUnknown = errors.New("peer unknown")
	// ErrNoPeerStatus is returned when there is a map entry for a given peer but there is no chain
	// status for that peer. This should happen in rare circumstances only, but is a very possible
	// scenario in a chaotic and adversarial network.
	ErrNoPeerStatus = errors.New("no chain status for peer")
)

// PeerConnectionState is the state of the connection.
// Concrete state constants (PeerDisconnected, etc.) are defined in the peers
// package using iota, starting from 0 in declaration order.
type PeerConnectionState int

// StoreConfig holds peer store parameters.
type StoreConfig struct {
	MaxPeers int
}

// peerConnectionStateNames maps PeerConnectionState values to their string
// representation. Order must match the iota constants in peers/status.go:
// PeerDisconnected=0, PeerDisconnecting=1, PeerConnected=2, PeerConnecting=3.
var peerConnectionStateNames = [...]string{
	"PeerDisconnected",
	"PeerDisconnecting",
	"PeerConnected",
	"PeerConnecting",
}

func (c PeerConnectionState) String() string {
	if c < 0 || int(c) >= len(peerConnectionStateNames) {
		return "(unrecognized)"
	}
	return peerConnectionStateNames[c]
}

// Store is a container for peer-related data at both the protocol and
// application level. It embeds sync.RWMutex; callers are responsible for
// acquiring the appropriate lock before accessing data.
type Store struct {
	sync.RWMutex
	ctx    context.Context
	config *StoreConfig
	peers  map[peer.ID]*PeerData
}

// PeerData aggregates protocol and application level info about a single peer.
type PeerData struct {
	// Network related data.
	Address       ma.Multiaddr
	Direction     network.Direction
	ConnState     PeerConnectionState
	Enr           *enr.Record
	NextValidTime time.Time
	// Chain related data.
	Ping                      *sync_pb.Ping
	ChainState                *sync_pb.Status
	ChainStateLastUpdated     time.Time
	ChainStateValidationError error
	// Scorers internal data.
	BadResponses         int
	ProcessedBlocks      uint64
	BlockProviderUpdated time.Time
	// Gossip Scoring data.
	TopicScores      map[string]*msg_proto.TopicScoreSnapshot
	GossipScore      float64
	BehaviourPenalty float64
}

// CurrentHeight returns the peer's current chain height from its chain state.
func (s *PeerData) CurrentHeight() *uint256.Int {
	if s.ChainState == nil {
		return nil
	}
	return utils.ConvertH256ToUint256Int(s.ChainState.CurrentHeight)
}

// NewStore creates a new peer data store.
func NewStore(ctx context.Context, config *StoreConfig) *Store {
	return &Store{
		ctx:    ctx,
		config: config,
		peers:  make(map[peer.ID]*PeerData),
	}
}

// PeerData returns data associated with a given peer, if any.
// Caller must hold at least a read lock.
func (s *Store) PeerData(pid peer.ID) (*PeerData, bool) {
	peerData, ok := s.peers[pid]
	return peerData, ok
}

// PeerDataGetOrCreate returns existing data for pid, or creates and stores a
// new empty PeerData if none exists. Caller must hold a write lock.
func (s *Store) PeerDataGetOrCreate(pid peer.ID) *PeerData {
	if peerData, ok := s.peers[pid]; ok {
		return peerData
	}
	peerData := &PeerData{}
	s.peers[pid] = peerData
	return peerData
}

// SetPeerData updates data associated with a given peer.
// Caller must hold a write lock.
func (s *Store) SetPeerData(pid peer.ID, data *PeerData) {
	s.peers[pid] = data
}

// DeletePeerData removes data associated with a given peer.
// Caller must hold a write lock.
func (s *Store) DeletePeerData(pid peer.ID) {
	delete(s.peers, pid)
}

// Peers returns the underlying peer data map.
// Caller must hold at least a read lock.
func (s *Store) Peers() map[peer.ID]*PeerData {
	return s.peers
}

// Config returns the store's configuration.
func (s *Store) Config() *StoreConfig {
	return s.config
}
