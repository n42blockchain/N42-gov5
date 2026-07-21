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

type h2V4DomainFixture struct {
	Schema      string `json:"schema"`
	ChainID     uint64 `json:"chain_id"`
	GenesisHash string `json:"genesis_hash"`
	View        uint64 `json:"view"`
	BlockHash   string `json:"block_hash"`
	ChangesHash string `json:"changes_hash"`
	ProposalHex string `json:"proposal_hex"`
	VoteHex     string `json:"vote_hex"`
	CommitHex   string `json:"commit_hex"`
	TimeoutHex  string `json:"timeout_hex"`
	NewViewHex  string `json:"new_view_hex"`
}

func TestH2V4CrossClientSigningDomains(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	view := uint64(0x0102030405060708)
	blockHash := repeatedHash(0x22)
	changesHash := repeatedHash(0x33)
	fixture := h2V4DomainFixture{
		Schema: "n42-h2-v4-domains-v1", ChainID: identity.ChainID,
		GenesisHash: hex.EncodeToString(identity.GenesisHash[:]), View: view,
		BlockHash: hex.EncodeToString(blockHash[:]), ChangesHash: hex.EncodeToString(changesHash[:]),
		ProposalHex: hex.EncodeToString(H2V4ProposalSigningMessage(identity, view, blockHash, changesHash)),
		VoteHex:     hex.EncodeToString(H2V4VoteSigningMessage(identity, view, blockHash)),
		CommitHex:   hex.EncodeToString(H2V4CommitSigningMessage(identity, view, blockHash, changesHash)),
		TimeoutHex:  hex.EncodeToString(H2V4TimeoutSigningMessage(identity, view)),
		NewViewHex:  hex.EncodeToString(H2V4NewViewSigningMessage(identity, view)),
	}
	got, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/h2_v4_domains_v1.json")
	if os.IsNotExist(err) {
		t.Logf("fixture to pin:\n%s", got)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("H2-v4 domain fixture drift\nwant:\n%s\ngot:\n%s", want, got)
	}

	otherChain := identity
	otherChain.ChainID++
	if reflect.DeepEqual(
		H2V4CommitSigningMessage(identity, view, blockHash, changesHash),
		H2V4CommitSigningMessage(otherChain, view, blockHash, changesHash),
	) {
		t.Fatal("chain identity must change the signing preimage")
	}
}
