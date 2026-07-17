// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// twoNodeCerts builds a registry with n1+n2 committed devices, simulates two
// IDC nodes each collecting a DISJOINT half via separate Collectors for the
// SAME block+root, and returns their independently-closed local certs plus
// the registry (for verification) and the full device list (for assertions).
func twoNodeCerts(t *testing.T, blockHash types.Hash, number uint64, root types.Hash, n1, n2 int) (certA, certB *MobileAttestationCert, reg *Registry, devicesA, devicesB []*device) {
	t.Helper()
	reg = NewRegistry()
	for i := 0; i < n1; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		devicesA = append(devicesA, d)
	}
	for i := 0; i < n2; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		devicesB = append(devicesB, d)
	}

	colA := NewCollector(reg, blockHash, number)
	for _, d := range devicesA {
		if _, err := colA.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("node A add: %v", err)
		}
	}
	certsA, err := colA.Close(NowMs())
	if err != nil || len(certsA) != 1 {
		t.Fatalf("node A close: certs=%d err=%v", len(certsA), err)
	}

	colB := NewCollector(reg, blockHash, number)
	for _, d := range devicesB {
		if _, err := colB.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("node B add: %v", err)
		}
	}
	certsB, err := colB.Close(NowMs())
	if err != nil || len(certsB) != 1 {
		t.Fatalf("node B close: certs=%d err=%v", len(certsB), err)
	}
	return certsA[0], certsB[0], reg, devicesA, devicesB
}

// TestMergeCertsDisjointUnion is the routine happy path: two IDC nodes each
// collected a disjoint half of the same block's cohort; MergeCerts combines
// them into one cert covering every device, and that cert VERIFIES —
// proving the merge is cryptographically sound, not just "the code runs".
func TestMergeCertsDisjointUnion(t *testing.T) {
	blockHash, root := h(1), h(2)
	certA, certB, reg, devicesA, devicesB := twoNodeCerts(t, blockHash, 100, root, 40, 35)

	merged, dropped, err := MergeCerts([]*MobileAttestationCert{certA, certB}, reg.IndexBound())
	if err != nil {
		t.Fatalf("MergeCerts: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("disjoint merge should drop nothing, got %d", len(dropped))
	}

	signers, verr := merged.Verify(reg)
	if verr != nil {
		t.Fatalf("merged cert failed to verify: %v", verr)
	}
	if len(signers) != len(devicesA)+len(devicesB) {
		t.Fatalf("merged signer count = %d, want %d", len(signers), len(devicesA)+len(devicesB))
	}
	// Every device from both nodes must be present in the merged signer set.
	present := make(map[MobileIndex]bool, len(signers))
	for _, idx := range signers {
		present[idx] = true
	}
	for _, d := range append(append([]*device{}, devicesA...), devicesB...) {
		idx, ok := reg.Lookup(d.pubkey)
		if !ok || !present[idx] {
			t.Fatalf("device index %d missing from merged cert", idx)
		}
	}
}

// TestMergeCertsSingleInputPassthrough: merging one cert returns it as-is.
func TestMergeCertsSingleInputPassthrough(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()
	col := NewCollector(reg, blockHash, number)
	for i := 0; i < 5; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	certs, err := col.Close(NowMs())
	if err != nil || len(certs) != 1 {
		t.Fatalf("close: %v/%d", err, len(certs))
	}

	merged, dropped, err := MergeCerts([]*MobileAttestationCert{certs[0]}, reg.IndexBound())
	if err != nil {
		t.Fatalf("MergeCerts single: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatal("single-input merge must drop nothing")
	}
	if merged != certs[0] {
		t.Fatal("single-input merge should return the same cert, not a rebuilt copy")
	}
}

// TestMergeCertsMismatchRejected: certs for a different block or receipts
// root must never be silently combined.
func TestMergeCertsMismatchRejected(t *testing.T) {
	buildOne := func(blockHash types.Hash, number uint64, root types.Hash, reg *Registry) *MobileAttestationCert {
		col := NewCollector(reg, blockHash, number)
		for i := 0; i < 5; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
				t.Fatal(err)
			}
		}
		certs, err := col.Close(NowMs())
		if err != nil || len(certs) != 1 {
			t.Fatalf("close: %v/%d", err, len(certs))
		}
		return certs[0]
	}

	reg := NewRegistry()
	certA := buildOne(h(1), 100, h(2), reg)
	certOtherBlock := buildOne(h(9), 100, h(2), reg)
	if _, _, err := MergeCerts([]*MobileAttestationCert{certA, certOtherBlock}, reg.IndexBound()); err == nil {
		t.Fatal("merging certs for different blocks must fail")
	}

	certOtherRoot := buildOne(h(1), 100, h(3), reg)
	if _, _, err := MergeCerts([]*MobileAttestationCert{certA, certOtherRoot}, reg.IndexBound()); err == nil {
		t.Fatal("merging certs for different receipts roots must fail")
	}
}

// TestMergeCertsOverlapFallbackKeepsLarger: the defense-in-depth path when
// reconciliation did NOT run and two certs share a signer. The smaller
// conflicting cert is dropped whole (reported in droppedForOverlap); the
// larger one's signers all survive and the result still verifies.
func TestMergeCertsOverlapFallbackKeepsLarger(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()

	var big, small []*device
	for i := 0; i < 20; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		big = append(big, d)
	}
	for i := 0; i < 3; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		small = append(small, d)
	}
	shared := big[0] // present in BOTH cohorts: the simulated protocol violation

	colBig := NewCollector(reg, blockHash, number)
	for _, d := range big {
		if _, err := colBig.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	certsBig, err := colBig.Close(NowMs())
	if err != nil || len(certsBig) != 1 {
		t.Fatalf("big close: %v / %d", err, len(certsBig))
	}

	colSmall := NewCollector(reg, blockHash, number)
	if _, err := colSmall.Add(shared.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}
	for _, d := range small {
		if _, err := colSmall.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	certsSmall, err := colSmall.Close(NowMs())
	if err != nil || len(certsSmall) != 1 {
		t.Fatalf("small close: %v / %d", err, len(certsSmall))
	}

	merged, dropped, err := MergeCerts([]*MobileAttestationCert{certsBig[0], certsSmall[0]}, reg.IndexBound())
	if err != nil {
		t.Fatalf("MergeCerts: %v", err)
	}
	if len(dropped) != 1 {
		t.Fatalf("expected exactly 1 dropped cert (the smaller conflicting one), got %d", len(dropped))
	}

	signers, verr := merged.Verify(reg)
	if verr != nil {
		t.Fatalf("merged cert failed to verify: %v", verr)
	}
	if len(signers) != len(big) {
		t.Fatalf("merged signer count = %d, want %d (only the larger cohort survives)", len(signers), len(big))
	}
}

// TestMergeCertsUbiquitousConflictDemonstratesWhyReconciliationMustRunFirst
// documents (with a real, passing assertion) the exact vulnerability that
// motivates reconcile.go: if ONE index is present in every candidate cert
// (as if reconciliation never ran), the overlap fallback can only ever keep
// ONE surviving cert — every OTHER node's honest, non-conflicting signers
// are discarded as collateral damage. This is why exclusion happens BEFORE
// aggregation (Collector.ExcludeIndices), not as a post-hoc merge policy.
func TestMergeCertsUbiquitousConflictDemonstratesWhyReconciliationMustRunFirst(t *testing.T) {
	blockHash, number, root := h(1), uint64(100), h(2)
	reg := NewRegistry()

	poison := newDevice(t)
	registerCommitted(t, reg, poison.pubkey, poison.pop())

	const nodes = 5
	const honestPerNode = 10
	var certs []*MobileAttestationCert
	totalHonest := 0
	for n := 0; n < nodes; n++ {
		col := NewCollector(reg, blockHash, number)
		// The poisoned index reaches EVERY node — exactly the scenario
		// reconciliation exists to prevent.
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
		got, err := col.Close(NowMs())
		if err != nil || len(got) != 1 {
			t.Fatalf("node %d close: %v/%d", n, err, len(got))
		}
		certs = append(certs, got[0])
	}

	merged, dropped, err := MergeCerts(certs, reg.IndexBound())
	if err != nil {
		t.Fatalf("MergeCerts: %v", err)
	}
	signers, verr := merged.Verify(reg)
	if verr != nil {
		t.Fatalf("merged cert failed to verify: %v", verr)
	}

	// The vulnerability, demonstrated: only ONE node's cohort survives
	// (honestPerNode + the poison signer), not nodes*honestPerNode+1.
	if len(signers) != honestPerNode+1 {
		t.Fatalf("got %d signers, want exactly %d (one node's worth) — "+
			"if this changes, re-examine whether the collapse this test documents still holds",
			len(signers), honestPerNode+1)
	}
	if len(dropped) != nodes-1 {
		t.Fatalf("expected %d of %d nodes' certs dropped due to the ubiquitous conflict, got %d",
			nodes-1, nodes, len(dropped))
	}
	t.Logf("without reconciliation: %d honest signers across %d nodes collapsed to %d surviving signers — "+
		"this is exactly why ExcludeIndices must run before Close(), not after", totalHonest+1, nodes, len(signers))
}

// TestSelectMajorityBucketPicksLargest: a genuine multi-root divergence
// (distinct from routing fragmentation, which MergeCerts already resolved)
// keeps only the largest bucket.
func TestSelectMajorityBucketPicksLargest(t *testing.T) {
	blockHash, number := h(1), uint64(100)
	reg := NewRegistry()

	build := func(root types.Hash, n int) *MobileAttestationCert {
		col := NewCollector(reg, blockHash, number)
		for i := 0; i < n; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
				t.Fatal(err)
			}
		}
		certs, err := col.Close(NowMs())
		if err != nil || len(certs) != 1 {
			t.Fatalf("close: %v/%d", err, len(certs))
		}
		return certs[0]
	}

	majority := build(h(10), 30)
	minorityA := build(h(11), 5)
	minorityB := build(h(12), 12)

	winner, discarded, err := SelectMajorityBucket([]*MobileAttestationCert{minorityA, majority, minorityB}, reg.IndexBound())
	if err != nil {
		t.Fatalf("SelectMajorityBucket: %v", err)
	}
	if winner.ReceiptsRoot != majority.ReceiptsRoot {
		t.Fatalf("winner root = %x, want the 30-signer majority %x", winner.ReceiptsRoot, majority.ReceiptsRoot)
	}
	if len(discarded) != 2 {
		t.Fatalf("discarded = %d, want 2", len(discarded))
	}
}

// TestSelectMajorityBucketDeterministicTie: equal-size buckets must resolve
// identically on every node — required for cross-fleet agreement on which
// bucket is "the" cert without any coordination beyond seeing the same set.
func TestSelectMajorityBucketDeterministicTie(t *testing.T) {
	blockHash, number := h(1), uint64(100)
	reg := NewRegistry()

	build := func(root types.Hash, n int) *MobileAttestationCert {
		col := NewCollector(reg, blockHash, number)
		for i := 0; i < n; i++ {
			d := newDevice(t)
			registerCommitted(t, reg, d.pubkey, d.pop())
			if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
				t.Fatal(err)
			}
		}
		certs, err := col.Close(NowMs())
		if err != nil || len(certs) != 1 {
			t.Fatalf("close: %v/%d", err, len(certs))
		}
		return certs[0]
	}

	certLow := build(h(5), 10)
	certHigh := build(h(200), 10)

	winner1, _, err := SelectMajorityBucket([]*MobileAttestationCert{certLow, certHigh}, reg.IndexBound())
	if err != nil {
		t.Fatal(err)
	}
	winner2, _, err := SelectMajorityBucket([]*MobileAttestationCert{certHigh, certLow}, reg.IndexBound())
	if err != nil {
		t.Fatal(err)
	}
	if winner1.ReceiptsRoot != winner2.ReceiptsRoot {
		t.Fatal("tie-break depends on input order — must be deterministic regardless of arrival order")
	}
	if winner1.ReceiptsRoot != certLow.ReceiptsRoot {
		t.Fatalf("tie-break winner = %x, want the lexicographically smaller root %x", winner1.ReceiptsRoot, certLow.ReceiptsRoot)
	}
}
