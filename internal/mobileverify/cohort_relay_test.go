// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestIndexAnnouncementRoundTrip(t *testing.T) {
	blockHash, number := h(7), uint64(555)
	var reporter types.Address
	reporter[19] = 0xAB
	indices := []MobileIndex{1, 5, 100, 9999}

	payload, err := encodeIndexAnnouncement(blockHash, number, reporter, indices)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gotHash, gotNumber, gotReporter, gotIndices, err := decodeIndexAnnouncement(payload, 100000)
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
	if _, _, _, _, err := decodeIndexAnnouncement([]byte{1, 2, 3}, 100); err == nil {
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
	payload := encodeCertAnnouncement(reporter, original)

	gotReporter, gotCert, err := decodeCertAnnouncement(payload, reg.IndexBound())
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
	if _, _, err := decodeCertAnnouncement([]byte{1, 2, 3}, 100); err == nil {
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
	payload := encodeCertAnnouncement(reporter, certs[0])

	// A registry bound of 0 makes any non-empty mask decode-invalid —
	// confirms decodeCertAnnouncement actually validates the mask rather
	// than trusting the wire bytes blindly.
	if _, _, err := decodeCertAnnouncement(payload, 0); err == nil {
		t.Fatal("a mask that fails DecodeMask validation must be rejected")
	}
}
