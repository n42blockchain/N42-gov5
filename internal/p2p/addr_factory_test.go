// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"strings"
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func TestWithRelayAddrsEmptyRelayIsIdentity(t *testing.T) {
	factory := withRelayAddrs("")
	in := []ma.Multiaddr{mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")}
	out := factory(in)
	if len(out) != 1 || out[0].String() != in[0].String() {
		t.Fatalf("expected the input unchanged, got %v", out)
	}
}

func TestWithRelayAddrsWrapsNonCircuitAddrs(t *testing.T) {
	relay := "/ip4/10.0.0.1/tcp/4001/p2p/" + randomPeerID(t).String()
	factory := withRelayAddrs(relay)
	addr := mustMultiaddr(t, "/ip4/127.0.0.1/tcp/4001")

	out := factory([]ma.Multiaddr{addr})
	// Original address plus one relay-wrapped address.
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	found := false
	for _, a := range out {
		if strings.Contains(a.String(), "/p2p-circuit") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected one of the output addresses to contain /p2p-circuit")
	}
}

func TestWithRelayAddrsSkipsAlreadyCircuitAddrs(t *testing.T) {
	relay := "/ip4/10.0.0.1/tcp/4001/p2p/" + randomPeerID(t).String()
	factory := withRelayAddrs(relay)
	circuitAddr := mustMultiaddr(t, relay+"/p2p-circuit/p2p/"+randomPeerID(t).String())

	out := factory([]ma.Multiaddr{circuitAddr})
	// No relay-addr was generated (input is already a circuit addr), so the
	// zero-relay-addrs fallback returns the original slice unchanged.
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1 (fallback to original addrs)", len(out))
	}
}
