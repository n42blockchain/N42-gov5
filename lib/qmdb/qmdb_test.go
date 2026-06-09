package qmdb

import (
	"encoding/binary"
	"testing"
)

func key(i uint64) Hash {
	var h Hash
	binary.BigEndian.PutUint64(h[24:], i)
	binary.BigEndian.PutUint64(h[0:], i*0x9E3779B97F4A7C15)
	return h
}
func val(i uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, i+1)
	return b
}

// TestBasic exercises set/get/update/delete and that the root tracks state.
func TestBasic(t *testing.T) {
	tr := New()
	if tr.Root() != nullHash {
		t.Fatal("empty root should be null")
	}
	tr.Set(key(1), val(10))
	tr.Set(key(2), val(20))
	r1 := tr.Root()
	if r1 == nullHash {
		t.Fatal("non-empty root should not be null")
	}
	if v, ok := tr.Get(key(1)); !ok || string(v) != string(val(10)) {
		t.Fatal("get key1")
	}
	// update changes the root and the value
	tr.Set(key(1), val(11))
	if tr.Root() == r1 {
		t.Fatal("update should change root")
	}
	if v, _ := tr.Get(key(1)); string(v) != string(val(11)) {
		t.Fatal("updated value")
	}
	// delete
	tr.Delete(key(2))
	if _, ok := tr.Get(key(2)); ok {
		t.Fatal("key2 should be gone")
	}
}

// TestAppendOnlyInvariant: every Set consumes a new slot (cursor only grows) and
// an update deactivates the prior slot rather than rewriting it.
func TestAppendOnlyInvariant(t *testing.T) {
	tr := New()
	tr.Set(key(1), val(1)) // slot 0
	tr.Set(key(1), val(2)) // slot 1, slot 0 deactivated
	tr.Set(key(1), val(3)) // slot 2
	if tr.NextSlot() != 3 {
		t.Fatalf("expected 3 slots consumed, got %d", tr.NextSlot())
	}
	if tr.LiveCount() != 1 {
		t.Fatalf("expected 1 live key, got %d", tr.LiveCount())
	}
	// slots 0 and 1 must be deactivated, slot 2 live
	if tr.entries[0].active || tr.entries[1].active || !tr.entries[2].active {
		t.Fatal("append-only deactivation wrong")
	}
}

// TestMembershipProof: every live key proves to the root; tampering fails.
func TestMembershipProof(t *testing.T) {
	tr := New()
	const n = 5000 // > 2 twigs (2*2048) to exercise the upper tree
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	// update + delete some to create dead slots
	for i := uint64(0); i < n; i += 7 {
		tr.Set(key(i), val(i+1000))
	}
	for i := uint64(1); i < n; i += 13 {
		tr.Delete(key(i))
	}
	root := tr.Root()

	checked := 0
	for i := uint64(0); i < n; i++ {
		want, ok := tr.Get(key(i))
		p, pok := tr.GetProof(key(i))
		if ok != pok {
			t.Fatalf("key %d: Get=%v GetProof=%v", i, ok, pok)
		}
		if !ok {
			continue
		}
		if string(p.Value) != string(want) {
			t.Fatalf("key %d: proof value mismatch", i)
		}
		if !VerifyProof(root, p) {
			t.Fatalf("key %d: proof did not verify", i)
		}
		// tamper -> must fail
		bad := *p
		bad.Value = append([]byte{}, p.Value...)
		bad.Value[0] ^= 0xff
		if VerifyProof(root, &bad) {
			t.Fatalf("key %d: tampered proof verified", i)
		}
		checked++
	}
	if checked < n/2 {
		t.Fatalf("too few live keys checked: %d", checked)
	}
}

// TestDeterminism: the same update sequence yields the same root every time.
func TestDeterminism(t *testing.T) {
	run := func() Hash {
		tr := New()
		rng := uint64(0x243F6A8885A308D3)
		next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
		for r := 0; r < 200; r++ {
			k := next() % 1000
			switch next() % 3 {
			case 2:
				tr.Delete(key(k))
			default:
				tr.Set(key(k), val(next()))
			}
		}
		return tr.Root()
	}
	if run() != run() {
		t.Fatal("same op sequence produced different roots (non-deterministic)")
	}
}

// TestSnapshotPositionPreserving: exporting the slot log and rebuilding yields
// the identical root, and proofs verify against the rebuilt tree. This is the
// snapshot-sync property for an append-only store (ship positions, not just the
// key set).
func TestSnapshotPositionPreserving(t *testing.T) {
	tr := New()
	for i := uint64(0); i < 6000; i++ {
		tr.Set(key(i), val(i))
	}
	for i := uint64(0); i < 6000; i += 5 {
		tr.Set(key(i), val(i+9))
	}
	for i := uint64(2); i < 6000; i += 11 {
		tr.Delete(key(i))
	}
	root := tr.Root()

	rebuilt := FromSnapshotLog(tr.SnapshotLog())
	if rebuilt.Root() != root {
		t.Fatalf("position-preserving rebuild root mismatch:\n  orig=%x\n  rebuilt=%x", root, rebuilt.Root())
	}
	if rebuilt.LiveCount() != tr.LiveCount() {
		t.Fatalf("live count mismatch: %d vs %d", rebuilt.LiveCount(), tr.LiveCount())
	}
	// proofs verify against the rebuilt root
	for i := uint64(0); i < 6000; i += 17 {
		if _, ok := rebuilt.Get(key(i)); !ok {
			continue
		}
		p, _ := rebuilt.GetProof(key(i))
		if !VerifyProof(root, p) {
			t.Fatalf("rebuilt proof for key %d did not verify", i)
		}
	}
}

// TestOrderDependence documents the deliberate QMDB property: the same live key
// set reached via a different insertion order has a DIFFERENT root (entries sit
// in different slots). This is why snapshots ship positions and cross-node
// agreement comes from identical replay, not from a canonical-by-key-set tree.
func TestOrderDependence(t *testing.T) {
	a := New()
	a.Set(key(1), val(1))
	a.Set(key(2), val(2))

	b := New()
	b.Set(key(2), val(2))
	b.Set(key(1), val(1))

	// same live set
	av, _ := a.Get(key(1))
	bv, _ := b.Get(key(1))
	if string(av) != string(bv) || a.LiveCount() != b.LiveCount() {
		t.Fatal("setup: live sets should match")
	}
	// but different roots (history-dependent)
	if a.Root() == b.Root() {
		t.Fatal("expected order-dependent roots to differ (QMDB property)")
	}
}
