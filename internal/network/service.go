// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package network

import (
	"context"
	"crypto/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	kaddht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"
	"github.com/rcrowley/go-metrics"

	"github.com/n42blockchain/N42/proto/msg_proto"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/common/utils"
)

const (
	sTopic = "newBlock"
)

var (
	egressTrafficMeter  = metrics.GetOrRegisterMeter("p2p/egress", nil)
	ingressTrafficMeter = metrics.GetOrRegisterMeter("p2p/ingress", nil)
)

type metricsLog struct{}

func (l metricsLog) Printf(format string, v ...any) {
	log.Debugf(format, v...)
}

type Service struct {
	networkConfig *conf.NetWorkConfig
	dht           *kaddht.IpfsDHT
	ctx           context.Context
	cancel        context.CancelFunc

	host host.Host

	nodes common.PeerMap
	boots []multiaddr.Multiaddr

	lock sync.RWMutex

	removeCh chan peer.ID
	addCh    chan peer.AddrInfo

	bc common.IBlockChain

	handlers map[message.MessageType]common.ConnHandler

	joinedTopics     map[string]*pubsub.Topic
	joinedTopicsLock sync.Mutex

	peerCallback common.ProtocolHandshakeFn
	peerInfo     common.ProtocolHandshakeInfo

	astPubSub common.IPubSub
}

func NewService(ctx context.Context, config *conf.NetWorkConfig, peers common.PeerMap, callback common.ProtocolHandshakeFn, info common.ProtocolHandshakeInfo) (common.INetwork, error) {
	c, cancel := context.WithCancel(ctx)

	s := Service{
		networkConfig: config,
		ctx:           c,
		cancel:        cancel,
		nodes:         peers,
		removeCh:      make(chan peer.ID, 10),
		addCh:         make(chan peer.AddrInfo, 10),
		peerCallback:  callback,
		peerInfo:      info,
		handlers:      make(map[message.MessageType]common.ConnHandler),
	}

	var peerKey crypto.PrivKey
	var err error
	if s.networkConfig.LocalPeerKey == "" {
		peerKey, _, err = crypto.GenerateEd25519Key(rand.Reader)
		if err != nil {
			log.Error("failed to create peer key", "err", err)
			return nil, err
		}
	} else {
		peerKey, err = utils.StringToPrivate(s.networkConfig.LocalPeerKey)
		if err != nil {
			log.Error("failed to parse peer key from string", "err", err)
			return nil, err
		}
	}

	h, err := libp2p.New(libp2p.Identity(peerKey), libp2p.ListenAddrStrings(s.networkConfig.ListenersAddress...), libp2p.Security(libp2ptls.ID, libp2ptls.New))
	if err != nil {
		log.Error("create p2p host failed", "err", err)
		return nil, err
	}

	h.SetStreamHandler(MSGProtocol, s.handleStream)
	s.host = h

	return &s, nil
}

func (s *Service) Bootstrapped() bool {
	return len(s.networkConfig.BootstrapPeers) == 0
}

func (s *Service) Host() host.Host {
	return s.host
}

func (s *Service) Start() error {
	var peersInfo []peer.AddrInfo
	for _, bootstrapPeer := range s.networkConfig.BootstrapPeers {
		peerAddr, err := multiaddr.NewMultiaddr(bootstrapPeer)
		if err != nil {
			log.Warn("failed to parse multiaddr", "addr", bootstrapPeer, "err", err)
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(peerAddr)
		if err != nil {
			log.Warn("failed to get peer info from multiaddr", "addr", peerAddr.String(), "err", err)
			continue
		}
		if pi.ID == s.host.ID() {
			continue
		}
		peersInfo = append(peersInfo, *pi)
		s.boots = append(s.boots, peerAddr)
	}

	kadDHT, err := NewKadDht(s.ctx, s, s.networkConfig.Bootstrapped, peersInfo...)
	if err != nil {
		log.Error("failed to create KadDHT", "err", err)
		return err
	}

	if err := kadDHT.Start(); err != nil {
		return err
	}

	if len(peersInfo) > 0 {
		log.Debug("connecting to bootstrap peers")
		s.connectBootstraps(peersInfo)
	}

	hash, blockNr, _ := s.peerInfo()
	log.Info("local peer", "PeerId", s.host.ID(), "PeerAddress", s.host.Addrs(), "BlockNr", blockNr.Uint64(), "genesisHash", hash)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in nodeManager", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		s.nodeManager(s.addCh)
	}()

	return nil
}

func (s *Service) PeerCount() int {
	return len(s.nodes)
}

// nodeManager manages peer connections, handling additions and removals.
func (s *Service) nodeManager(peerCh chan peer.AddrInfo) {
	stateTimer := time.NewTicker(60 * time.Second)
	defer stateTimer.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case p, ok := <-peerCh:
			if !ok {
				continue
			}
			s.handleNewPeer(p)
		case id, ok := <-s.removeCh:
			if !ok {
				continue
			}
			log.Info("removing peer", "PeerID", id.String())
			s.deleteNode(id)
			event.GlobalEvent.Send(common.PeerDropEvent{Peer: id})
			if !s.checkBootstrap(id.String()) {
				s.host.Peerstore().RemovePeer(id)
			}
		case <-stateTimer.C:
			s.state()
		}
	}
}

// handleNewPeer attempts to connect and handshake with a newly discovered peer.
func (s *Service) handleNewPeer(p peer.AddrInfo) {
	if s.checkNode(p.ID) {
		return
	}

	log.Debug("discovered new peer", "PeerID", p.ID, "PeerAddress", p.Addrs)
	node, err := NewNode(s.ctx, s.host, s, p, s.handlers)
	if err != nil {
		return
	}

	hash, number, err := s.peerInfo()
	if err != nil {
		_ = node.Close()
		return
	}

	var h msg_proto.ProtocolHandshakeMessage
	if err := node.ProtocolHandshake(&h, AppProtocol, hash, number, true); err != nil {
		log.Warn("handshake failed", "PeerID", p.ID, "PeerAddress", p.Addrs, "ProtocolID", AppProtocol, "err", err)
		_ = node.Close()
		return
	}

	cPeer, ok := s.peerCallback(node, utils.ConvertH256ToHash(h.GenesisHash), utils.ConvertH256ToUint256Int(h.CurrentHeight))
	if !ok {
		log.Error("peer callback rejected peer", "PeerID", p.ID, "PeerAddress", p.Addrs)
		_ = node.Close()
		return
	}

	node.Start()
	s.addNode(cPeer)
	log.Info("connected peer", "peerInfo", p.String(), "blockNumber", utils.ConvertH256ToUint256Int(h.CurrentHeight).Uint64())
	event.GlobalEvent.Send(common.PeerJoinEvent{Peer: cPeer.ID()})
}

func (s *Service) checkBootstrap(id string) bool {
	for _, peerAddr := range s.boots {
		pi, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		if pi.ID.String() == id {
			return true
		}
	}
	return false
}

// connectBootstraps connects to the given bootstrap nodes concurrently.
func (s *Service) connectBootstraps(bootstraps []peer.AddrInfo) {
	var wg sync.WaitGroup
	for _, pi := range bootstraps {
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := s.host.Connect(s.ctx, pi); err != nil {
				log.Warn("failed to connect to bootnode", "PeerID", pi.ID, "err", err)
			} else {
				log.Info("connected to bootnode", "PeerID", pi.ID, "PeerAddress", pi.Addrs)
			}
		}(pi)
	}
	wg.Wait()
}

func (s *Service) Wait() {
	<-s.ctx.Done()
	s.host.Close()
}

func (s *Service) state() {
	s.lock.RLock()
	defer s.lock.RUnlock()

	for id, p := range s.nodes {
		node, ok := p.IPeer.(*Node)
		if !ok || node == nil {
			log.Debug("peer state: invalid node type", "PeerID", id)
			continue
		}
		log.Debug("peer state", "PeerID", id, "currentHeight", p.CurrentHeight.Uint64(), "PeerAddress", node.peer.Addrs, "connectTime", common.PrettyDuration(time.Since(p.AddTimer)))
		node.RLock()
		for protocolID, stream := range node.streams {
			if stream != nil {
				log.Debug("  stream state", "protocolID", protocolID, "StreamID", stream.ID(), "uptime", common.PrettyDuration(time.Since(stream.Stat().Opened)), "direction", stream.Stat().Direction)
			}
		}
		node.RUnlock()
	}
}

func (s *Service) addNode(node common.Peer) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, ok := s.nodes[node.ID()]; !ok {
		s.nodes[node.ID()] = node
	}
}

func (s *Service) deleteNode(id peer.ID) {
	s.lock.Lock()
	defer s.lock.Unlock()
	delete(s.nodes, id)
}

func (s *Service) checkNode(id peer.ID) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	_, ok := s.nodes[id]
	return ok
}

func (s *Service) handleStream(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	if s.checkNode(remotePeer) {
		log.Debug("peer already connected", "connID", stream.Conn().ID())
		return
	}

	log.Info("incoming peer stream", "streamID", stream.ID(), "protocolID", stream.Protocol(), "PeerID", remotePeer)
	p := s.host.Peerstore().PeerInfo(remotePeer)

	node, err := NewNode(s.ctx, s.host, s, p, s.handlers, WithStream(stream))
	if err != nil {
		stream.Close()
		log.Error("failed to create node", "peer", p.String(), "err", err)
		return
	}

	hash, number, err := s.peerInfo()
	if err != nil {
		log.Error("failed to get peer info", "err", err)
		return
	}

	var h msg_proto.ProtocolHandshakeMessage
	if err := node.AcceptHandshake(&h, AppProtocol, hash, number); err != nil {
		log.Debug("failed to accept handshake", "err", err)
		return
	}

	cp, ok := s.peerCallback(node, hash, number)
	if !ok {
		log.Debug("peer callback rejected incoming peer", "PeerID", remotePeer)
		return
	}

	node.Start()
	s.addNode(cp)
}

func (s *Service) HandlePeerFound(p peer.AddrInfo) {
	if p.ID == s.host.ID() {
		return
	}
	select {
	case s.addCh <- p:
	case <-s.ctx.Done():
	}
}

func (s *Service) SendMsgToPeer(id string, data []byte) error {
	msg := P2PMessage{
		MsgType: message.MsgApplication,
		Payload: data,
	}

	s.lock.RLock()
	defer s.lock.RUnlock()
	if n, ok := s.nodes[peer.ID(id)]; ok {
		return n.Write(&msg)
	}

	return errPeerNotFound
}

func (s *Service) ID() string {
	if s.host != nil {
		return s.host.ID().String()
	}
	return ""
}

func (s *Service) WriterMessage(messageType message.MessageType, payload []byte, peer peer.ID) error {
	msg := P2PMessage{
		MsgType: messageType,
		Payload: payload,
	}

	s.lock.RLock()
	defer s.lock.RUnlock()
	if n, ok := s.nodes[peer]; ok {
		return n.Write(&msg)
	}

	return errPeerNotFound
}

func (s *Service) SetHandler(mt message.MessageType, handler common.ConnHandler) error {
	if _, ok := s.handlers[mt]; !ok {
		s.handlers[mt] = handler
	}
	return nil
}

func (s *Service) ClosePeer(id peer.ID) error {
	return nil
}
