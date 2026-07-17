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

// decodePacketForTest decodes encoded packet bytes back to a
// StreamPacket, for tests that need to feed PublishLocal.
func decodePacketForTest(encoded []byte) (*evmsdk.StreamPacket, error) {
	return evmsdk.DecodeStreamPacket(encoded)
}

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

func TestPacketCacheBoundsUnsignedForksAndCopiesReads(t *testing.T) {
	c := NewPacketCache(4)
	for i := 0; i < maxPeerPacketsPerHeight; i++ {
		var hash types.Hash
		hash[0] = byte(i + 1)
		if !c.PutPeer(hash, 10, []byte{byte(i + 1)}) {
			t.Fatalf("peer packet %d rejected before per-height cap", i)
		}
	}
	var overflow types.Hash
	overflow[0] = 0xff
	if c.PutPeer(overflow, 10, []byte{0xff}) {
		t.Fatal("peer packet above per-height fork cap was accepted")
	}

	first := types.Hash{0: 1}
	got, ok := c.Get(first)
	if !ok {
		t.Fatal("cached packet missing")
	}
	got[0] = 0xff
	again, _ := c.Get(first)
	if again[0] != 1 {
		t.Fatal("Get exposed mutable cache storage")
	}
}

func TestPacketServiceRejectsNumbersOutsideLocalHeadWindow(t *testing.T) {
	svc := NewPacketService(NewPacketCache(8), nil, "/test")
	svc.SetHeadNumber(func() uint64 { return 100 })
	for _, number := range []uint64{92, 100, 101, 102} {
		if !svc.acceptPeerNumber(number) {
			t.Fatalf("number %d inside local window was rejected", number)
		}
	}
	for _, number := range []uint64{91, 103, ^uint64(0)} {
		if svc.acceptPeerNumber(number) {
			t.Fatalf("number %d outside local window was accepted", number)
		}
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
