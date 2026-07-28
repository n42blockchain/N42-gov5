// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/golang/snappy"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/blst"
)

type crossClientH2Fixture struct {
	Schema           string             `json:"schema"`
	SignatureDomains map[string]string  `json:"signature_domains"`
	VoteGossipHex    string             `json:"vote_gossip_hex"`
	Messages         []h2FixtureMessage `json:"messages"`
}

type h2FixtureMessage struct {
	Name    string `json:"name"`
	Type    uint8  `json:"type"`
	WireHex string `json:"wire_hex"`
}

func repeatedHash(b byte) (out types.Hash) {
	for i := range out {
		out[i] = b
	}
	return out
}

func mustFixtureSignature(t *testing.T) []byte {
	t.Helper()
	sig, err := hex.DecodeString(
		"96ac1723aa056aa092d9891b0b2bfd7cdca6139feb5fdbed2c33670f08743402" +
			"63676e73576d7686ed386866d9f58ba10c7a6f2835ccb63b94eb16f5e1540c2" +
			"8285757a85f0298614f189b2c625cb165cb4eb8dd1d9770b711972cd5f13a9bf5",
	)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func crossClientH2Messages(t *testing.T) []struct {
	name string
	msg  *ConsensusMsg
} {
	t.Helper()
	sig := mustFixtureSignature(t)
	hashA := repeatedHash(0xaa)
	hashB := repeatedHash(0xbb)
	txRoot := repeatedHash(0xcc)
	qc := QuorumCertificate{
		View:               0x0102030405060708,
		BlockHash:          hashA,
		AggregateSignature: append([]byte(nil), sig...),
		Signers:            []bool{true, false, true, true, false, true, true, false, true},
	}
	tc := TimeoutCertificate{
		View:               0x1112131415161718,
		AggregateSignature: append([]byte(nil), sig...),
		Signers:            []bool{true, true, false, true, false, false, true},
		HighQC:             qc.Clone(),
	}

	return []struct {
		name string
		msg  *ConsensusMsg
	}{
		{"proposal", &ConsensusMsg{Type: MsgProposal, Payload: &Proposal{
			View: 0x2122232425262728, BlockHash: hashB, JustifyQC: qc.Clone(), Proposer: 3,
			Signature: append([]byte(nil), sig...), PrepareQC: func() *QuorumCertificate { q := qc.Clone(); return &q }(), TxRootHash: txRoot,
		}}},
		{"vote", &ConsensusMsg{Type: MsgVote, Payload: &Vote{
			View: 0x2122232425262728, BlockHash: hashB, Voter: 4,
			Signature: append([]byte(nil), sig...), HighTC: func() *TimeoutCertificate { c := tc.Clone(); return &c }(),
		}}},
		{"commit_vote", &ConsensusMsg{Type: MsgCommitVote, Payload: &CommitVote{
			View: 0x2122232425262728, BlockHash: hashB, Voter: 5, Signature: append([]byte(nil), sig...),
		}}},
		{"prepare_qc", &ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{
			View: 0x2122232425262728, BlockHash: hashB, QC: qc.Clone(),
		}}},
		{"timeout", &ConsensusMsg{Type: MsgTimeout, Payload: &TimeoutMessage{
			View: 0x3132333435363738, HighQC: qc.Clone(), Sender: 6,
			Signature: append([]byte(nil), sig...), HighTC: func() *TimeoutCertificate { c := tc.Clone(); return &c }(),
		}}},
		{"new_view", &ConsensusMsg{Type: MsgNewView, Payload: &NewViewMsg{
			View: 0x4142434445464748, TimeoutCert: tc.Clone(), Leader: 7, Signature: append([]byte(nil), sig...),
		}}},
		{"decide", &ConsensusMsg{Type: MsgDecide, Payload: &Decide{
			View: 0x2122232425262728, BlockHash: hashB, CommitQC: qc.Clone(),
		}}},
	}
}

func TestCrossClientH2V1Vectors(t *testing.T) {
	view := uint64(0x2122232425262728)
	hash := repeatedHash(0xbb)
	fixture := crossClientH2Fixture{
		Schema: "n42-h2-gov5-v1",
		SignatureDomains: map[string]string{
			"prepare_hex":  hex.EncodeToString(SigningMessage(view, hash)),
			"commit_hex":   hex.EncodeToString(CommitSigningMessage(view, hash)),
			"timeout_hex":  hex.EncodeToString(TimeoutSigningMessage(0x3132333435363738)),
			"new_view_hex": hex.EncodeToString(NewViewSigningMessage(0x4142434445464748)),
		},
	}

	for _, item := range crossClientH2Messages(t) {
		wire, err := EncodeConsensusMsg(item.msg)
		if err != nil {
			t.Fatalf("encode %s: %v", item.name, err)
		}
		decoded, err := DecodeConsensusMsg(wire)
		if err != nil {
			t.Fatalf("decode %s: %v", item.name, err)
		}
		if !reflect.DeepEqual(decoded, item.msg) {
			t.Fatalf("%s round trip mismatch\nwant: %#v\n got: %#v", item.name, item.msg, decoded)
		}
		fixture.Messages = append(fixture.Messages, h2FixtureMessage{
			Name: item.name, Type: uint8(item.msg.Type), WireHex: hex.EncodeToString(wire),
		})
		if item.name == "vote" {
			fixture.VoteGossipHex = hex.EncodeToString(snappy.Encode(nil, wire))
		}
	}

	got, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/cross_client_h2_v1.json")
	if os.IsNotExist(err) {
		t.Logf("fixture to pin:\n%s", got)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cross-client H2 fixture drift\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// This fixture is consumed verbatim by the Rust n42-network tests. Keep the
// raw custom-SSZ envelope and its raw-Snappy gossip representation stable.
func TestRustCrossClientVoteWireFixture(t *testing.T) {
	var ikm [32]byte
	for i := range ikm {
		ikm[i] = 0x11
	}
	key, err := blst.SecretKeyFromRandom32Byte(ikm)
	if err != nil {
		t.Fatal(err)
	}
	var hash types.Hash
	for i := range hash {
		hash[i] = 0xaa
	}
	message := &ConsensusMsg{
		Type: MsgVote,
		Payload: &Vote{
			View:      0x0102030405060708,
			BlockHash: hash,
			Voter:     0x0a0b0c0d,
			Signature: key.Sign([]byte("gov5 codec test")).Marshal(),
		},
	}
	raw, err := EncodeConsensusMsg(message)
	if err != nil {
		t.Fatal(err)
	}
	const wantRaw = "0298000000080706050403020120000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0d0c0b0a6000000096ac1723aa056aa092d9891b0b2bfd7cdca6139feb5fdbed2c33670f0874340263676e73576d7686ed386866d9f58ba10c7a6f2835ccb63b94eb16f5e1540c28285757a85f0298614f189b2c625cb165cb4eb8dd1d9770b711972cd5f13a9bf500000000"
	if got := hex.EncodeToString(raw); got != wantRaw {
		t.Fatalf("raw cross-client fixture changed:\n%s", got)
	}

	compressed := snappy.Encode(nil, raw)
	const wantSnappy = "9d01440298000000080706050403020120000000aa7a0100f06b0d0c0b0a6000000096ac1723aa056aa092d9891b0b2bfd7cdca6139feb5fdbed2c33670f0874340263676e73576d7686ed386866d9f58ba10c7a6f2835ccb63b94eb16f5e1540c28285757a85f0298614f189b2c625cb165cb4eb8dd1d9770b711972cd5f13a9bf500000000"
	if got := hex.EncodeToString(compressed); got != wantSnappy {
		t.Fatalf("snappy cross-client fixture changed:\n%s", got)
	}
}

func TestRustCrossClientHeaderExtraFixture(t *testing.T) {
	var ikm [32]byte
	for i := range ikm {
		ikm[i] = 0x11
	}
	key, err := blst.SecretKeyFromRandom32Byte(ikm)
	if err != nil {
		t.Fatal(err)
	}
	var hash types.Hash
	for i := range hash {
		hash[i] = 0x22
	}
	extra, err := buildHeaderExtra(9, &QuorumCertificate{
		View:               5,
		BlockHash:          hash,
		AggregateSignature: key.Sign([]byte("header qc")).Marshal(),
		Signers:            []bool{true, false, true, true, false, false, true},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "4e3432480900000000000000050000000000000020000000222222222222222222222222222222222222222222222222222222222222222260000000ab1a550ff30189be533cd89266cfc55a36fdb893a46c0531acf61de31d2a77b04901e94971eec839432f034d31ad01d30f7441b94a3e7c79caed3a90337daf2f2b2998fdec51b3dca3ec731e799e552ffed6298cd8c5962299d0c717551113b00300000007004d000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	if got := hex.EncodeToString(extra); got != want {
		t.Fatalf("header extra cross-client fixture changed:\n%s", got)
	}
}
