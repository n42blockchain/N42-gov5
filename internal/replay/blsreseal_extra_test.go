// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import "testing"

// TestBLSResealerPoolAndCommitteeSizeGetters covers the two trivial config
// accessors.
func TestBLSResealerPoolAndCommitteeSizeGetters(t *testing.T) {
	r, err := NewBLSResealer(BLSResealConfig{
		Seed:          [32]byte{0x01},
		PoolSize:      8,
		CommitteeSize: 4,
	})
	if err != nil {
		t.Fatalf("NewBLSResealer: %v", err)
	}
	if r.PoolSize() != 8 {
		t.Fatalf("PoolSize() = %d, want 8", r.PoolSize())
	}
	if r.CommitteeSize() != 4 {
		t.Fatalf("CommitteeSize() = %d, want 4", r.CommitteeSize())
	}
}

// NOTE (defect, not fixed here per instructions): signMembers is dead code.
// BuildCE/VerifyCE use a scalar-sum fast path (Σsk_i · H(m)) instead, and
// grepping the package, nothing else calls signMembers. Tested directly so
// its per-member signing behavior doesn't silently rot.
func TestBLSResealerSignMembersDirect(t *testing.T) {
	r, err := NewBLSResealer(BLSResealConfig{
		Seed:          [32]byte{0x02},
		PoolSize:      8,
		CommitteeSize: 4,
	})
	if err != nil {
		t.Fatalf("NewBLSResealer: %v", err)
	}

	members := []int{0, 1, 2, 3}
	msg := []byte("test message")
	sigs := r.signMembers(members, msg)
	if len(sigs) != len(members) {
		t.Fatalf("signMembers returned %d sigs, want %d", len(sigs), len(members))
	}
	for i, sig := range sigs {
		if sig == nil {
			t.Fatalf("signMembers sig[%d] is nil", i)
		}
		if !sig.Verify(r.pks[members[i]], msg) {
			t.Fatalf("signMembers sig[%d] does not verify against member %d's pubkey", i, members[i])
		}
	}
}
