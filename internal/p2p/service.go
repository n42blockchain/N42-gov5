// Package p2p defines the network protocol implementation for N42 consensus
// used by N42, including peer discovery using discv5, gossip-sub
// using libp2p, and handing peer lifecycles + handshakes.
package p2p

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/internal/p2p/leakybucket"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
	astLog "github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/common/utils"
)

var (
	pollingPeriod = 6 * time.Second
	refreshRate   = 16 * time.Second
)

const (
	maxBadResponses      = 5
	pubsubQueueSize      = 1024
	maxDialTimeout       = 10 * time.Second
	ttfbTimeout          = 10 * time.Second
	reconnectBootNode    = 2 * time.Minute
	maxConcurrentPeerOps = 16
)

// Service for managing peer to peer (p2p) networking.
type Service struct {
	started          atomic.Bool
	isPreGenesis     atomic.Bool
	handshakeReady   chan struct{}
	handshakeOnce    sync.Once
	protectedPeers   map[peer.ID]struct{}
	pingMethod       func(ctx context.Context, id peer.ID) error
	cancel           context.CancelFunc
	cfg              *conf.P2PConfig
	peers            *peers.Status
	addrFilter       *multiaddr.Filters
	ipLimiter        *leakybucket.Collector
	privKey          *ecdsa.PrivateKey
	pubsub           *pubsub.PubSub
	joinedTopics     map[string]*pubsub.Topic
	joinedTopicsLock sync.Mutex
	dv5Listener      Listener
	startupErr       error
	startupErrMu     sync.RWMutex
	ctx              context.Context
	host             host.Host
	genesisHash      types.Hash
	ping             *sync_pb.Ping
	wg               sync.WaitGroup
}

// NewService initializes a new p2p service. No connections are made until
// Start is called during the service registry startup.
func NewService(ctx context.Context, genesisHash types.Hash, cfg *conf.P2PConfig, nodeCfg conf.NodeConfig) (*Service, error) {
	var err error
	ctx, cancel := context.WithCancel(ctx)
	_ = cancel // govet fix for lost cancel. Cancel is handled in service.Stop().

	s := &Service{
		ctx:          ctx,
		cancel:       cancel,
		cfg:          cfg,
		joinedTopics: make(map[string]*pubsub.Topic, len(gossipTopicMappings)),
		genesisHash:  genesisHash,
	}
	s.isPreGenesis.Store(true)

	s.ping, err = getSeqNumber(s.cfg)
	if err != nil {
		log.Error("Failed to generate p2p seq number", "err", err)
		return nil, err
	}

	cfg.Discv5BootStrapAddr = parseBootStrapAddrs(cfg.BootstrapNodeAddr, nodeCfg)

	ipAddr := utils.IPAddr()
	s.privKey, err = privKey(s.cfg)
	if err != nil {
		log.Error("Failed to generate p2p private key", "err", err)
		return nil, err
	}
	s.addrFilter, err = configureFilter(s.cfg)
	if err != nil {
		log.Error("Failed to create address filter", "err", err)
		return nil, err
	}
	s.ipLimiter = leakybucket.NewCollector(ipLimit, ipBurst, 30*time.Second, true /* deleteEmptyBuckets */)

	opts := s.buildOptions(ipAddr, s.privKey)
	h, err := libp2p.New(opts...)
	if err != nil {
		log.Error("Failed to create p2p host", "err", err)
		return nil, err
	}

	s.host = h
	// Gossipsub must be initialized before adding peers, as libp2p's
	// implementation ignores previously added peers.
	psOpts := s.pubsubOptions()
	setPubSubParameters()

	gs, err := pubsub.NewGossipSub(s.ctx, s.host, psOpts...)
	if err != nil {
		log.Error("Failed to start pubsub", "err", err)
		return nil, err
	}
	s.pubsub = gs

	s.peers = peers.NewStatus(ctx, &peers.StatusConfig{
		PeerLimit: s.cfg.MaxPeers,
		ScorerParams: &scorers.Config{
			BadResponsesScorerConfig: &scorers.BadResponsesScorerConfig{
				Threshold:     maxBadResponses,
				DecayInterval: 10 * time.Minute,
			},
		},
	})

	return s, nil
}

func (s *Service) GetPing() *sync_pb.Ping {
	return proto.Clone(s.ping).(*sync_pb.Ping)
}

func (s *Service) IncSeqNumber() {
	atomic.AddUint64(&s.ping.SeqNumber, 1)
}

func (s *Service) GetConfig() *conf.P2PConfig {
	return s.cfg
}

// Start the p2p service.
func (s *Service) Start() {
	if s.started.Load() {
		log.Error("Attempted to start p2p service when it was already started")
		return
	}

	s.isPreGenesis.Store(false)
	// Mark as started early so InterceptAccept allows inbound connections
	// during discovery and bootnode dialing, which can trigger peer callbacks.
	s.started.Store(true)

	var peersToWatch []string
	if s.cfg.RelayNodeAddr != "" {
		peersToWatch = append(peersToWatch, s.cfg.RelayNodeAddr)
		if err := dialRelayNode(s.ctx, s.host, s.cfg.RelayNodeAddr); err != nil {
			log.Error("Could not dial relay node", "err", err)
		}
	}

	if !s.cfg.NoDiscovery {
		ipAddr := utils.IPAddr()
		listener, err := s.startDiscoveryV5(ipAddr, s.privKey)
		if err != nil {
			log.Crit("Failed to start discovery", "err", err)
			s.recordStartupError(err)
			return
		}
		bootnodes, err := s.bootnodes()
		if err != nil {
			s.recordStartupError(err)
			return
		}
		s.connectWithAllPeers(bootnodes)
		s.dv5Listener = listener
		go s.listenForNewNodes()
		utils.RunEvery(s.ctx, reconnectBootNode, func() {
			s.ensureBootPeerConnections(bootnodes)
		})
	}

	if len(s.cfg.StaticPeers) > 0 {
		log.Info("Connecting to static peers", "count", len(s.cfg.StaticPeers), "peers", s.cfg.StaticPeers)
		addrs, err := PeersFromStringAddrs(s.cfg.StaticPeers)
		if err != nil {
			log.Error("Could not parse static peer addresses", "err", err)
		} else {
			log.Info("Parsed static peer multiaddrs", "count", len(addrs))
		}
		s.connectWithAllPeers(addrs)
	} else {
		log.Info("No static peers configured")
	}
	s.RefreshENR()

	// Periodic functions.
	if len(peersToWatch) > 0 {
		utils.RunEvery(s.ctx, ttfbTimeout, func() {
			ensurePeerConnections(s.ctx, s.host, peersToWatch...)
		})
	}

	utils.RunEvery(s.ctx, 30*time.Minute, s.Peers().Prune)
	utils.RunEvery(s.ctx, 10*time.Second, s.updateMetrics)
	utils.RunEvery(s.ctx, refreshRate, s.RefreshENR)
	utils.RunEvery(s.ctx, 1*time.Minute, s.logPeerStatus)

	logIPAddr(s.host.ID(), s.host.Network().ListenAddresses()...)

	if s.cfg.HostAddress != "" {
		logExternalIPAddr(s.host.ID(), s.cfg.HostAddress, s.cfg.TCPPort)
		verifyConnectivity(s.cfg.HostAddress, s.cfg.TCPPort, "tcp")
	}
	if s.cfg.HostDNS != "" {
		logExternalDNSAddr(s.host.ID(), s.cfg.HostDNS, s.cfg.TCPPort)
	}
	s.wg.Add(1)
	go s.loop()
}

func (s *Service) loop() {
	defer log.Debug("Context closed, exiting goroutine")
	defer s.wg.Done()
	<-s.ctx.Done()
	log.Info("Saving seq number to file")
	if err := saveSeqNumber(s.cfg, s.GetPing()); err != nil {
		log.Error("Failed to save seq number", "err", err)
	}
}

// Stop the p2p service and terminate all peer connections.
func (s *Service) Stop() error {
	s.cancel()
	s.started.Store(false)
	if s.dv5Listener != nil {
		s.dv5Listener.Close()
	}
	s.wg.Wait()
	log.Info("P2P service stopped")
	return nil
}

// Status returns an error if the service is unhealthy.
func (s *Service) Status() error {
	if s.isPreGenesis.Load() {
		return nil
	}
	if !s.started.Load() {
		return errors.New("not running")
	}
	s.startupErrMu.RLock()
	defer s.startupErrMu.RUnlock()
	return s.startupErr
}

// Started returns true if the p2p service has successfully started.
func (s *Service) Started() bool {
	return s.started.Load()
}

// Encoding returns the configured networking encoding.
func (_ *Service) Encoding() encoder.NetworkEncoding {
	return &encoder.SszNetworkEncoder{}
}

// PubSub returns the p2p pubsub framework.
func (s *Service) PubSub() *pubsub.PubSub {
	return s.pubsub
}

// Host returns the currently running libp2p host.
func (s *Service) Host() host.Host {
	return s.host
}

// SetStreamHandler sets the protocol handler on the p2p host multiplexer.
func (s *Service) SetStreamHandler(topic string, handler network.StreamHandler) {
	s.host.SetStreamHandler(protocol.ID(topic), handler)
}

// PeerID returns the Peer ID of the local peer.
func (s *Service) PeerID() peer.ID {
	return s.host.ID()
}

// Disconnect from a peer.
func (s *Service) Disconnect(pid peer.ID) error {
	return s.host.Network().ClosePeer(pid)
}

// Connect to a specific peer.
func (s *Service) Connect(pi peer.AddrInfo) error {
	return s.host.Connect(s.ctx, pi)
}

// Peers returns the peer status interface.
func (s *Service) Peers() *peers.Status {
	return s.peers
}

// ENR returns the local node's current ENR.
func (s *Service) ENR() *enr.Record {
	if s.dv5Listener == nil {
		return nil
	}
	return s.dv5Listener.Self().Record()
}

// DiscoveryAddresses returns ENR addresses as multiaddresses.
func (s *Service) DiscoveryAddresses() ([]multiaddr.Multiaddr, error) {
	if s.dv5Listener == nil {
		return nil, nil
	}
	return convertToUdpMultiAddr(s.dv5Listener.Self())
}

// AddPingMethod registers the metadata ping RPC method for ENR refresh.
func (s *Service) AddPingMethod(reqFunc func(ctx context.Context, id peer.ID) error) {
	s.pingMethod = reqFunc
}

// logPeerStatus logs periodic peer connection statistics.
func (s *Service) logPeerStatus() {
	inbound := len(s.peers.InboundConnected())
	outbound := len(s.peers.OutboundConnected())
	active := len(s.peers.Active())

	astLog.PrintStatusLine("p2p", fmt.Sprintf("%d peers (in:%d out:%d) ▸ active:%d",
		inbound+outbound, inbound, outbound, active))

	for _, p := range s.peers.All() {
		peerStr := p.String()
		if len(peerStr) > 16 {
			peerStr = peerStr[:16] + "..."
		}
		fields := []interface{}{"peer", peerStr}
		if dialArgs, err := s.peers.DialArgs(p); err == nil {
			fields = append(fields, "addr", dialArgs)
		}
		if direction, err := s.peers.Direction(p); err == nil {
			fields = append(fields, "dir", direction)
		}
		if chainState, err := s.peers.ChainState(p); err == nil {
			fields = append(fields, "height", utils.ConvertH256ToUint256Int(chainState.CurrentHeight).Uint64())
		}
		fields = append(fields, "score", fmt.Sprintf("%.1f", s.peers.Scorers().Score(p)))
		log.Debug("Peer", fields...)
	}

	if s.dv5Listener != nil {
		log.Trace("Discovery table", "nodes", len(s.dv5Listener.AllNodes()))
	}
}

func (s *Service) pingPeers() {
	if s.pingMethod == nil {
		return
	}
	connected := s.peers.Connected()
	sem := make(chan struct{}, maxConcurrentPeerOps)
	var wg sync.WaitGroup
	for _, pid := range connected {
		wg.Add(1)
		sem <- struct{}{}
		go func(id peer.ID) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.pingMethod(s.ctx, id); err != nil {
				log.Debug("Failed to ping peer", "peer", id, "err", err)
			}
		}(pid)
	}
	wg.Wait()
}

func (s *Service) connectWithAllPeers(multiAddrs []multiaddr.Multiaddr) {
	addrInfos, err := peer.AddrInfosFromP2pAddrs(multiAddrs...)
	if err != nil {
		log.Error("Could not convert to peer address info's from multiaddresses", "err", err)
		return
	}
	sem := make(chan struct{}, maxConcurrentPeerOps)
	var wg sync.WaitGroup
	for _, info := range addrInfos {
		wg.Add(1)
		sem <- struct{}{}
		go func(info peer.AddrInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.connectWithPeer(s.ctx, info); err != nil {
				log.Trace("Could not connect with peer", "peer", info.String(), "err", err)
			}
		}(info)
	}
	wg.Wait()
}

func (s *Service) connectWithPeer(ctx context.Context, info peer.AddrInfo) error {
	ctx, span := trace.StartSpan(ctx, "p2p.connectWithPeer")
	defer span.End()

	if info.ID == s.host.ID() {
		log.Warn("bootNode ID == localNode ID")
		return nil
	}
	if s.Peers().IsBad(info.ID) {
		return errors.New("refused to connect to bad peer")
	}
	ctx, cancel := context.WithTimeout(ctx, maxDialTimeout)
	defer cancel()

	log.Info("Dialing peer", "id", info.ID, "addrs", info.Addrs)
	if err := s.host.Connect(ctx, info); err != nil {
		// Do not penalize peer for transient dial failures (peer id mismatch
		// after key rotation, connection refused, timeout). Only increment
		// bad response score for protocol-level misbehavior, not connectivity
		// issues. This prevents bootnode from being permanently blacklisted
		// after a key rotation or temporary unavailability.
		errMsg := err.Error()
		if !strings.Contains(errMsg, "peer id mismatch") &&
			!strings.Contains(errMsg, "connection refused") &&
			!strings.Contains(errMsg, "context deadline exceeded") &&
			!strings.Contains(errMsg, "i/o timeout") {
			s.Peers().Scorers().BadResponsesScorer().Increment(info.ID)
		}
		log.Warn("Peer dial failed", "id", info.ID, "err", err)
		return err
	}
	log.Info("Peer connected successfully", "id", info.ID)
	return nil
}

func (s *Service) bootnodes() ([]multiaddr.Multiaddr, error) {
	nodes := make([]*enode.Node, 0, len(s.cfg.Discv5BootStrapAddr))
	for _, addr := range s.cfg.Discv5BootStrapAddr {
		bootNode, err := enode.Parse(enode.ValidSchemes, addr)
		if err != nil {
			return nil, err
		}
		if err := bootNode.Record().Load(enr.WithEntry("tcp", new(enr.TCP))); err != nil {
			if !enr.IsNotFound(err) {
				log.Error("Could not retrieve tcp port", "err", err)
			}
			continue
		}
		nodes = append(nodes, bootNode)
	}
	return convertToMultiAddr(nodes), nil
}

// recordStartupError records a startup failure and marks the service as not started.
func (s *Service) recordStartupError(err error) {
	s.started.Store(false)
	s.startupErrMu.Lock()
	s.startupErr = err
	s.startupErrMu.Unlock()
}

// ensureBootPeerConnections reconnects to disconnected bootnodes.
func (s *Service) ensureBootPeerConnections(bootnodes []multiaddr.Multiaddr) {
	addrInfos, err := peer.AddrInfosFromP2pAddrs(bootnodes...)
	if err != nil {
		log.Error("Could not convert to peer address info's from multiaddresses", "err", err)
		return
	}
	sem := make(chan struct{}, maxConcurrentPeerOps)
	var wg sync.WaitGroup
	for _, info := range addrInfos {
		if connState, err := s.peers.ConnState(info.ID); err != nil || connState != peers.PeerDisconnected {
			continue
		}
		if nextValidTime, err := s.peers.NextValidTime(info.ID); err != nil || !time.Now().After(nextValidTime) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(info peer.AddrInfo) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.connectWithPeer(s.ctx, info); err != nil {
				log.Warn("Could not connect with bootnode", "peer", info.String(), "err", err)
			}
		}(info)
	}
	wg.Wait()
}
