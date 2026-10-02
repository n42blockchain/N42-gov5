// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"net"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params/networkname"
)

func TestMultiAddrFromString(t *testing.T) {
	addr, err := multiAddrFromString("/ip4/127.0.0.1/tcp/4001")
	if err != nil {
		t.Fatalf("multiAddrFromString: %v", err)
	}
	if addr.String() != "/ip4/127.0.0.1/tcp/4001" {
		t.Fatalf("addr = %q", addr.String())
	}
	if _, err := multiAddrFromString("not a multiaddr"); err == nil {
		t.Fatal("expected an error for an invalid multiaddr string")
	}
}

func TestUDPVersionFromIP(t *testing.T) {
	if got := udpVersionFromIP(net.ParseIP("127.0.0.1")); got != "udp4" {
		t.Fatalf("udpVersionFromIP(v4) = %q, want udp4", got)
	}
	if got := udpVersionFromIP(net.ParseIP("::1")); got != "udp6" {
		t.Fatalf("udpVersionFromIP(v6) = %q, want udp6", got)
	}
}

func TestParseGenericAddrsSplitsAndSkipsInvalid(t *testing.T) {
	addrs := []string{
		"",
		"/ip4/127.0.0.1/tcp/4001",
		"/ip4/10.0.0.1/tcp/30303/p2p/QmcgpsyWgH8Y8ajJz1Cu72KnS5uo2Aa2LpzU7kinSupNKC",
		"garbage-not-an-address",
	}
	enodeStrings, multiAddrStrings := parseGenericAddrs(addrs)
	if len(enodeStrings) != 0 {
		t.Fatalf("expected no enode strings, got %v", enodeStrings)
	}
	if len(multiAddrStrings) != 2 {
		t.Fatalf("expected 2 multiaddr strings, got %v", multiAddrStrings)
	}
}

func TestPeersFromStringAddrsWithMultiaddrs(t *testing.T) {
	pid := randomPeerID(t)
	addrStr := "/ip4/127.0.0.1/tcp/4001/p2p/" + pid.String()
	addrs, err := PeersFromStringAddrs([]string{addrStr})
	if err != nil {
		t.Fatalf("PeersFromStringAddrs: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("len(addrs) = %d, want 1", len(addrs))
	}
	if addrs[0].String() != addrStr {
		t.Fatalf("addrs[0] = %q, want %q", addrs[0].String(), addrStr)
	}
}

func TestPeersFromStringAddrsEmpty(t *testing.T) {
	addrs, err := PeersFromStringAddrs(nil)
	if err != nil {
		t.Fatalf("PeersFromStringAddrs(nil): %v", err)
	}
	if len(addrs) != 0 {
		t.Fatalf("expected no addrs, got %v", addrs)
	}
}

func TestParseBootStrapAddrsUsesExplicitList(t *testing.T) {
	// parseBootStrapAddrs only keeps the enode:// shaped entries (discv5
	// needs an enode, not an arbitrary multiaddr) — an invalid one is
	// logged and dropped, which is exercised here too.
	explicit := []string{"not-an-enode-or-multiaddr"}
	got := parseBootStrapAddrs(explicit, conf.NodeConfig{})
	if len(got) != 0 {
		t.Fatalf("parseBootStrapAddrs = %v, want empty (invalid entry dropped)", got)
	}
}

func TestParseBootStrapAddrsEmptyUnknownChainWarns(t *testing.T) {
	got := parseBootStrapAddrs(nil, conf.NodeConfig{Chain: "some-unknown-chain"})
	if len(got) != 0 {
		t.Fatalf("expected no bootstrap nodes for an unknown chain, got %v", got)
	}
}

func TestParseBootStrapAddrsFallsBackToNamedChain(t *testing.T) {
	// Exercise the mainnet/testnet branch selection; the result only needs
	// to not panic and to return a (possibly empty) slice without error.
	_ = parseBootStrapAddrs(nil, conf.NodeConfig{Chain: networkname.MainnetChainName})
	_ = parseBootStrapAddrs(nil, conf.NodeConfig{Chain: networkname.TestnetChainName})
}
