// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

func TestIndexAnnouncementRoundTrip(t *testing.T) {
	blockHash, number := h(7), uint64(555)
	var reporter types.Address
	reporter[19] = 0xAB
	indices := []IndexCommitment{
		{Index: 1, Commitment: h(101)},
		{Index: 5, Commitment: h(102)},
		{Index: 100, Commitment: h(103)},
		{Index: 9999, Commitment: h(104)},
	}

	payload, err := encodeIndexAnnouncement(blockHash, number, reporter, indices, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gotHash, gotNumber, gotReporter, gotIndices, err := decodeIndexAnnouncement(payload, 100000, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotHash != blockHash || gotNumber != number || gotReporter != reporter {
		t.Fatalf("header mismatch: hash=%v number=%d reporter=%v", gotHash, gotNumber, gotReporter)
	}
	if !reflect.DeepEqual(gotIndices, indices) {
		t.Fatalf("indices = %v, want %v", gotIndices, indices)
	}
}

func TestIndexAnnouncementTruncatedRejected(t *testing.T) {
	if _, _, _, _, err := decodeIndexAnnouncement([]byte{1, 2, 3}, 100, nil); err == nil {
		t.Fatal("truncated payload must be rejected")
	}
}

func TestCertAnnouncementRoundTrip(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	var devices []*device
	for i := 0; i < 5; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		devices = append(devices, d)
	}
	col := NewCollector(reg, blockHash, number)
	for _, d := range devices {
		if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	certs, err := col.Close(NowMs())
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v/%d", err, len(certs))
	}
	original := certs[0]

	var reporter types.Address
	reporter[19] = 0xCD
	payload := encodeCertAnnouncement(reporter, original, nil)

	gotReporter, gotCert, err := decodeCertAnnouncement(payload, reg.IndexBound(), nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotReporter != reporter {
		t.Fatalf("reporter = %v, want %v", gotReporter, reporter)
	}
	if gotCert.BlockHash != original.BlockHash || gotCert.BlockNumber != original.BlockNumber ||
		gotCert.ReceiptsRoot != original.ReceiptsRoot || gotCert.AggregateSig != original.AggregateSig ||
		gotCert.WindowClosedAt != original.WindowClosedAt || !reflect.DeepEqual(gotCert.SignerMask, original.SignerMask) {
		t.Fatalf("decoded cert != original:\n got=%+v\nwant=%+v", gotCert, original)
	}
	// The round-tripped cert must still verify — proves the wire format
	// preserves everything Verify() needs, not just byte-equal fields.
	if _, verr := gotCert.Verify(reg); verr != nil {
		t.Fatalf("round-tripped cert failed to verify: %v", verr)
	}
}

func TestCertAnnouncementTruncatedRejected(t *testing.T) {
	if _, _, err := decodeCertAnnouncement([]byte{1, 2, 3}, 100, nil); err == nil {
		t.Fatal("truncated payload must be rejected")
	}
}

func TestCertAnnouncementBadMaskRejected(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	d := newDevice(t)
	registerCommitted(t, reg, d.pubkey, d.pop())
	col := NewCollector(reg, blockHash, number)
	if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}
	certs, err := col.Close(NowMs())
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v/%d", err, len(certs))
	}
	var reporter types.Address
	payload := encodeCertAnnouncement(reporter, certs[0], nil)

	// A registry bound of 0 makes any non-empty mask decode-invalid —
	// confirms decodeCertAnnouncement actually validates the mask rather
	// than trusting the wire bytes blindly.
	if _, _, err := decodeCertAnnouncement(payload, 0, nil); err == nil {
		t.Fatal("a mask that fails DecodeMask validation must be rejected")
	}
}

// TestCohortAnnouncementAuthentication is the Layer 1 fix: a signed
// announcement round-trips and verifies, while an unauthenticated one (zero
// sig, or a signature that does not verify under the claimed reporter's key) is
// rejected — so an outsider can no longer spoof a reporter.
func TestCohortAnnouncementAuthentication(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	var reporter types.Address
	reporter[0] = 0xAB
	pkBytes := sk.PublicKey().Marshal()

	sign := func(msg []byte) []byte { return sk.Sign(msg).Marshal() }
	// verify: reporter must be our known validator AND the sig must verify.
	verify := func(rep types.Address, msg, sig []byte) bool {
		if rep != reporter {
			return false
		}
		pk, e := bls.PublicKeyFromBytes(pkBytes)
		if e != nil {
			return false
		}
		s, e := bls.SignatureFromBytes(sig)
		if e != nil {
			return false
		}
		return s.Verify(pk, msg)
	}

	blockHash := h(7)
	indices := []IndexCommitment{{Index: 1, Commitment: h(2)}, {Index: 5, Commitment: h(3)}}

	// Signed announcement authenticates.
	payload, err := encodeIndexAnnouncement(blockHash, 1000, reporter, indices, sign)
	if err != nil {
		t.Fatal(err)
	}
	bh, num, rep, gotIdx, err := decodeIndexAnnouncement(payload, 100000, verify)
	if err != nil {
		t.Fatalf("authenticated announcement rejected: %v", err)
	}
	if bh != blockHash || num != 1000 || rep != reporter || len(gotIdx) != 2 {
		t.Fatal("authenticated announcement fields mismatch")
	}

	// Unsigned (zero-sig) announcement is rejected by a real verifier.
	unsigned, _ := encodeIndexAnnouncement(blockHash, 1000, reporter, indices, nil)
	if _, _, _, _, err := decodeIndexAnnouncement(unsigned, 100000, verify); err == nil {
		t.Fatal("unsigned announcement accepted by an authenticating verifier")
	}

	// A signature from a DIFFERENT reporter address is rejected (can't spoof).
	var attacker types.Address
	attacker[0] = 0xCC
	spoof, _ := encodeIndexAnnouncement(blockHash, 1000, attacker, indices, sign)
	if _, _, _, _, err := decodeIndexAnnouncement(spoof, 100000, verify); err == nil {
		t.Fatal("announcement claiming a different reporter than the signer was accepted")
	}

	// Cert announcement authenticates the same way.
	local := newDevice(t)
	reg := NewRegistry()
	registerCommitted(t, reg, local.pubkey, local.pop())
	coll := NewCollector(reg, blockHash, 1000)
	if _, err := coll.Add(local.receipt(blockHash, 1000, h(9))); err != nil {
		t.Fatal(err)
	}
	certs, err := coll.Close(NowMs())
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v/%d", err, len(certs))
	}
	certPayload := encodeCertAnnouncement(reporter, certs[0], sign)
	if _, _, err := decodeCertAnnouncement(certPayload, reg.IndexBound(), verify); err != nil {
		t.Fatalf("authenticated cert announcement rejected: %v", err)
	}
	unsignedCert := encodeCertAnnouncement(reporter, certs[0], nil)
	if _, _, err := decodeCertAnnouncement(unsignedCert, reg.IndexBound(), verify); err == nil {
		t.Fatal("unsigned cert announcement accepted by an authenticating verifier")
	}
}
