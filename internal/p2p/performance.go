package p2p

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

type ServiceOption func(*Service, conf.NodeConfig) error

type performanceNetwork struct {
	prefix  string
	allowed map[peer.ID]struct{}
}

// WithPerformanceChain applies only to explicitly enabled performance chains.
// The immutable scope binds full genesis, chain ID and namespace. Legacy nodes
// keep their transport protocols, discovery and connection policy unchanged.
func WithPerformanceChain(chainCfg *params.ChainConfig) ServiceOption {
	return func(s *Service, nodeCfg conf.NodeConfig) error {
		if chainCfg == nil || chainCfg.Performance == nil {
			return nil
		}
		profile, err := params.ResolveExecutionProfile(nodeCfg.Profile)
		if err != nil {
			return err
		}
		if err := chainCfg.ValidatePerformanceSelection(nodeCfg.Chain, profile); err != nil {
			return err
		}
		if s.genesisHash == ([32]byte{}) {
			return errors.New("performance P2P requires the actual genesis hash")
		}
		cfg, allowed, err := performanceP2PConfig(s.cfg)
		if err != nil {
			return err
		}
		s.cfg = cfg
		s.performance = &performanceNetwork{
			prefix:  fmt.Sprintf("/n42-performance/%s/%s/%x", chainCfg.Performance.NetworkNamespace, chainCfg.ChainID, s.genesisHash[:]),
			allowed: allowed,
		}
		return nil
	}
}

// ValidatePerformanceP2PConfig is also called during genesis selection so
// unsafe explicit addresses are rejected before persistent storage is opened.
func ValidatePerformanceP2PConfig(cfg *conf.P2PConfig) error {
	_, _, err := performanceP2PConfig(cfg)
	return err
}

func performanceP2PConfig(cfg *conf.P2PConfig) (*conf.P2PConfig, map[peer.ID]struct{}, error) {
	if cfg == nil {
		cfg = &conf.P2PConfig{}
	}
	if len(cfg.BootstrapNodeAddr) != 0 || len(cfg.Discv5BootStrapAddr) != 0 || cfg.RelayNodeAddr != "" || cfg.EnableUPnP || cfg.HostDNS != "" || cfg.GenesisHashOverride != "" {
		return nil, nil, errors.New("performance P2P forbids bootnodes, relay, UPnP, DNS and genesis overrides")
	}
	for _, address := range []string{cfg.LocalIP, cfg.HostAddress} {
		if address != "" && (net.ParseIP(address) == nil || net.ParseIP(address).To4() == nil || !net.ParseIP(address).IsLoopback()) {
			return nil, nil, errors.New("performance P2P addresses must be IPv4 loopback IPs")
		}
	}
	allowed := make(map[peer.ID]struct{}, len(cfg.StaticPeers))
	for _, address := range cfg.StaticPeers {
		m, err := ma.NewMultiaddr(address)
		if err != nil {
			return nil, nil, fmt.Errorf("performance static peer: %w", err)
		}
		info, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil || len(info.Addrs) != 1 {
			return nil, nil, errors.New("performance peers require an explicit peer ID and one loopback TCP address")
		}
		transport := info.Addrs[0]
		protocols := transport.Protocols()
		if len(protocols) != 2 || protocols[0].Code != ma.P_IP4 || protocols[1].Code != ma.P_TCP || !loopbackAddress(transport) {
			return nil, nil, errors.New("performance static peers require loopback TCP; DNS, relay and QUIC are forbidden")
		}
		allowed[info.ID] = struct{}{}
	}
	copyConfig := *cfg
	copyConfig.StaticPeers = append([]string(nil), cfg.StaticPeers...)
	copyConfig.DenyListCIDR = append([]string(nil), cfg.DenyListCIDR...)
	copyConfig.NoDiscovery = true
	copyConfig.QUICEnabled = false // QUIC TLS bypasses the namespaced Noise handshake.
	if copyConfig.LocalIP == "" {
		copyConfig.LocalIP = "127.0.0.1"
	}
	if copyConfig.HostAddress == "" {
		copyConfig.HostAddress = copyConfig.LocalIP
	}
	return &copyConfig, allowed, nil
}

func loopbackAddress(address ma.Multiaddr) bool {
	ip, err := manet.ToIP(address)
	return err == nil && ip.To4() != nil && ip.IsLoopback()
}

func (s *Service) performancePeerAllowed(id peer.ID) bool {
	if s.performance == nil {
		return true
	}
	_, ok := s.performance.allowed[id]
	return ok
}

// WireTopic maps application protocol/topic names at the network boundary.
// Mapping is idempotent so shutdown paths may pass PubSub/Mux wire names.
func (s *Service) WireTopic(topic string) string {
	if s.performance == nil || strings.HasPrefix(topic, s.performance.prefix+"/") {
		return topic
	}
	return s.performance.prefix + topic
}

func (s *Service) LogicalTopic(topic string) string {
	if s.performance != nil {
		return strings.TrimPrefix(topic, s.performance.prefix)
	}
	return topic
}

// Topic helpers preserve existing mocks/interfaces and the nil-performance
// path while sync validators register under actual wire names.
func WireTopic(p P2P, topic string) string {
	if mapper, ok := p.(interface{ WireTopic(string) string }); ok {
		return mapper.WireTopic(topic)
	}
	return topic
}

func LogicalTopic(p P2P, topic string) string {
	if mapper, ok := p.(interface{ LogicalTopic(string) string }); ok {
		return mapper.LogicalTopic(topic)
	}
	return topic
}

// scopedHost covers callers that bypass Service.Send (block push, block fetch,
// Rotor and messaging). Libp2p internal identify/pubsub protocols stay intact;
// application RPC and /n42 protocols are scoped on the wire. Incoming streams
// expose logical IDs to existing rate-limiters/decoders, without weakening wire
// protocol negotiation or installing aliases on the underlying host.
type scopedHost struct {
	host.Host
	service *Service
}

func (h *scopedHost) wire(id protocol.ID) protocol.ID {
	if strings.HasPrefix(string(id), "/rpc/") || strings.HasPrefix(string(id), "/n42/") {
		return protocol.ID(h.service.WireTopic(string(id)))
	}
	return id
}

func (h *scopedHost) NewStream(ctx context.Context, pid peer.ID, ids ...protocol.ID) (network.Stream, error) {
	if !h.service.performancePeerAllowed(pid) {
		return nil, errors.New("performance stream peer is not explicitly configured")
	}
	wire := make([]protocol.ID, len(ids))
	for i, id := range ids {
		wire[i] = h.wire(id)
	}
	stream, err := h.Host.NewStream(ctx, pid, wire...)
	if err != nil {
		return nil, err
	}
	return &scopedStream{Stream: stream, host: h}, nil
}

func (h *scopedHost) SetStreamHandler(id protocol.ID, handler network.StreamHandler) {
	h.Host.SetStreamHandler(h.wire(id), func(stream network.Stream) {
		if !h.service.performancePeerAllowed(stream.Conn().RemotePeer()) {
			_ = stream.Reset()
			return
		}
		handler(&scopedStream{Stream: stream, host: h})
	})
}

func (h *scopedHost) RemoveStreamHandler(id protocol.ID) {
	h.Host.RemoveStreamHandler(h.wire(id))
}

func (h *scopedHost) SetStreamHandlerMatch(id protocol.ID, match func(protocol.ID) bool, handler network.StreamHandler) {
	appScoped := h.wire(id) != id
	h.Host.SetStreamHandlerMatch(h.wire(id), func(wireID protocol.ID) bool {
		if appScoped && !strings.HasPrefix(string(wireID), h.service.performance.prefix+"/") {
			return false
		}
		return match(protocol.ID(h.service.LogicalTopic(string(wireID))))
	}, func(stream network.Stream) {
		if !h.service.performancePeerAllowed(stream.Conn().RemotePeer()) {
			_ = stream.Reset()
			return
		}
		handler(&scopedStream{Stream: stream, host: h})
	})
}

type scopedStream struct {
	network.Stream
	host *scopedHost
}

func (s *scopedStream) Protocol() protocol.ID {
	return protocol.ID(s.host.service.LogicalTopic(string(s.Stream.Protocol())))
}

func (s *scopedStream) SetProtocol(id protocol.ID) error {
	return s.Stream.SetProtocol(s.host.wire(id))
}
