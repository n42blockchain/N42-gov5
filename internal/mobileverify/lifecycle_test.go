// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestRevokeRemovesActiveMembershipKeepsHistory(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	idx := registerCommitted(t, reg, d.pubkey, d.pop())
	rootBefore := reg.Root()

	// A cert built while the device is active.
	blockHash, number, root := h(0xA1), uint64(5), h(0x0C)
	col := NewCollector(reg, blockHash, number)
	if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}
	certs, _ := col.Close(1)
	if len(certs) != 1 {
		t.Fatalf("certs = %d", len(certs))
	}

	// Revoke: root advances, active count drops, membership proof fails,
	// but the historical cert still verifies (pubkey retained at the index).
	newRoot, revoked := reg.Revoke(d.pubkey)
	if !revoked || newRoot == rootBefore {
		t.Fatalf("revoke = (root %x, %v), want advanced root, true", newRoot[:4], revoked)
	}
	if reg.Count() != 0 {
		t.Fatalf("active count after revoke = %d, want 0", reg.Count())
	}
	if _, ok := reg.Lookup(d.pubkey); ok {
		t.Fatal("revoked device still looks up (could attest)")
	}
	if _, err := reg.MembershipProof(d.pubkey); err == nil {
		t.Fatal("revoked device still proves membership")
	}
	// Index still resolves for historical certs.
	if _, err := reg.PublicKey(idx); err != nil {
		t.Fatalf("revoked index no longer resolves for history: %v", err)
	}
	if _, err := certs[0].Verify(reg); err != nil {
		t.Fatalf("historical cert stopped verifying after revoke: %v", err)
	}

	// A revoked device cannot attest to a new block.
	col2 := NewCollector(reg, h(0xB2), 6)
	if _, err := col2.Add(d.receipt(h(0xB2), 6, root)); err == nil {
		t.Fatal("revoked device attested to a new block")
	}
}

func TestRotateContinuesUnderNewKey(t *testing.T) {
	reg := NewRegistry()
	oldD := newDevice(t)
	registerCommitted(t, reg, oldD.pubkey, oldD.pop())

	newD := newDevice(t)
	if _, err := reg.Rotate(oldD.pubkey, newD.pubkey, newD.pop()); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// Old key is gone from active membership; new key is pending until commit.
	if _, ok := reg.Lookup(oldD.pubkey); ok {
		t.Fatal("old key still active after rotate")
	}
	if reg.PendingCount() != 1 {
		t.Fatalf("pending after rotate = %d, want 1", reg.PendingCount())
	}
	reg.CommitEpoch()
	if _, ok := reg.Lookup(newD.pubkey); !ok {
		t.Fatal("new key not active after commit")
	}
	if reg.Count() != 1 {
		t.Fatalf("active count = %d, want 1 (old revoked, new active)", reg.Count())
	}
}

func TestPruneInactive(t *testing.T) {
	reg := NewRegistry()
	stale := newDevice(t)
	fresh := newDevice(t)
	registerCommitted(t, reg, stale.pubkey, stale.pop())
	registerCommitted(t, reg, fresh.pubkey, fresh.pop())

	// Sleeps make the ordering deterministic against a ms-granularity clock:
	// both devices' commit lastSeen precede the cutoff; fresh's mark follows it.
	time.Sleep(5 * time.Millisecond)
	cutoff := NowMs()
	time.Sleep(5 * time.Millisecond)
	reg.MarkActive(fresh.pubkey)

	_, pruned := reg.PruneInactive(cutoff)
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if _, ok := reg.Lookup(stale.pubkey); ok {
		t.Fatal("stale device survived prune")
	}
	if _, ok := reg.Lookup(fresh.pubkey); !ok {
		t.Fatal("fresh device wrongly pruned")
	}
	if reg.Count() != 1 {
		t.Fatalf("active count after prune = %d, want 1", reg.Count())
	}
}

func TestRevokeUnknownIsNoop(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	root := reg.Root()
	got, revoked := reg.Revoke(d.pubkey)
	if revoked || got != root {
		t.Fatal("revoking an unknown device changed state")
	}
	_ = types.Hash{}
}
