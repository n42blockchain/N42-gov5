// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import "testing"

func TestPortableSnapshotRoundTripAndIntegrity(t *testing.T) {
	tree := New()
	for i := uint64(0); i < 2050; i++ {
		tree.Set(interopKey(i), interopValue(i))
	}
	tree.Set(interopKey(7), interopValue(1_000_007))
	tree.Delete(interopKey(9))

	snapshot := &PortableSnapshot{
		ChainID:     1143,
		GenesisHash: interopKey(0x11),
		BlockNumber: 42,
		BlockHash:   interopKey(0x22),
		Root:        tree.Root(),
		NextSlot:    tree.NextSlot(),
		Entries:     tree.SnapshotLog(),
	}
	encoded, err := MarshalPortableSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalPortableSnapshot(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ChainID != snapshot.ChainID || decoded.GenesisHash != snapshot.GenesisHash || decoded.BlockNumber != snapshot.BlockNumber || decoded.BlockHash != snapshot.BlockHash || decoded.Root != snapshot.Root || decoded.NextSlot != snapshot.NextSlot {
		t.Fatal("portable snapshot metadata changed across round trip")
	}
	if got := FromSnapshotLog(decoded.Entries).Root(); got != snapshot.Root {
		t.Fatalf("portable snapshot root = %x, want %x", got, snapshot.Root)
	}

	encoded[len(encoded)/2] ^= 0x80
	if _, err := UnmarshalPortableSnapshot(encoded); err == nil {
		t.Fatal("tampered portable snapshot was accepted")
	}
}

func TestPortableSnapshotRejectsSparseSlots(t *testing.T) {
	snapshot := &PortableSnapshot{
		NextSlot: 2,
		Entries: []SlotEntry{
			{Slot: 0, KeyHash: interopKey(0), Value: interopValue(0), Active: true},
			{Slot: 2, KeyHash: interopKey(2), Value: interopValue(2), Active: true},
		},
	}
	if _, err := MarshalPortableSnapshot(snapshot); err == nil {
		t.Fatal("sparse portable snapshot was accepted")
	}
}
