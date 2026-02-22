package p2p

import (
	"net"
	"runtime"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/n42blockchain/N42/conf"
)

const (
	// Limit for rate limiter when processing new inbound dials.
	ipLimit = 4

	// Burst limit for inbound dials.
	ipBurst = 8

	// High watermark buffer signifies the buffer till which
	// we will handle inbound requests.
	highWatermarkBuffer = 10
)

// InterceptPeerDial tests whether we're permitted to Dial the specified peer.
func (s *Service) InterceptPeerDial(pid peer.ID) (allow bool) {
	if s.peers.IsBad(pid) {
		log.Debug("Rejecting dial to bad peer", "peer", pid)
		return false
	}
	return true
}

// InterceptAddrDial tests whether we're permitted to dial the specified
// multiaddr for the given peer.
func (s *Service) InterceptAddrDial(pid peer.ID, m multiaddr.Multiaddr) (allow bool) {
	if s.peers.IsBad(pid) {
		return false
	}
	return filterConnections(s.addrFilter, m)
}

// InterceptAccept checks whether the incidental inbound connection is allowed.
func (s *Service) InterceptAccept(n network.ConnMultiaddrs) (allow bool) {
	// Deny all incoming connections before we are ready
	if !s.started.Load() {
		return false
	}
	if !s.validateDial(n.RemoteMultiaddr()) {
		// Allow other go-routines to run in the event
		// we receive a large amount of junk connections.
		runtime.Gosched()
		log.Trace("Not accepting inbound dial from ip address", "peer", n.RemoteMultiaddr(), "reason", "exceeded dial limit")
		return false
	}
	if s.isPeerAtLimit(true /* inbound */) {
		log.Trace("Not accepting inbound dial", "peer", n.RemoteMultiaddr(), "reason", "at peer limit")
		return false
	}
	return filterConnections(s.addrFilter, n.RemoteMultiaddr())
}

// InterceptSecured tests whether a given connection, now authenticated,
// is allowed. Rejects bad peers and inbound connections at peer limit.
func (s *Service) InterceptSecured(dir network.Direction, pid peer.ID, _ network.ConnMultiaddrs) (allow bool) {
	if s.peers.IsBad(pid) {
		log.Debug("Rejecting secured connection from bad peer", "peer", pid, "direction", dir)
		return false
	}
	if dir == network.DirInbound && s.isPeerAtLimit(true /* inbound */) {
		log.Debug("Rejecting secured inbound connection", "peer", pid, "reason", "at peer limit")
		return false
	}
	return true
}

// InterceptUpgraded tests whether a fully capable connection is allowed.
func (_ *Service) InterceptUpgraded(_ network.Conn) (allow bool, reason control.DisconnectReason) {
	return true, 0
}

func (s *Service) validateDial(addr multiaddr.Multiaddr) bool {
	ip, err := manet.ToIP(addr)
	if err != nil {
		return false
	}
	return s.ipLimiter.Add(ip.String(), 1) > 0
}

var privateCIDRList = []string{
	// Private ip addresses specified by rfc-1918.
	// See: https://tools.ietf.org/html/rfc1918
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	// Reserved address space for CGN devices, specified by rfc-6598
	// See: https://tools.ietf.org/html/rfc6598
	"100.64.0.0/10",
	// IPv4 Link-Local addresses, specified by rfc-3926
	// See: https://tools.ietf.org/html/rfc3927
	"169.254.0.0/16",
}

// configureFilter looks at the provided allow lists and
// deny lists to appropriately create a filter.
func configureFilter(cfg *conf.P2PConfig) (*multiaddr.Filters, error) {
	addrFilter := multiaddr.NewFilters()

	switch cfg.AllowListCIDR {
	case "public":
		cfg.DenyListCIDR = append(cfg.DenyListCIDR, privateCIDRList...)
	case "private":
		var err error
		if addrFilter, err = privateCIDRFilter(addrFilter, multiaddr.ActionAccept); err != nil {
			return nil, err
		}
	case "":
		// No allow list configured.
	default:
		_, ipnet, err := net.ParseCIDR(cfg.AllowListCIDR)
		if err != nil {
			return nil, err
		}
		addrFilter.AddFilter(*ipnet, multiaddr.ActionAccept)
	}

	for _, cidr := range cfg.DenyListCIDR {
		switch cidr {
		case "private":
			var err error
			if addrFilter, err = privateCIDRFilter(addrFilter, multiaddr.ActionDeny); err != nil {
				return nil, err
			}
		case "public":
			var err error
			if addrFilter, err = privateCIDRFilter(addrFilter, multiaddr.ActionAccept); err != nil {
				return nil, err
			}
		default:
			_, ipnet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, err
			}
			if action, _ := addrFilter.ActionForFilter(*ipnet); action == multiaddr.ActionAccept {
				log.Warn("Address is in conflict with previous rule", "address", ipnet.String())
			}
			addrFilter.AddFilter(*ipnet, multiaddr.ActionDeny)
		}
	}
	return addrFilter, nil
}

// privateCIDRFilter applies the given action to all private CIDR ranges.
// It logs a warning if a new rule conflicts with a previously applied rule.
func privateCIDRFilter(addrFilter *multiaddr.Filters, action multiaddr.Action) (*multiaddr.Filters, error) {
	for _, privCidr := range privateCIDRList {
		_, ipnet, err := net.ParseCIDR(privCidr)
		if err != nil {
			return nil, err
		}
		if curAction, found := addrFilter.ActionForFilter(*ipnet); found && curAction != action {
			log.Warn("Address is in conflict with previous rule", "address", ipnet.String())
		}
		addrFilter.AddFilter(*ipnet, action)
	}
	return addrFilter, nil
}

// filterConnections checks IP subnets against the filter. When an allow list
// is configured, only connections from those subnets are permitted. Otherwise,
// connections are allowed unless explicitly blocked.
func filterConnections(f *multiaddr.Filters, a multiaddr.Multiaddr) bool {
	acceptedNets := f.FiltersForAction(multiaddr.ActionAccept)
	if len(acceptedNets) == 0 {
		return !f.AddrBlocked(a)
	}

	ip, err := manet.ToIP(a)
	if err != nil {
		log.Trace("Multiaddress has invalid ip", "err", err)
		return false
	}
	for _, ipnet := range acceptedNets {
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}
