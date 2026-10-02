// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestMultiAddressBuilderIPv4(t *testing.T) {
	addr, err := MultiAddressBuilder("127.0.0.1", 4001)
	if err != nil {
		t.Fatalf("MultiAddressBuilder: %v", err)
	}
	if got, want := addr.String(), "/ip4/127.0.0.1/tcp/4001"; got != want {
		t.Fatalf("addr = %q, want %q", got, want)
	}
}

func TestMultiAddressBuilderIPv6(t *testing.T) {
	addr, err := MultiAddressBuilder("::1", 4001)
	if err != nil {
		t.Fatalf("MultiAddressBuilder: %v", err)
	}
	if got, want := addr.String(), "/ip6/::1/tcp/4001"; got != want {
		t.Fatalf("addr = %q, want %q", got, want)
	}
}

func TestMultiAddressBuilderInvalidIP(t *testing.T) {
	if _, err := MultiAddressBuilder("not-an-ip", 4001); err == nil {
		t.Fatal("expected an error for an invalid ip address")
	}
}

func TestMultiAddressBuilderWithIDIPv4(t *testing.T) {
	pid := testPeerID(t)
	addr, err := multiAddressBuilderWithID("127.0.0.1", "tcp", 4001, pid)
	if err != nil {
		t.Fatalf("multiAddressBuilderWithID: %v", err)
	}
	want := "/ip4/127.0.0.1/tcp/4001/p2p/" + pid.String()
	if addr.String() != want {
		t.Fatalf("addr = %q, want %q", addr.String(), want)
	}
}

func TestMultiAddressBuilderWithIDIPv6(t *testing.T) {
	pid := testPeerID(t)
	addr, err := multiAddressBuilderWithID("::1", "udp", 30303, pid)
	if err != nil {
		t.Fatalf("multiAddressBuilderWithID: %v", err)
	}
	want := "/ip6/::1/udp/30303/p2p/" + pid.String()
	if addr.String() != want {
		t.Fatalf("addr = %q, want %q", addr.String(), want)
	}
}

func TestMultiAddressBuilderWithIDInvalidIP(t *testing.T) {
	pid := testPeerID(t)
	if _, err := multiAddressBuilderWithID("not-an-ip", "tcp", 4001, pid); err == nil {
		t.Fatal("expected an error for an invalid ip address")
	}
}

func TestMultiAddressBuilderWithIDEmptyPeerID(t *testing.T) {
	if _, err := multiAddressBuilderWithID("127.0.0.1", "tcp", 4001, peer.ID("")); err == nil {
		t.Fatal("expected an error for an empty peer id")
	}
}

func TestPrivKeyOptionAppliesIdentity(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	opt := privKeyOption(priv)
	cfg := &libp2p.Config{}
	if err := opt(cfg); err != nil {
		t.Fatalf("privKeyOption applied to config: %v", err)
	}
	if cfg.PeerKey == nil {
		t.Fatal("expected PeerKey to be set by privKeyOption")
	}
}

func testPeerID(t *testing.T) peer.ID {
	t.Helper()
	return randomPeerID(t)
}

// Sanity check the format strings themselves use the documented separators.
func TestMultiAddressBuilderFormatSeparators(t *testing.T) {
	addr, err := MultiAddressBuilder("10.0.0.5", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(addr.String(), "/ip4/") {
		t.Fatalf("unexpected prefix: %s", addr.String())
	}
}
