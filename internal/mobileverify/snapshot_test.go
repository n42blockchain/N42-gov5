// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/crypto/bls"
)

// registerAndCommit adds n PoP-valid members to reg and commits them,
// returning the anchored root.
func registerAndCommit(t *testing.T, reg *Registry, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		var pub [48]byte
		copy(pub[:], sk.PublicKey().Marshal())
		var pop [96]byte
		copy(pop[:], sk.Sign(PoPMessage(pub)).Marshal())
		if _, _, err := reg.Register(pub, pop); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	reg.CommitEpoch()
}

// TestSnapshotSyncMatchesAnchoredRoot: node A builds a committed set and
// anchors root R; node B (fresh) imports A's snapshot and reproduces EXACTLY
// R — so a peer can bulk-sync membership and verify it against the header
// anchor with no trust in the serving node. Registration order on B is
// irrelevant: determinism comes from the export being in index order.
func TestSnapshotSyncMatchesAnchoredRoot(t *testing.T) {
	a := NewRegistry()
	registerAndCommit(t, a, 25)
	// A second epoch, to exercise multi-batch index continuity.
	registerAndCommit(t, a, 12)
	anchored := a.Root()

	blob := a.ExportCommitted()

	b := NewRegistry()
	got, err := b.ImportCommitted(blob)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got != anchored {
		t.Fatalf("imported root %s != anchored %s", got.Hex(), anchored.Hex())
	}
	if b.IndexBound() != a.IndexBound() {
		t.Fatalf("index bound %d != %d", b.IndexBound(), a.IndexBound())
	}
	// B can now serve membership proofs verifiable against the anchored root.
	if b.Count() != a.Count() {
		t.Fatalf("count %d != %d", b.Count(), a.Count())
	}
}

// TestSnapshotTamperDetected: a flipped byte in the snapshot yields a
// different root, so verification against the anchor rejects it.
func TestSnapshotTamperDetected(t *testing.T) {
	a := NewRegistry()
	registerAndCommit(t, a, 8)
	anchored := a.Root()
	blob := a.ExportCommitted()

	// Flip a byte inside the first pubkey (offset 9 = after the 9-byte header).
	blob[9] ^= 0xFF

	b := NewRegistry()
	got, err := b.ImportCommitted(blob)
	if err != nil {
		// A bad pubkey is also an acceptable rejection.
		return
	}
	if got == anchored {
		t.Fatal("tampered snapshot reproduced the anchored root")
	}
}

// TestImportRequiresEmpty: importing over a non-empty committed set is refused.
func TestImportRequiresEmpty(t *testing.T) {
	a := NewRegistry()
	registerAndCommit(t, a, 3)
	blob := a.ExportCommitted()
	if _, err := a.ImportCommitted(blob); err == nil {
		t.Fatal("import over a populated registry should fail")
	}
}

// Revoked entries remain in the append-only index for historical certificate
// verification, but they must not be restored into active membership or the
// accumulator tree by snapshot import.
func TestSnapshotPreservesRevokedMembership(t *testing.T) {
	a := NewRegistry()
	d := newDevice(t)
	idx := registerCommitted(t, a, d.pubkey, d.pop())
	anchored, revoked := a.Revoke(d.pubkey)
	if !revoked {
		t.Fatal("fixture device was not revoked")
	}

	b := NewRegistry()
	got, err := b.ImportCommitted(a.ExportCommitted())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got != anchored {
		t.Fatalf("imported root %s != post-revocation root %s", got.Hex(), anchored.Hex())
	}
	if b.Count() != 0 {
		t.Fatalf("revoked member restored as active: count=%d", b.Count())
	}
	if _, ok := b.Lookup(d.pubkey); ok {
		t.Fatal("revoked member restored into active lookup")
	}
	if b.IndexBound() != 1 {
		t.Fatalf("historical index bound = %d, want 1", b.IndexBound())
	}
	if _, err := b.PublicKey(idx); err != nil {
		t.Fatalf("revoked historical key was not retained: %v", err)
	}
}

func TestSnapshotRejectsTamperedPoP(t *testing.T) {
	a := NewRegistry()
	registerAndCommit(t, a, 1)
	blob := a.ExportCommitted()
	// First entry's PoP starts after header(9)+pubkey(48).
	blob[9+48] ^= 0x01

	b := NewRegistry()
	if _, err := b.ImportCommitted(blob); err == nil {
		t.Fatal("snapshot with tampered proof of possession was accepted")
	}
}
