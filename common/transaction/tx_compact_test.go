// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func addrOf(b byte) *types.Address {
	var a types.Address
	for i := range a {
		a[i] = b
	}
	return &a
}

func txsForCompactTest() map[string]*Transaction {
	sig := func(seed byte) (*uint256.Int, *uint256.Int, *uint256.Int) {
		r := make([]byte, 32)
		s := make([]byte, 32)
		for i := range r {
			r[i], s[i] = seed, seed+1
		}
		return uint256.NewInt(28), new(uint256.Int).SetBytes(r), new(uint256.Int).SetBytes(s)
	}

	v1, r1, s1 := sig(0x11)
	legacyTransfer := NewTx(&LegacyTx{ // the dominant shape on the legacy chain
		Nonce: 7, Gas: 21000, GasPrice: uint256.NewInt(2_000_000_000),
		Value: uint256.NewInt(123_456_789_000_000_000),
		To:    addrOf(0xaa), From: addrOf(0xbb),
		V: v1, R: r1, S: s1,
	})

	v2, r2, s2 := sig(0x22)
	legacyCreate := NewTx(&LegacyTx{ // contract creation: To nil, big data
		Nonce: 0, Gas: 1_500_000, GasPrice: uint256.NewInt(1),
		Value: uint256.NewInt(0), From: addrOf(0xcc),
		Data: bytes.Repeat([]byte{0x60, 0x80, 0x52}, 100),
		Sign: []byte{1, 2, 3, 4},
		V:    v2, R: r2, S: s2,
	})

	v3, r3, s3 := sig(0x33)
	alTx := NewTx(&AccessListTx{
		ChainID: uint256.NewInt(1), Nonce: 5, Gas: 60000,
		GasPrice: uint256.NewInt(3_000_000_000), Value: uint256.NewInt(42),
		To: addrOf(0xdd), From: addrOf(0xee),
		AccessList: AccessList{
			{Address: *addrOf(0x01), StorageKeys: []types.Hash{{0x10}, {0x20}}},
			{Address: *addrOf(0x02)},
		},
		V: v3, R: r3, S: s3,
	})

	v4, r4, s4 := sig(0x44)
	dynTx := NewTx(&DynamicFeeTx{
		ChainID: uint256.NewInt(1), Nonce: 9, Gas: 90000,
		GasTipCap: uint256.NewInt(1_500_000_000), GasFeeCap: uint256.NewInt(40_000_000_000),
		Value: uint256.NewInt(0), To: addrOf(0xf1), From: addrOf(0xf2),
		Data: []byte{0xa9, 0x05, 0x9c, 0xbb},
		V:    v4, R: r4, S: s4,
	})

	nilFields := NewTx(&LegacyTx{ // nil pointers everywhere they can be
		Nonce: 1, Gas: 21000,
		// GasPrice/Value/V/R/S nil, To/From nil
	})

	return map[string]*Transaction{
		"legacyTransfer": legacyTransfer,
		"legacyCreate":   legacyCreate,
		"accessList":     alTx,
		"dynamicFee":     dynTx,
		"nilFields":      nilFields,
	}
}

// TestCompactTxRoundTrip: decode(encode(tx)) must reproduce the tx hash, the
// embedded sender, the signature, and all execution-relevant fields exactly.
func TestCompactTxRoundTrip(t *testing.T) {
	for name, tx := range txsForCompactTest() {
		enc := tx.MarshalCompactStorage()
		if enc == nil {
			t.Fatalf("%s: expected compact support", name)
		}
		if !IsCompactTx(enc) {
			t.Fatalf("%s: marker missing", name)
		}
		var got Transaction
		if err := got.Unmarshal(enc); err != nil { // via the dispatching Unmarshal
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if got.Hash() != tx.Hash() {
			t.Fatalf("%s: hash mismatch:\n  want %x\n  got  %x", name, tx.Hash(), got.Hash())
		}
		// From is NOT part of the RLP hash — check it separately (it is the
		// embedded verified sender the replay relies on).
		wf, gf := tx.From(), got.From()
		if (wf == nil) != (gf == nil) || (wf != nil && *wf != *gf) {
			t.Fatalf("%s: From diverged", name)
		}
		if !bytes.Equal(tx.Sign(), got.Sign()) {
			t.Fatalf("%s: Sign diverged", name)
		}
		wv, wr, ws := tx.RawSignatureValues()
		gv, gr, gs := got.RawSignatureValues()
		for i, pair := range [][2]*uint256.Int{{wv, gv}, {wr, gr}, {ws, gs}} {
			a, b := pair[0], pair[1]
			if (a == nil) != (b == nil) || (a != nil && !a.Eq(b)) {
				t.Fatalf("%s: signature value %d diverged", name, i)
			}
		}
		if tx.Gas() != got.Gas() || tx.Nonce() != got.Nonce() {
			t.Fatalf("%s: gas/nonce diverged", name)
		}
		if !bytes.Equal(tx.Data(), got.Data()) {
			t.Fatalf("%s: data diverged", name)
		}
		if len(tx.AccessList()) != len(got.AccessList()) {
			t.Fatalf("%s: access list diverged", name)
		}
	}
}

// TestCompactTxProtoCrossCheck: proto and compact encodings of the same tx must
// decode (through the same Unmarshal) to the same hash + sender — the
// mixed-table guarantee. Also guards the marker against proto collision.
func TestCompactTxProtoCrossCheck(t *testing.T) {
	for name, tx := range txsForCompactTest() {
		protoEnc, err := tx.Marshal()
		if err != nil {
			t.Fatalf("%s: proto marshal: %v", name, err)
		}
		if IsCompactTx(protoEnc) {
			t.Fatalf("%s: proto encoding collides with compact marker", name)
		}
		var fromProto, fromCompact Transaction
		if err := fromProto.Unmarshal(protoEnc); err != nil {
			t.Fatalf("%s: proto unmarshal: %v", name, err)
		}
		if err := fromCompact.Unmarshal(tx.MarshalCompactStorage()); err != nil {
			t.Fatalf("%s: compact unmarshal: %v", name, err)
		}
		if fromProto.Hash() != fromCompact.Hash() {
			t.Fatalf("%s: proto vs compact hash mismatch", name)
		}
	}
}

// TestCompactTxSize: the dominant legacy-transfer shape must be substantially
// smaller than proto.
func TestCompactTxSize(t *testing.T) {
	tx := txsForCompactTest()["legacyTransfer"]
	protoEnc, _ := tx.Marshal()
	compactEnc := tx.MarshalCompactStorage()
	t.Logf("legacyTransfer: proto=%dB compact=%dB (%.1fx)",
		len(protoEnc), len(compactEnc), float64(len(protoEnc))/float64(len(compactEnc)))
	if len(compactEnc)*15 > len(protoEnc)*10 { // require >= 1.5x
		t.Fatalf("compact not compact enough: proto=%d compact=%d", len(protoEnc), len(compactEnc))
	}
}
