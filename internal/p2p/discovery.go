package p2p

import (
	"crypto/ecdsa"
	"net"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/discover"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/params/networkname"
	"github.com/n42blockchain/N42/common/utils"
)

// Listener defines the discovery V5 network interface that is used
// to communicate with other peers.
type Listener interface {
	Self() *enode.Node
	Close()
	Lookup(enode.ID) []*enode.Node
	Resolve(*enode.Node) *enode.Node
	RandomNodes() enode.Iterator
	Ping(*enode.Node) error
	RequestENR(*enode.Node) (*enode.Node, error)
	LocalNode() *enode.LocalNode
	AllNodes() []*enode.Node
}

// RefreshENR refreshes the ENR entry for our node, allowing it to be
// dynamically discoverable by other peers.
func (s *Service) RefreshENR() {
	if s.dv5Listener == nil {
		return
	}

	s.IncSeqNumber()
	s.pingPeers()
}

// listenForNewNodes watches for new nodes in the network and adds them to the peerstore.
func (s *Service) listenForNewNodes() {
	iterator := s.dv5Listener.RandomNodes()
	iterator = enode.Filter(iterator, s.filterPeer)
	defer iterator.Close()
	sem := make(chan struct{}, maxConcurrentPeerOps)
	for {
		if s.ctx.Err() != nil {
			break
		}
		if s.isPeerAtLimit(false /* inbound */) {
			log.Trace("Not looking for peers, at peer limit")
			time.Sleep(pollingPeriod)
			continue
		}
		exists := iterator.Next()
		if !exists {
			break
		}
		node := iterator.Node()
		peerInfo, _, err := convertToAddrInfo(node)
		if err != nil {
			log.Error("Could not convert to peer info", "err", err)
			continue
		}
		// Make sure that peer is not dialed too often, for each connection attempt there's a backoff period.
		s.Peers().RandomizeBackOff(peerInfo.ID)
		select {
		case sem <- struct{}{}:
			go func(info *peer.AddrInfo) {
				defer func() { <-sem }()
				if err := s.connectWithPeer(s.ctx, *info); err != nil {
					log.Warn("Could not connect with peer", "peer", info.String(), "err", err)
				}
			}(peerInfo)
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Service) createListener(
	ipAddr net.IP,
	privKey *ecdsa.PrivateKey,
) (*discover.UDPv5, error) {
	// By default, bind to all interfaces.
	var bindIP net.IP
	switch udpVersionFromIP(ipAddr) {
	case "udp4":
		bindIP = net.IPv4zero
	case "udp6":
		bindIP = net.IPv6zero
	default:
		return nil, errors.New("invalid ip provided")
	}

	if s.cfg.LocalIP != "" {
		ipAddr = net.ParseIP(s.cfg.LocalIP)
		if ipAddr == nil {
			return nil, errors.New("invalid local ip provided")
		}
		bindIP = ipAddr
	}
	udpAddr := &net.UDPAddr{
		IP:   bindIP,
		Port: int(s.cfg.UDPPort),
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, errors.Wrap(err, "could not listen to UDP")
	}

	localNode, err := s.createLocalNode(
		privKey,
		ipAddr,
		int(s.cfg.UDPPort),
		int(s.cfg.TCPPort),
	)
	if err != nil {
		return nil, errors.Wrap(err, "could not create local node")
	}
	if s.cfg.HostAddress != "" {
		hostIP := net.ParseIP(s.cfg.HostAddress)
		if hostIP == nil || (hostIP.To4() == nil && hostIP.To16() == nil) {
			log.Error("Invalid host address given", "address", s.cfg.HostAddress)
		} else {
			localNode.SetFallbackIP(hostIP)
			localNode.SetStaticIP(hostIP)
		}
	}
	if s.cfg.HostDNS != "" {
		ips, err := net.LookupIP(s.cfg.HostDNS)
		if err != nil {
			return nil, errors.Wrap(err, "could not resolve host address")
		}
		if len(ips) > 0 {
			localNode.SetFallbackIP(ips[0])
		}
	}
	dv5Cfg := discover.Config{
		PrivateKey: privKey,
		Bootnodes:  make([]*enode.Node, 0, len(s.cfg.Discv5BootStrapAddr)),
	}
	for _, addr := range s.cfg.Discv5BootStrapAddr {
		bootNode, err := enode.Parse(enode.ValidSchemes, addr)
		if err != nil {
			return nil, errors.Wrap(err, "could not bootstrap addr")
		}
		dv5Cfg.Bootnodes = append(dv5Cfg.Bootnodes, bootNode)
	}

	listener, err := discover.ListenV5(conn, localNode, dv5Cfg)
	if err != nil {
		return nil, errors.Wrap(err, "could not listen to discV5")
	}
	return listener, nil
}

func (s *Service) createLocalNode(
	privKey *ecdsa.PrivateKey,
	ipAddr net.IP,
	udpPort, tcpPort int,
) (*enode.LocalNode, error) {
	db, err := enode.OpenDB(s.ctx, filepath.Join(s.cfg.DataDir, "nodedata"), "")
	if err != nil {
		return nil, errors.Wrap(err, "could not open node's peer database")
	}
	localNode := enode.NewLocalNode(db, privKey)
	localNode.Set(enr.IP(ipAddr))
	localNode.Set(enr.UDP(udpPort))
	localNode.Set(enr.TCP(tcpPort))
	localNode.SetFallbackIP(ipAddr)
	localNode.SetFallbackUDP(udpPort)

	localNode, err = addForkEntry(localNode, s.genesisHash)
	if err != nil {
		return nil, errors.Wrap(err, "could not add fork version entry to enr")
	}
	return localNode, nil
}

func (s *Service) startDiscoveryV5(
	addr net.IP,
	privKey *ecdsa.PrivateKey,
) (*discover.UDPv5, error) {
	listener, err := s.createListener(addr, privKey)
	if err != nil {
		return nil, errors.Wrap(err, "could not create listener")
	}
	log.Info("Discovery v5 started", "enr", listener.Self().String())
	return listener, nil
}

// filterPeer validates each node retrieved from DHT discovery.
// A peer is valid if it has a valid IP/TCP, is not bad/active/connected,
// is ready to dial, and has a matching fork digest ENR entry.
func (s *Service) filterPeer(node *enode.Node) bool {
	if node == nil || node.IP() == nil {
		return false
	}
	if err := node.Record().Load(enr.WithEntry("tcp", new(enr.TCP))); err != nil {
		if !enr.IsNotFound(err) {
			log.Debug("Could not retrieve tcp port", "err", err)
		}
		return false
	}
	peerData, multiAddr, err := convertToAddrInfo(node)
	if err != nil {
		log.Debug("Could not convert to peer data", "err", err)
		return false
	}
	if s.peers.IsBad(peerData.ID) {
		return false
	}
	if s.peers.IsActive(peerData.ID) {
		return false
	}
	if s.host.Network().Connectedness(peerData.ID) == network.Connected {
		return false
	}
	if !s.peers.IsReadyToDial(peerData.ID) {
		return false
	}
	nodeENR := node.Record()
	if err := s.compareForkENR(nodeENR); err != nil {
		log.Trace("Fork ENR mismatches between peer and local node", "err", err)
		return false
	}
	s.peers.Add(nodeENR, peerData.ID, multiAddr, network.DirUnknown)
	return true
}

// isPeerAtLimit reports whether connected/active peers exceed the configured maximum.
// For inbound peers, the high watermark buffer is applied.
func (s *Service) isPeerAtLimit(inbound bool) bool {
	numOfConns := len(s.host.Network().Peers())
	maxPeers := int(s.cfg.MaxPeers)
	if inbound {
		maxPeers += highWatermarkBuffer
		currInbound := len(s.peers.InboundConnected())
		maxInbound := s.peers.InboundLimit() + highWatermarkBuffer
		if currInbound >= maxInbound {
			return true
		}
	}
	activePeers := len(s.Peers().Active())
	return activePeers >= maxPeers || numOfConns >= maxPeers
}

// PeersFromStringAddrs converts peer raw ENRs into multiaddrs for p2p.
func PeersFromStringAddrs(addrs []string) ([]ma.Multiaddr, error) {
	var allAddrs []ma.Multiaddr
	enodeString, multiAddrString := parseGenericAddrs(addrs)
	for _, stringAddr := range multiAddrString {
		addr, err := multiAddrFromString(stringAddr)
		if err != nil {
			return nil, errors.Wrapf(err, "Could not get multiaddr from string")
		}
		allAddrs = append(allAddrs, addr)
	}
	for _, stringAddr := range enodeString {
		enodeAddr, err := enode.Parse(enode.ValidSchemes, stringAddr)
		if err != nil {
			return nil, errors.Wrapf(err, "Could not get enode from string")
		}
		addr, err := convertToSingleMultiAddr(enodeAddr)
		if err != nil {
			return nil, errors.Wrapf(err, "Could not get multiaddr")
		}
		allAddrs = append(allAddrs, addr)
	}
	return allAddrs, nil
}

func parseBootStrapAddrs(addrs []string, nodeCfg conf.NodeConfig) (discv5Nodes []string) {
	if len(addrs) == 0 {
		switch nodeCfg.Chain {
		case networkname.MainnetChainName:
			addrs = params.MainnetBootnodes
		case networkname.TestnetChainName:
			addrs = params.TestnetBootnodes
		}
	}
	discv5Nodes, _ = parseGenericAddrs(addrs)
	if len(discv5Nodes) == 0 {
		log.Warn("No bootstrap addresses supplied")
	}
	return discv5Nodes
}

func parseGenericAddrs(addrs []string) (enodeString, multiAddrString []string) {
	for _, addr := range addrs {
		if addr == "" {
			// Ignore empty entries
			continue
		}
		_, err := enode.Parse(enode.ValidSchemes, addr)
		if err == nil {
			enodeString = append(enodeString, addr)
			continue
		}
		_, err = multiAddrFromString(addr)
		if err == nil {
			multiAddrString = append(multiAddrString, addr)
			continue
		}
		log.Error("Invalid address provided", "address", addr, "err", err)
	}
	return enodeString, multiAddrString
}

func convertToMultiAddr(nodes []*enode.Node) []ma.Multiaddr {
	var multiAddrs []ma.Multiaddr
	for _, node := range nodes {
		// ignore nodes with no ip address stored
		if node.IP() == nil {
			continue
		}
		multiAddr, err := convertToSingleMultiAddr(node)
		if err != nil {
			log.Error("Could not convert to multiAddr", "err", err)
			continue
		}
		multiAddrs = append(multiAddrs, multiAddr)
	}
	return multiAddrs
}

func convertToAddrInfo(node *enode.Node) (*peer.AddrInfo, ma.Multiaddr, error) {
	multiAddr, err := convertToSingleMultiAddr(node)
	if err != nil {
		return nil, nil, err
	}
	info, err := peer.AddrInfoFromP2pAddr(multiAddr)
	if err != nil {
		return nil, nil, err
	}
	return info, multiAddr, nil
}

// peerIDFromNode extracts the libp2p peer ID from an enode's public key.
func peerIDFromNode(node *enode.Node) (peer.ID, error) {
	assertedKey, err := utils.ConvertToInterfacePubkey(node.Pubkey())
	if err != nil {
		return "", errors.Wrap(err, "could not get pubkey")
	}
	id, err := peer.IDFromPublicKey(assertedKey)
	if err != nil {
		return "", errors.Wrap(err, "could not get peer id")
	}
	return id, nil
}

func convertToSingleMultiAddr(node *enode.Node) (ma.Multiaddr, error) {
	id, err := peerIDFromNode(node)
	if err != nil {
		return nil, err
	}
	return multiAddressBuilderWithID(node.IP().String(), "tcp", uint(node.TCP()), id)
}

func convertToUdpMultiAddr(node *enode.Node) ([]ma.Multiaddr, error) {
	id, err := peerIDFromNode(node)
	if err != nil {
		return nil, err
	}

	var addresses []ma.Multiaddr
	var ip4 enr.IPv4
	var ip6 enr.IPv6
	if node.Load(&ip4) == nil {
		address, ipErr := multiAddressBuilderWithID(net.IP(ip4).String(), "udp", uint(node.UDP()), id)
		if ipErr != nil {
			return nil, errors.Wrap(ipErr, "could not build IPv4 address")
		}
		addresses = append(addresses, address)
	}
	if node.Load(&ip6) == nil {
		address, ipErr := multiAddressBuilderWithID(net.IP(ip6).String(), "udp", uint(node.UDP()), id)
		if ipErr != nil {
			return nil, errors.Wrap(ipErr, "could not build IPv6 address")
		}
		addresses = append(addresses, address)
	}

	return addresses, nil
}

func multiAddrFromString(address string) (ma.Multiaddr, error) {
	return ma.NewMultiaddr(address)
}

func udpVersionFromIP(ipAddr net.IP) string {
	if ipAddr.To4() != nil {
		return "udp4"
	}
	return "udp6"
}
