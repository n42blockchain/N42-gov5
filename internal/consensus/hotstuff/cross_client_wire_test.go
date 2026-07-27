// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"testing"

	"github.com/golang/snappy"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/blst"
)

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
