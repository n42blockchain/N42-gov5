// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type h2V4EnvelopeFixture struct {
	Schema      string `json:"schema"`
	EnvelopeHex string `json:"envelope_hex"`
	GossipHex   string `json:"gossip_hex"`
}

func TestH2V4CrossClientEnvelope(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	items := crossClientH2Messages(t)
	commitVote := items[2].msg
	envelope := H2V4Envelope{
		Identity: identity, ChangesHash: repeatedHash(0x33), Message: commitVote,
	}
	wire, err := EncodeH2V4Envelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	gossip, err := EncodeH2V4Gossip(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeH2V4Gossip(gossip, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Message, commitVote) || decoded.ChangesHash != envelope.ChangesHash {
		t.Fatal("H2-v4 envelope round trip mismatch")
	}

	fixture := h2V4EnvelopeFixture{Schema: "n42-h2-v4-envelope-v1", EnvelopeHex: hex.EncodeToString(wire), GossipHex: hex.EncodeToString(gossip)}
	got, _ := json.MarshalIndent(fixture, "", "  ")
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/h2_v4_envelope_v1.json")
	if os.IsNotExist(err) {
		t.Logf("fixture to pin:\n%s", got)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("H2-v4 envelope fixture drift\nwant:\n%s\ngot:\n%s", want, got)
	}

	wrong := identity
	wrong.GenesisHash[0] ^= 1
	if _, err := DecodeH2V4Envelope(wire, wrong); err == nil {
		t.Fatal("wrong chain identity accepted")
	}
	if _, err := DecodeH2V4Envelope(append(wire, 0), identity); err == nil {
		t.Fatal("trailing byte accepted")
	}
}
