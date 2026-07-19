// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"reflect"
	"testing"
)

func TestReconcileIndicesNoOverlap(t *testing.T) {
	banned := ReconcileIndices(2, 
		[]IndexCommitment{{Index: 1, Commitment: h(11)}, {Index: 2, Commitment: h(12)}, {Index: 3, Commitment: h(13)}},
		[]IndexCommitment{{Index: 4, Commitment: h(14)}, {Index: 5, Commitment: h(15)}, {Index: 6, Commitment: h(16)}},
		[]IndexCommitment{{Index: 7, Commitment: h(17)}},
	)
	if len(banned) != 0 {
		t.Fatalf("banned = %v, want none", banned)
	}
}

// TestReconcileIndicesDetectsCrossSetOverlap: indices 3 and 5 each appear in
// two sets WITH THE SAME commitment — proof of a genuine duplicate
// submission (BLS signing is deterministic, so two honest observations of
// the same device's signature always hash identically) — and must be
// banned; index 6 (unique) must not be.
func TestReconcileIndicesDetectsCrossSetOverlap(t *testing.T) {
	banned := ReconcileIndices(2, 
		[]IndexCommitment{{Index: 1, Commitment: h(21)}, {Index: 2, Commitment: h(22)}, {Index: 3, Commitment: h(23)}},
		[]IndexCommitment{{Index: 3, Commitment: h(23)}, {Index: 4, Commitment: h(24)}, {Index: 5, Commitment: h(25)}},
		[]IndexCommitment{{Index: 5, Commitment: h(25)}, {Index: 6, Commitment: h(26)}},
	)
	want := []MobileIndex{3, 5}
	if !reflect.DeepEqual(banned, want) {
		t.Fatalf("banned = %v, want %v", banned, want)
	}
}

// TestReconcileIndicesRejectsUnauthenticatedForgery is the fix for the
// censorship vulnerability this commitment scheme exists to close: an index
// named by two sets with DIFFERING commitments must NOT be banned. At most
// one of the two claims can be genuine (a real device's signature hashes to
// exactly one value), and without the raw signature there is no way to tell
// which — so a lone dishonest node cannot silently censor an honest device
// merely by naming its index; it would need to also produce the matching
// commitment, which requires already knowing the real signature bytes.
func TestReconcileIndicesRejectsUnauthenticatedForgery(t *testing.T) {
	victim := MobileIndex(42)
	realCommitment := h(99)   // what the honest node that actually collected the signature computed
	forgedCommitment := h(66) // an attacker's claim, naming the same index but a DIFFERENT (fabricated) commitment

	banned := ReconcileIndices(2, 
		[]IndexCommitment{{Index: victim, Commitment: realCommitment}}, // the one honest observer
		[]IndexCommitment{{Index: victim, Commitment: forgedCommitment}}, // attacker names the index without the real signature
	)
	if len(banned) != 0 {
		t.Fatalf("banned = %v, want none — a non-matching (forged) claim must not censor the honest observation", banned)
	}
}

// TestReconcileIndicesUbiquitousIndex is the exact scenario
// certmerge_test.go's collapse test warns about: one index present in
// EVERY set, all with the SAME commitment (a genuine cross-node duplicate,
// not a forgery), must be caught.
func TestReconcileIndicesUbiquitousIndex(t *testing.T) {
	ubiquitousCommitment := h(199)
	sets := make([][]IndexCommitment, 5)
	for i := range sets {
		sets[i] = []IndexCommitment{
			{Index: 99, Commitment: ubiquitousCommitment},
			{Index: MobileIndex(i * 10), Commitment: h(byte(i*10 + 1))},
			{Index: MobileIndex(i*10 + 1), Commitment: h(byte(i*10 + 2))},
		}
	}
	banned := ReconcileIndices(2, sets[0], sets[1], sets[2], sets[3], sets[4])
	if len(banned) != 1 || banned[0] != 99 {
		t.Fatalf("banned = %v, want exactly [99]", banned)
	}
}

// TestReconcileIndicesRepeatWithinOneSetIsNotAConflict: a single set
// containing the same (index, commitment) pair twice (shouldn't happen from
// Collector.Freeze, which is built from a map, but defend against a
// hand-built input) must not by itself count as a cross-node conflict.
func TestReconcileIndicesRepeatWithinOneSetIsNotAConflict(t *testing.T) {
	c := h(1)
	banned := ReconcileIndices(2, 
		[]IndexCommitment{{Index: 1, Commitment: c}, {Index: 1, Commitment: c}},
		[]IndexCommitment{{Index: 2, Commitment: h(2)}},
	)
	if len(banned) != 0 {
		t.Fatalf("banned = %v, want none (repeat was within one set)", banned)
	}
}

func TestReconcileIndicesEmpty(t *testing.T) {
	if banned := ReconcileIndices(2); len(banned) != 0 {
		t.Fatalf("no sets at all: banned = %v, want none", banned)
	}
	if banned := ReconcileIndices(2, nil, []IndexCommitment{}); len(banned) != 0 {
		t.Fatalf("empty sets: banned = %v, want none", banned)
	}
}

// TestExcludeIndicesBeforeCloseCoexistsWithReconcile: the full pre-aggregation
// pipeline this design relies on — Freeze() announces (index, commitment)
// pairs, ReconcileIndices computes the banned set, ExcludeIndices removes
// it, THEN Close() aggregates over what remains. Confirms Collector's public
// surface composes correctly.
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

	mine := col.Freeze()
	if len(mine) != 6 {
		t.Fatalf("Freeze() = %d, want 6", len(mine))
	}
	victimIdx, _ := reg.Lookup(devices[2].pubkey)
	var victimCommitment [32]byte
	for _, ic := range mine {
		if ic.Index == victimIdx {
			victimCommitment = ic.Commitment
		}
	}

	// Simulate a peer that genuinely ALSO collected the victim's signature
	// (a real cross-node duplicate submission — the exact commitment must
	// match, since it's the identical deterministic BLS signature).
	otherDevice := newDevice(t)
	registerCommitted(t, reg, otherDevice.pubkey, otherDevice.pop())
	otherIdx, _ := reg.Lookup(otherDevice.pubkey)
	peerSet := []IndexCommitment{
		{Index: victimIdx, Commitment: victimCommitment},
		{Index: otherIdx, Commitment: h(200)},
	}

	banned := ReconcileIndices(2, mine, peerSet)
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

// TestFreezeSealsAgainstLateAdd: the TOCTOU fix — once Freeze() has run, a
// receipt arriving afterward must be rejected, not silently added to a
// cohort that was already snapshotted and announced.
func TestFreezeSealsAgainstLateAdd(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()
	col := NewCollector(reg, blockHash, number)
	d1 := newDevice(t)
	registerCommitted(t, reg, d1.pubkey, d1.pop())
	if _, err := col.Add(d1.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}

	_ = col.Freeze() // seals intake, same as announceIndex does

	d2 := newDevice(t)
	registerCommitted(t, reg, d2.pubkey, d2.pop())
	if _, err := col.Add(d2.receipt(blockHash, number, root)); err == nil {
		t.Fatal("Add after Freeze must be rejected — this is exactly the race the freeze closes")
	}
	if col.Size() != 1 {
		t.Fatalf("collector size = %d, want 1 (only the pre-freeze receipt)", col.Size())
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
	allSets := make([][]IndexCommitment, nodes)
	totalHonest := 0
	for n := 0; n < nodes; n++ {
		col := NewCollector(reg, blockHash, number)
		// The poison device's REAL signature (deterministic BLS) reaches
		// every node — a genuine cross-node duplicate submission, so every
		// node's Freeze() independently computes the SAME commitment for it.
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
		allSets[n] = col.Freeze()
	}

	// Reconciliation round: every node sees every announcement (a healthy,
	// fully-connected gossip round) and computes the SAME banned set.
	banned := ReconcileIndices(2, allSets[0], allSets[1], allSets[2], allSets[3], allSets[4])
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

	merged, dropped, invalid, err := MergeCerts(certs, reg)
	if err != nil {
		t.Fatalf("MergeCerts: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("with reconciliation, certs must already be disjoint — got %d dropped", len(dropped))
	}
	if len(invalid) != 0 {
		t.Fatalf("all inputs are genuine local certs — got %d flagged invalid", len(invalid))
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

// TestReconcileThreshold is the Layer 2C fix: with an f+1 threshold a single
// malicious reporter replaying a device's public (index, commitment) can no
// longer trigger a ban (count reaches 2, below f+1=3), while a genuine f+1
// agreement still bans.
func TestReconcileThreshold(t *testing.T) {
	victim := IndexCommitment{Index: 42, Commitment: h(7)}

	// The victim's honest source announces it, plus ONE malicious replayer.
	// threshold f+1 = 3 → not banned (attack fails).
	honest := []IndexCommitment{victim}
	replay := []IndexCommitment{victim}
	if banned := ReconcileIndices(3, honest, replay); len(banned) != 0 {
		t.Fatalf("single replayer (count=2) triggered a ban under threshold 3: %v", banned)
	}

	// The same two announcements DO ban under the old threshold-2 rule —
	// exactly the censorship Layer 2C closes.
	if banned := ReconcileIndices(2, honest, replay); len(banned) != 1 {
		t.Fatalf("threshold 2 should still ban on count=2, got %v", banned)
	}

	// A genuine 3-way agreement (device really reached 3 nodes) bans at f+1.
	if banned := ReconcileIndices(3, honest, replay, []IndexCommitment{victim}); len(banned) != 1 {
		t.Fatalf("f+1 genuine agreement should ban, got %v", banned)
	}

	// The floor: a degenerate threshold below 2 never bans a device reported
	// by only one node.
	if banned := ReconcileIndices(1, honest); len(banned) != 0 {
		t.Fatalf("threshold floor should not ban a single report, got %v", banned)
	}
}
