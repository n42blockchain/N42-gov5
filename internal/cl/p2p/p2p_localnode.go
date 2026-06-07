// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/p2p. Import paths rewritten to N42 in-repo equivalents;
// enode.OpenDBEx -> enode.OpenDB (N42's enode exposes OpenDB(ctx, path, tmpDir)
// without the logger param).

//go:build n42el

package p2p

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"

	"github.com/n42blockchain/N42/internal/p2p/discover"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// privateRanges lists RFC1918 and loopback ranges that are not publicly routable.
var privateRanges = func() []net.IPNet {
	ranges := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "::1/128", "fc00::/7"}
	out := make([]net.IPNet, 0, len(ranges))
	for _, r := range ranges {
		_, ipNet, _ := net.ParseCIDR(r)
		out = append(out, *ipNet)
	}
	return out
}()

// warnIfPrivateENRIP logs a warning when the address that will be published in the
// discv5 ENR is a private/RFC1918 address and no external IP was resolved via NAT.
func warnIfPrivateENRIP(advertiseIP net.IP, externalIP net.IP, logger log.Logger) {
	if externalIP != nil || advertiseIP == nil || advertiseIP.IsUnspecified() {
		return
	}
	for _, r := range privateRanges {
		if r.Contains(advertiseIP) {
			logger.Warn("[Caplin] ENR advertises a private/RFC1918 IP — incoming peers WILL NOT be able to connect. "+
				"If running behind Docker or NAT, set --caplin.nat=extip:<public-ip> (or --caplin.nat=stun) "+
				"and expose UDP/TCP ports.", "addr", advertiseIP)
			return
		}
	}
}

func newLocalNode(
	ctx context.Context,
	privKey *ecdsa.PrivateKey,
	ipAddr net.IP,
	externalIP net.IP, // from NAT resolution; overrides ipAddr in the ENR when set
	udpPort, tcpPort int,
	tmpDir string,
	logger log.Logger,
) (*enode.LocalNode, error) {
	db, err := enode.OpenDB(ctx, "", tmpDir)
	if err != nil {
		return nil, fmt.Errorf("could not open node's peer database: %w", err)
	}
	localNode := enode.NewLocalNode(db, privKey)

	udpEntry := enr.UDP(udpPort)
	tcpEntry := enr.TCP(tcpPort)

	localNode.Set(udpEntry)
	localNode.Set(tcpEntry)
	localNode.SetFallbackUDP(udpPort)

	// Determine the IP to advertise in the ENR:
	//   1. NAT-resolved external IP takes priority.
	//   2. If bind addr is 0.0.0.0, fall back to OS outbound IP detection.
	//   3. Otherwise use the bind IP as-is.
	advertiseIP := ipAddr
	if externalIP != nil {
		advertiseIP = externalIP
	} else if ipAddr.IsUnspecified() {
		if detected := detectOutboundIP(ipAddr); detected != nil {
			logger.Info("[Caplin] Discovery address is unspecified, using detected outbound IP for ENR. Set --caplin.discovery.addr explicitly to override", "detected", detected)
			advertiseIP = detected
		} else {
			logger.Warn("[Caplin] Discovery address is unspecified and outbound IP detection failed, ENR will have no IP. Set --caplin.discovery.addr to your public IP")
			advertiseIP = nil
		}
	}

	warnIfPrivateENRIP(advertiseIP, externalIP, logger)

	if advertiseIP != nil && !advertiseIP.IsUnspecified() {
		localNode.Set(enr.IP(advertiseIP))
		localNode.SetFallbackIP(advertiseIP)
	}

	return localNode, nil
}

// detectOutboundIP determines the preferred outbound IP address by asking the
// OS routing table (no actual traffic is sent). Returns nil if detection fails.
func detectOutboundIP(unspecified net.IP) net.IP {
	network, target := "udp4", "8.8.8.8:80"
	if unspecified.To4() == nil {
		network, target = "udp6", "[2001:4860:4860::8888]:80"
	}
	conn, err := net.Dial(network, target)
	if err != nil {
		return nil
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP
}

func NewUDPv5Listener(ctx context.Context, cfg *P2PConfig, discCfg discover.Config, logger log.Logger) (*discover.UDPv5, error) {
	var (
		ipAddr = cfg.IpAddr
		port   = cfg.Port
	)

	ip := net.ParseIP(ipAddr)
	if ip == nil {
		return nil, fmt.Errorf("bad ip address provided, %s was provided", ipAddr)
	}

	var bindIP net.IP
	var networkVersion string
	// If the IP is an IPv4 address, bind to the correct zero address.
	if ip.To4() != nil {
		bindIP, networkVersion = ip.To4(), "udp4"
	} else {
		bindIP, networkVersion = ip.To16(), "udp6"
	}

	udpAddr := &net.UDPAddr{
		IP:   bindIP,
		Port: port,
	}
	conn, err := net.ListenUDP(networkVersion, udpAddr)
	if err != nil {
		return nil, err
	}

	localNode, err := newLocalNode(ctx, discCfg.PrivateKey, ip, cfg.ExternalIP, port, int(cfg.TCPPort), cfg.TmpDir, logger)
	if err != nil {
		return nil, err
	}

	// Start stream handlers
	udpListener, err := discover.ListenV5(conn, localNode, discCfg)
	if err != nil {
		return nil, err
	}
	return udpListener, nil
}
