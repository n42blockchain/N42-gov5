// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import "testing"

// These log helpers only format and emit log lines; the tests just confirm
// they do not panic across their branches (empty/invalid input, IPv4-only
// addr lists, etc). The logger itself is not mockable here, so behavior is
// verified by absence of a crash plus, where cheap, the computed multiaddr.

func TestLogIPAddrNoPanic(t *testing.T) {
	pid := randomPeerID(t)
	logIPAddr(pid) // no addrs
	logIPAddr(pid, mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001"))
	logIPAddr(pid, mustMultiaddr(t, "/dns4/example.com/tcp/4001")) // no ip4/ip6 substring: falls through
}

func TestLogExternalIPAddrNoPanic(t *testing.T) {
	pid := randomPeerID(t)
	logExternalIPAddr(pid, "", 4001)              // empty addr: no-op branch
	logExternalIPAddr(pid, "127.0.0.1", 4001)      // valid
	logExternalIPAddr(pid, "not-an-ip-address", 1) // MultiAddressBuilder error branch
}

func TestLogExternalDNSAddrNoPanic(t *testing.T) {
	pid := randomPeerID(t)
	logExternalDNSAddr(pid, "", 4001)
	logExternalDNSAddr(pid, "example.com", 4001)
}
