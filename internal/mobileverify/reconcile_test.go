// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"reflect"
	"testing"
)

func TestReconcileIndicesNoOverlap(t *testing.T) {
	banned := ReconcileIndices(
		[]MobileIndex{1, 2, 3},
		[]MobileIndex{4, 5, 6},
		[]MobileIndex{7},
	)
	if len(banned) != 0 {
		t.Fatalf("banned = %v, want none", banned)
	}
}

func TestReconcileIndicesDetectsCrossSetOverlap(t *testing.T) {
	banned := ReconcileIndices(
		[]MobileIndex{1, 2, 3},
		[]MobileIndex{3, 4, 5},
		[]MobileIndex{5, 6},
	)
	want := []MobileIndex{3, 5}
	if !reflect.DeepEqual(banned, want) {
		t.Fatalf("banned = %v, want %v", banned, want)
	}
}

// TestReconcileIndicesUbiquitousIndex is the exact scenario
// certmerge_test.go's collapse test warns about: one index present in
// EVERY set must be caught (it will conflict with every other set).
func TestReconcileIndicesUbiquitousIndex(t *testing.T) {
	sets := make([][]MobileIndex, 5)
	for i := range sets {
		sets[i] = []MobileIndex{99, MobileIndex(i * 10), MobileIndex(i*10 + 1)}
	}
	banned := ReconcileIndices(sets[0], sets[1], sets[2], sets[3], sets[4])
	if len(banned) != 1 || banned[0] != 99 {
		t.Fatalf("banned = %v, want exactly [99]", banned)
	}
}

// TestReconcileIndicesRepeatWithinOneSetIsNotAConflict: a single set
// containing the same index twice (shouldn't happen from Collector.Indices,
// which is built from a map, but defend against a hand-built input) must
// not by itself count as a cross-node conflict.
func TestReconcileIndicesRepeatWithinOneSetIsNotAConflict(t *testing.T) {
	banned := ReconcileIndices([]MobileIndex{1, 1, 1}, []MobileIndex{2})
	if len(banned) != 0 {
		t.Fatalf("banned = %v, want none (repeat was within one set)", banned)
	}
}

func TestReconcileIndicesEmpty(t *testing.T) {
	if banned := ReconcileIndices(); len(banned) != 0 {
		t.Fatalf("no sets at all: banned = %v, want none", banned)
	}
	if banned := ReconcileIndices(nil, []MobileIndex{}); len(banned) != 0 {
		t.Fatalf("empty sets: banned = %v, want none", banned)
	}
}

// TestExcludeIndicesBeforeCloseCoexistsWithReconcile: the full pre-aggregation
// pipeline this design relies on — Indices() announces, ReconcileIndices
// computes the banned set, ExcludeIndices removes it, THEN Close() aggregates
// over what remains. Confirms Collector's public surface composes correctly.
func TestExcludeIndicesBeforeCloseCoexistsWithReconcile(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()

	var devices []*device
	for i := 0; i < 6; i++ {
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

	mine := col.Indices()
	if len(mine) != 6 {
		t.Fatalf("Indices() = %d, want 6", len(mine))
	}
	victimIdx, _ := reg.Lookup(devices[2].pubkey)
	// Simulate a peer's announcement that also reports the same device —
	// the cross-node conflict this whole pipeline exists to catch.
	otherDevice := newDevice(t)
	registerCommitted(t, reg, otherDevice.pubkey, otherDevice.pop())
	otherIdx, _ := reg.Lookup(otherDevice.pubkey)
	peerSet := []MobileIndex{victimIdx, otherIdx}

	banned := ReconcileIndices(mine, peerSet)
	if len(banned) != 1 || banned[0] != victimIdx {
		t.Fatalf("banned = %v, want exactly [%d]", banned, victimIdx)
	}

	removed := col.ExcludeIndices(banned)
	if removed != 1 {
		t.Fatalf("ExcludeIndices removed %d, want 1", removed)
	}
	if col.Size() != 5 {
		t.Fatalf("collector size after exclusion = %d, want 5", col.Size())
	}

	certs, err := col.Close(NowMs())
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v/%d", err, len(certs))
	}
	signers, verr := certs[0].Verify(reg)
	if verr != nil {
		t.Fatalf("cert failed to verify: %v", verr)
	}
	if len(signers) != 5 {
		t.Fatalf("final cert signer count = %d, want 5 (excluded device must be absent)", len(signers))
	}
	for _, idx := range signers {
		if idx == victimIdx {
			t.Fatal("excluded device's signature still made it into the final cert")
		}
	}
}

// TestReconciliationPreventsUbiquitousConflictCollapse is the payoff test:
// run the SAME scenario as
// TestMergeCertsUbiquitousConflictDemonstratesWhyReconciliationMustRunFirst,
// but with the reconcile+exclude step actually applied before each node's
// Close(). Every honest signer now survives, and the poisoned index is
// excluded everywhere instead of collapsing the merge to one node.
func TestReconciliationPreventsUbiquitousConflictCollapse(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()

	poison := newDevice(t)
	registerCommitted(t, reg, poison.pubkey, poison.pop())
	poisonIdx, _ := reg.Lookup(poison.pubkey)

	const nodes = 5
	const honestPerNode = 10
	cols := make([]*Collector, nodes)
	allSets := make([][]MobileIndex, nodes)
	totalHonest := 0
	for n := 0; n < nodes; n++ {
		col := NewCollector(reg, blockHash, number)
		if _, err := col.Add(poison.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < honestPerNode; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
				t.Fatal(err)
			}
			totalHonest++
		}
		cols[n] = col
		allSets[n] = col.Indices()
	}

	// Reconciliation round: every node sees every announcement (a healthy,
	// fully-connected gossip round) and computes the SAME banned set.
	banned := ReconcileIndices(allSets[0], allSets[1], allSets[2], allSets[3], allSets[4])
	if len(banned) != 1 || banned[0] != poisonIdx {
		t.Fatalf("banned = %v, want exactly [%d]", banned, poisonIdx)
	}

	var certs []*MobileAttestationCert
	for n := 0; n < nodes; n++ {
		if removed := cols[n].ExcludeIndices(banned); removed != 1 {
			t.Fatalf("node %d: excluded %d, want 1", n, removed)
		}
		got, err := cols[n].Close(NowMs())
		if err != nil || len(got) != 1 {
			t.Fatalf("node %d close: %v/%d", n, err, len(got))
		}
		certs = append(certs, got[0])
	}

	merged, dropped, err := MergeCerts(certs, reg.IndexBound())
	if err != nil {
		t.Fatalf("MergeCerts: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("with reconciliation, certs must already be disjoint — got %d dropped", len(dropped))
	}
	signers, verr := merged.Verify(reg)
	if verr != nil {
		t.Fatalf("merged cert failed to verify: %v", verr)
	}
	if len(signers) != totalHonest {
		t.Fatalf("merged signer count = %d, want %d — ALL honest signers across ALL %d nodes must survive "+
			"(the poisoned index is excluded, not collateral-damaging honest devices)",
			len(signers), totalHonest, nodes)
	}
	for _, idx := range signers {
		if idx == poisonIdx {
			t.Fatal("poisoned index survived reconciliation — it must be excluded everywhere")
		}
	}
	t.Logf("with reconciliation: %d honest signers across %d nodes fully preserved (vs %d without it)",
		totalHonest, nodes, honestPerNode+1)
}
