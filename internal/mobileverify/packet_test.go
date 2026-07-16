// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func TestPacketCacheWindowEviction(t *testing.T) {
	c := NewPacketCache(4)
	mk := func(b byte) types.Hash { var h types.Hash; h[0] = b; return h }

	for i := uint64(1); i <= 10; i++ {
		c.Put(mk(byte(i)), i, []byte{byte(i)})
	}
	// Window 4 with max 10: numbers < 6 evicted.
	if _, ok := c.Get(mk(5)); ok {
		t.Fatal("block 5 should be evicted (window 4, head 10)")
	}
	for i := uint64(6); i <= 10; i++ {
		data, ok := c.Get(mk(byte(i)))
		if !ok || data[0] != byte(i) {
			t.Fatalf("block %d missing from window", i)
		}
	}
	// Idempotent put does not duplicate.
	before := c.Len()
	c.Put(mk(10), 10, []byte{99})
	if c.Len() != before {
		t.Fatal("idempotent put changed cache size")
	}
	if data, _ := c.Get(mk(10)); data[0] != 10 {
		t.Fatal("idempotent put overwrote content-addressed entry")
	}
}

// buildTestPacket makes a minimal but WELL-FORMED packet: real header
// bytes whose hash matches BlockHash — the self-certification invariant
// ValidatePacketBytes enforces.
func buildTestPacket(t *testing.T, number uint64) (types.Hash, []byte) {
	t.Helper()
	hdr := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
	}
	hb, err := hdr.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	pkt := &evmsdk.StreamPacket{
		BlockHash:   hdr.Hash(),
		HeaderRLP:   hb,
		ReadLogData: []byte{0, 0, 0, 0}, // empty read log (entry_count=0)
	}
	encoded, err := evmsdk.EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatal(err)
	}
	return hdr.Hash(), encoded
}

func TestValidatePacketBytes(t *testing.T) {
	wantHash, encoded := buildTestPacket(t, 42)

	gotHash, gotNum, raw, err := ValidatePacketBytes(encoded)
	if err != nil {
		t.Fatalf("valid packet rejected: %v", err)
	}
	if gotHash != wantHash || gotNum != 42 || len(raw) != len(encoded) {
		t.Fatalf("identity mismatch: %x %d", gotHash[:4], gotNum)
	}

	// Forged BlockHash (header doesn't hash to it) must be rejected.
	hdr := &block.Header{Number: uint256.NewInt(42), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	hb, _ := hdr.Marshal()
	var wrong types.Hash
	wrong[0] = 0xEE
	forged, err := evmsdk.EncodeStreamPacket(&evmsdk.StreamPacket{
		BlockHash:   wrong,
		HeaderRLP:   hb,
		ReadLogData: []byte{0, 0, 0, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ValidatePacketBytes(forged); err == nil {
		t.Fatal("forged block hash accepted")
	}

	// Garbage must be rejected.
	if _, _, _, err := ValidatePacketBytes([]byte{1, 2, 3}); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestPacketServicePublishLocalCacheOnly(t *testing.T) {
	cache := NewPacketCache(8)
	svc := NewPacketService(cache, nil, "/n42/test/mobileverify_packet")

	hdr := &block.Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
	hb, _ := hdr.Marshal()
	pkt := &evmsdk.StreamPacket{BlockHash: hdr.Hash(), HeaderRLP: hb, ReadLogData: []byte{0, 0, 0, 0}}

	if err := svc.PublishLocal(pkt, 7); err != nil {
		t.Fatalf("cache-only publish: %v", err)
	}
	data, ok := svc.Get(hdr.Hash())
	if !ok {
		t.Fatal("published packet not in cache")
	}
	// Round-trip: the cached bytes decode back to the same identity.
	gotHash, gotNum, _, err := ValidatePacketBytes(data)
	if err != nil || gotHash != hdr.Hash() || gotNum != 7 {
		t.Fatalf("cached packet corrupt: %v %x %d", err, gotHash[:4], gotNum)
	}
}
