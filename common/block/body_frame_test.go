// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/rlp"
)

func sampleFrameHashes(n int) []types.Hash {
	out := make([]types.Hash, n)
	for i := range out {
		out[i] = crypto.Keccak256Hash([]byte{byte(i), byte(i >> 8), 0x46})
	}
	return out
}

// TestCompactFrameBodyRoundTrip: a frame body round-trips as version 0x02
// with its hashes and tail, and an ordinary body still writes version 0x01
// byte-identically to what it wrote before frame bodies existed.
func TestCompactFrameBodyRoundTrip(t *testing.T) {
	body := sampleBody(t, 0)
	body.FrameHashes = sampleFrameHashes(17)
	enc, err := body.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if enc[1] != compactFrameBodyVersion || !IsCompactBody(enc) {
		t.Fatalf("version byte %#x", enc[1])
	}
	got := new(Body)
	if err := got.UnmarshalCompact(enc); err != nil {
		t.Fatal(err)
	}
	if !got.IsFrameBody() || len(got.FrameHashes) != 17 || len(got.Txs) != 0 {
		t.Fatalf("frames %d txs %d", len(got.FrameHashes), len(got.Txs))
	}
	for i := range body.FrameHashes {
		if got.FrameHashes[i] != body.FrameHashes[i] {
			t.Fatalf("frame %d differs", i)
		}
	}
	assertBodiesEqual(t, body, got)

	// Truncations never panic and never decode.
	for n := 2; n < len(enc); n++ {
		if err := new(Body).UnmarshalCompact(enc[:n]); err == nil {
			t.Fatalf("truncated at %d decoded", n)
		}
	}

	// Old body: still version 1, decodes with no frame hashes.
	old := sampleBody(t, 5)
	oldEnc, err := old.MarshalCompact()
	if err != nil {
		t.Fatal(err)
	}
	if oldEnc[1] != compactBodyVersion {
		t.Fatalf("ordinary body version %#x", oldEnc[1])
	}
	back := new(Body)
	if err := back.UnmarshalCompact(oldEnc); err != nil {
		t.Fatal(err)
	}
	if back.IsFrameBody() {
		t.Fatal("ordinary body decoded as frame body")
	}
	assertBodiesEqual(t, old, back)
}

// TestFrameBlockRLPRoundTrip: the wire block of a frame body carries the
// hashes and no transactions; an ordinary block's wire form is unchanged
// by the new optional field.
func TestFrameBlockRLPRoundTrip(t *testing.T) {
	hdr := &Header{Number: uint256.NewInt(7), GasLimit: 30_000_000, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1)}
	full := sampleBody(t, 3)
	hashes := sampleFrameHashes(4)
	blk := NewFrameBlockFromReceipt(hdr, hashes, full.Txs, nil, nil).(*Block)
	if blk.TxHash() != FrameTxRoot(hashes) {
		t.Fatal("frame block TxHash is not FrameTxRoot")
	}
	want := crypto.Keccak256Hash(bytes.Join([][]byte{hashes[0][:], hashes[1][:], hashes[2][:], hashes[3][:]}, nil))
	if FrameTxRoot(hashes) != want {
		t.Fatal("FrameTxRoot is not keccak(concat)")
	}
	enc, err := rlp.EncodeToBytes(blk)
	if err != nil {
		t.Fatal(err)
	}
	var dec Block
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Hash() != blk.Hash() {
		t.Fatal("hash changed across RLP")
	}
	db := dec.Body().(*Body)
	if !db.IsFrameBody() || len(db.Txs) != 0 || len(db.FrameHashes) != 4 || db.FrameHashes[3] != hashes[3] {
		t.Fatalf("decoded frame body: frames %d txs %d", len(db.FrameHashes), len(db.Txs))
	}
	if BodyTxRoot(db, dec.Time()) != dec.TxHash() {
		t.Fatal("BodyTxRoot disagrees with header")
	}
	var reuse Block
	if _, _, err := reuse.DecodeRLPReusePool(rlp.NewStream(bytes.NewReader(enc), 0), nil); err != nil {
		t.Fatal(err)
	}
	if !reuse.Body().(*Body).IsFrameBody() {
		t.Fatal("reuse-pool decode dropped frame hashes")
	}

	// An ordinary block encodes exactly as the pre-P1b struct did.
	plain := NewBlockFromReceipt(hdr, full.Txs, nil, nil, nil).(*Block)
	encPlain, err := rlp.EncodeToBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	type legacyBlockRLP struct {
		Header    *Header
		TxData    [][]byte
		Verifiers []*Verify
		Rewards   []*Reward
		ZkProof   []byte `rlp:"optional"`
	}
	txData := make([][]byte, len(full.Txs))
	for i, tx := range full.Txs {
		txData[i], _ = tx.EthEncoded()
	}
	legacy, err := rlp.EncodeToBytes(&legacyBlockRLP{Header: plain.header, TxData: txData})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy, encPlain) {
		t.Fatal("ordinary block wire form changed")
	}

	// A frame body carrying transactions on the wire is refused.
	bad, _ := rlp.EncodeToBytes(&blockRLP{Header: plain.header, TxData: txData, FrameHashes: hashes})
	if err := rlp.DecodeBytes(bad, new(Block)); err == nil {
		t.Fatal("frame body with tx data accepted")
	}
}

// TestHeaderFrameTxRoot: a header marked with SetFrameTxRoot assembles with
// that root and no per-transaction root; the mark is not part of the hash.
func TestHeaderFrameTxRoot(t *testing.T) {
	hdr := &Header{Number: uint256.NewInt(3), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1)}
	before := CopyHeader(hdr).Hash()
	hashes := sampleFrameHashes(2)
	hdr.SetFrameTxRoot(FrameTxRoot(hashes))
	if CopyHeader(hdr).Hash() != before {
		t.Fatal("frame root mark changed the header hash")
	}
	blk := NewBlockFromReceipt(hdr, sampleBody(t, 2).Txs, nil, nil, nil)
	if blk.TxHash() != FrameTxRoot(hashes) {
		t.Fatal("assembly ignored the frame root")
	}
}

// TestFrameBlockWireSize: the pushed form of a frame block grows by one
// hash per frame and not with its transactions.
func TestFrameBlockWireSize(t *testing.T) {
	hdr := &Header{Number: uint256.NewInt(9), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1)}
	full := sampleBody(t, 40)
	small, _ := rlp.EncodeToBytes(NewFrameBlockFromReceipt(hdr, sampleFrameHashes(400), nil, nil, nil))
	withTxs, _ := rlp.EncodeToBytes(NewFrameBlockFromReceipt(hdr, sampleFrameHashes(400), full.Txs, nil, nil))
	if len(small) != len(withTxs) {
		t.Fatalf("frame block wire form depends on its transactions: %d vs %d", len(small), len(withTxs))
	}
	if len(small) > 400*33+1024 {
		t.Fatalf("400-frame block is %d bytes", len(small))
	}
}
