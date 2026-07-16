// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestAccumulatorCrossNodeDeterminism is the property the whole pivot to
// an on-chain accumulator buys: two nodes that admit the SAME pending set
// (in any order) and commit derive the IDENTICAL root and the IDENTICAL
// index assignment — so a cert built on one node verifies on the other.
// This is exactly what the first-writer-wins map sketch could not
// guarantee.
func TestAccumulatorCrossNodeDeterminism(t *testing.T) {
	const n = 12
	devices := make([]*device, n)
	for i := range devices {
		devices[i] = newDevice(t)
	}

	regA := NewRegistry()
	regB := NewRegistry()

	// Node A admits in forward order; node B in reverse — same set.
	for i := 0; i < n; i++ {
		if _, _, err := regA.Register(devices[i].pubkey, devices[i].pop()); err != nil {
			t.Fatal(err)
		}
	}
	for i := n - 1; i >= 0; i-- {
		if _, _, err := regB.Register(devices[i].pubkey, devices[i].pop()); err != nil {
			t.Fatal(err)
		}
	}

	rootA, nA := regA.CommitEpoch()
	rootB, nB := regB.CommitEpoch()

	if nA != n || nB != n {
		t.Fatalf("committed counts = %d/%d, want %d", nA, nB, n)
	}
	if rootA != rootB {
		t.Fatalf("roots diverged despite identical membership:\n A: %x\n B: %x", rootA[:8], rootB[:8])
	}
	if rootA == (types.Hash{}) {
		t.Fatal("root is empty")
	}
	// Every device resolves to the same index on both nodes.
	for _, d := range devices {
		ia, oka := regA.Lookup(d.pubkey)
		ib, okb := regB.Lookup(d.pubkey)
		if !oka || !okb || ia != ib {
			t.Fatalf("index mismatch for a device: A=(%d,%v) B=(%d,%v)", ia, oka, ib, okb)
		}
	}

	// A cert produced on A verifies on B: build a cohort, aggregate on A,
	// verify against B's registry.
	blockHash, number, root := h(0xAB), uint64(100), h(0x0C)
	col := NewCollector(regA, blockHash, number)
	for _, d := range devices {
		if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	certs, err := col.Close(1)
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v certs=%d", err, len(certs))
	}
	if _, err := certs[0].Verify(regB); err != nil {
		t.Fatalf("cert from node A failed verification on node B: %v", err)
	}
}

// TestAccumulatorAppendStability confirms indices never move across epochs:
// a second batch appends above the first, leaving earlier indices intact.
func TestAccumulatorAppendStability(t *testing.T) {
	reg := NewRegistry()
	d1 := newDevice(t)
	if _, _, err := reg.Register(d1.pubkey, d1.pop()); err != nil {
		t.Fatal(err)
	}
	reg.CommitEpoch()
	i1, _ := reg.Lookup(d1.pubkey)

	root1 := reg.Root()
	d2 := newDevice(t)
	if _, _, err := reg.Register(d2.pubkey, d2.pop()); err != nil {
		t.Fatal(err)
	}
	reg.CommitEpoch()

	// d1's index is unchanged; d2 appended above; root advanced.
	if got, _ := reg.Lookup(d1.pubkey); got != i1 {
		t.Fatalf("d1 index moved: %d -> %d", i1, got)
	}
	if i2, _ := reg.Lookup(d2.pubkey); i2 != 1 {
		t.Fatalf("d2 index = %d, want 1", i2)
	}
	if reg.Root() == root1 {
		t.Fatal("root did not advance after the second batch")
	}
}

// TestPendingCannotAttest confirms a registered-but-uncommitted device is
// rejected by the collector until CommitEpoch.
func TestPendingCannotAttest(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	if _, _, err := reg.Register(d.pubkey, d.pop()); err != nil {
		t.Fatal(err)
	}
	col := NewCollector(reg, h(0x01), 1)
	if _, err := col.Add(d.receipt(h(0x01), 1, h(0x02))); err == nil {
		t.Fatal("pending (uncommitted) device attested")
	}
	reg.CommitEpoch()
	if _, err := col.Add(d.receipt(h(0x01), 1, h(0x02))); err != nil {
		t.Fatalf("committed device rejected: %v", err)
	}
}
