// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package nat is a minimal shim of erigon's p2p/nat. cl/p2p only needs the
// Interface type (an optional NAT resolver whose ExternalIP populates the
// discv5 ENR and libp2p multiaddrs). In the embedded B+ EL-follower the NAT
// is usually nil — the external address comes from explicit config
// (--caplin.discovery.addr / HostAddress) — so we provide just the interface
// plus an ExtIP helper for the extip:<ip> mode, without the full
// upnp/pmp/stun machinery.

//go:build n42el

package nat

import (
	"fmt"
	"net"
)

// Interface resolves the node's external IP for ENR / multiaddr advertisement.
// Mirrors the subset of erigon p2p/nat.Interface that cl/p2p consumes.
type Interface interface {
	// ExternalIP returns the external (public) IP of this host.
	ExternalIP() (net.IP, error)
	fmt.Stringer
}

// ExtIP is a fixed external IP (the extip:<ip> mode). Used when the operator
// knows the node's public address and passes it explicitly.
type ExtIP net.IP

func (n ExtIP) ExternalIP() (net.IP, error) {
	if net.IP(n) == nil {
		return nil, fmt.Errorf("nat: no external ip configured")
	}
	return net.IP(n), nil
}

func (n ExtIP) String() string { return fmt.Sprintf("extip:%v", net.IP(n)) }
