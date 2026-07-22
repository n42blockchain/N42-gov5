// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

type h2V4EnvelopeFixture struct {
	Schema      string `json:"schema"`
	EnvelopeHex string `json:"envelope_hex"`
	GossipHex   string `json:"gossip_hex"`
}

type h2V4FinalityFixture struct {
	Schema       string   `json:"schema"`
	ChainID      uint64   `json:"chain_id"`
	GenesisHash  string   `json:"genesis_hash"`
	ChangesHash  string   `json:"changes_hash"`
	ValidatorPKs []string `json:"validator_public_keys"`
	EnvelopeHex  string   `json:"envelope_hex"`
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

func TestH2V4CrossClientFinalityProof(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	changesHash := repeatedHash(0x33)
	blockHash := repeatedHash(0x44)
	const view = uint64(9001)

	keys := make([]blscommon.SecretKey, 4)
	validators := make([]ValidatorInfo, 4)
	validatorPKs := make([]string, 4)
	for i := range keys {
		var ikm [32]byte
		for j := range ikm {
			ikm[j] = byte(1 + i*32 + j)
		}
		key, err := bls.SecretKeyFromRandom32Byte(ikm)
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = key
		pk := key.PublicKey()
		validatorPKs[i] = hex.EncodeToString(pk.Marshal())
		validators[i] = ValidatorInfo{Address: types.Address{byte(i + 1)}, PublicKey: pk}
	}
	vs := NewValidatorSet(validators, 1)
	message := H2V4CommitSigningMessage(identity, view, blockHash, changesHash)
	signatures := make([]blscommon.Signature, 3)
	for i := range signatures {
		signatures[i] = keys[i].Sign(message)
	}
	qc := QuorumCertificate{
		View:               view,
		BlockHash:          blockHash,
		AggregateSignature: bls.AggregateSignatures(signatures).Marshal(),
		Signers:            []bool{true, true, true, false},
	}
	envelope := H2V4Envelope{
		Identity:    identity,
		ChangesHash: changesHash,
		Message: &ConsensusMsg{Type: MsgDecide, Payload: &Decide{
			View: view, BlockHash: blockHash, CommitQC: qc,
		}},
	}
	if _, err := VerifyH2V4Decide(&envelope, vs); err != nil {
		t.Fatalf("valid H2-v4 Decide rejected: %v", err)
	}
	wrongDomain := envelope
	wrongDomain.ChangesHash[0] ^= 1
	if _, err := VerifyH2V4Decide(&wrongDomain, vs); err == nil {
		t.Fatal("H2-v4 Decide accepted under wrong changes hash")
	}

	wire, err := EncodeH2V4Envelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	fixture := h2V4FinalityFixture{
		Schema: "n42-h2-v4-finality-v1", ChainID: identity.ChainID,
		GenesisHash:  hex.EncodeToString(identity.GenesisHash[:]),
		ChangesHash:  hex.EncodeToString(changesHash[:]),
		ValidatorPKs: validatorPKs, EnvelopeHex: hex.EncodeToString(wire),
	}
	got, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/h2_v4_finality_v1.json")
	if os.IsNotExist(err) {
		t.Logf("fixture to pin:\n%s", got)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("H2-v4 finality fixture drift\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestServicePublishesEveryH2V4ConsensusEnvelope(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	p2p := &routingTestP2P{}
	service := &Service{p2p: p2p, ctx: context.Background(), h2V4Identity: &identity}
	items := crossClientH2Messages(t)
	for _, item := range items {
		service.publishH2V4Message(item.msg)
	}
	if len(p2p.topics) != len(items) {
		t.Fatalf("unexpected H2-v4 publications: %v", p2p.topics)
	}
	for i, item := range items {
		if p2p.topics[i] != h2V4GossipTopic {
			t.Fatalf("message %s published on %q", item.name, p2p.topics[i])
		}
		envelope, err := DecodeH2V4Gossip(p2p.payloads[i], identity)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(envelope.Message, item.msg) || envelope.ChangesHash != (types.Hash{}) {
			t.Fatalf("unexpected H2-v4 %s envelope: %+v", item.name, envelope)
		}
	}
}

func TestServiceAcceptsChainBoundH2V4Ingress(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	engine.EnableH2V4(identity)
	service := &Service{
		engine:       &HotStuff{engine: engine},
		h2V4Identity: &identity,
		rotor:        NewRotor(3),
	}

	timeout := &TimeoutMessage{
		View:      1,
		HighQC:    GenesisQC(),
		Sender:    1,
		Signature: setup.keys[1].Sign(H2V4TimeoutSigningMessage(identity, 1)).Marshal(),
	}
	gossip, err := EncodeH2V4Gossip(H2V4Envelope{
		Identity: identity, ChangesHash: types.Hash{},
		Message: &ConsensusMsg{Type: MsgTimeout, Payload: timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	from := peer.ID("rust-validator")
	if err := service.processH2V4GossipMessage(gossip, from); err != nil {
		t.Fatalf("valid Rust H2-v4 message rejected: %v", err)
	}
	if got, ok := service.rotor.LookupPeer(setup.vs, 1); !ok || got != from {
		t.Fatalf("H2-v4 sender was not registered: got (%q, %v)", got, ok)
	}
}

func TestServiceRejectsNonzeroChangesHashOnH2V4Ingress(t *testing.T) {
	setup := newTestSetup(t, 1)
	engine, _ := newTestEngine(t, setup, 0)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	engine.EnableH2V4(identity)
	service := &Service{engine: &HotStuff{engine: engine}, h2V4Identity: &identity}

	gossip, err := EncodeH2V4Gossip(H2V4Envelope{
		Identity: identity, ChangesHash: repeatedHash(0x44),
		Message: &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{
			View: 1, HighQC: GenesisQC(), Sender: 0,
			Signature: setup.keys[0].Sign(H2V4TimeoutSigningMessage(identity, 1)).Marshal(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.processH2V4GossipMessage(gossip, peer.ID("rust-validator")); err == nil {
		t.Fatal("nonzero H2-v4 changes hash was accepted")
	}
}
