// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestReconcileWithReveals proves the Layer 2B property: a genuine multi-node
// submission (each reporter holds the raw device signature) bans at the
// threshold, while a replay collusion — reporters that copied the victim's
// PUBLIC commitment but never received the raw signature — cannot reach the
// threshold and is reported as misbehaving.
func TestReconcileWithReveals(t *testing.T) {
	reg := NewRegistry()
	dev := newDevice(t)
	idx := registerCommitted(t, reg, dev.pubkey, dev.pop())

	blockHash, number, root := h(1), uint64(1000), h(2)
	var sig [96]byte
	copy(sig[:], dev.receipt(blockHash, number, root).Signature[:])

	// Three honest reporters that genuinely received the device's signature.
	R1 := types.Address{0: 1}
	R2 := types.Address{0: 2}
	R3 := types.Address{0: 3}
	commitOf := func(r types.Address) []IndexCommitment {
		return []IndexCommitment{{Index: idx, Commitment: ReporterBoundCommitment(sig, r)}}
	}
	revealOf := func(r types.Address) ReporterReveal {
		return ReporterReveal{Reporter: r, Sigs: map[MobileIndex]RevealedSig{idx: {Sig: sig, Root: root}}}
	}

	// Genuine 3-way submission → banned at threshold 3.
	commits := map[types.Address][]IndexCommitment{R1: commitOf(R1), R2: commitOf(R2), R3: commitOf(R3)}
	reveals := map[types.Address]ReporterReveal{R1: revealOf(R1), R2: revealOf(R2), R3: revealOf(R3)}
	banned, misbehaved := ReconcileWithReveals(3, reg, blockHash, number, commits, reveals)
	if len(banned) != 1 || banned[0] != idx {
		t.Fatalf("genuine 3-way submission not banned: %v", banned)
	}
	if len(misbehaved) != 0 {
		t.Fatalf("honest reporters flagged as misbehaving: %v", misbehaved)
	}

	// Replay attack: R1 is the victim's honest source; R2 and R3 are attackers
	// that COPIED R1's commitment (bound to R1) and copied the raw sig from the
	// reveal, but present them under their OWN addresses. Their bound
	// commitment (R1-bound) does not match ReporterBoundCommitment(sig, R2/R3),
	// so they are not counted and are flagged.
	attackCommits := map[types.Address][]IndexCommitment{
		R1: commitOf(R1),
		R2: {{Index: idx, Commitment: ReporterBoundCommitment(sig, R1)}}, // copied R1's
		R3: {{Index: idx, Commitment: ReporterBoundCommitment(sig, R1)}}, // copied R1's
	}
	attackReveals := map[types.Address]ReporterReveal{
		R1: revealOf(R1),
		R2: revealOf(R2), // reveals the copied raw sig under R2
		R3: revealOf(R3),
	}
	banned, misbehaved = ReconcileWithReveals(3, reg, blockHash, number, attackCommits, attackReveals)
	if len(banned) != 0 {
		t.Fatalf("replay collusion censored the device: %v", banned)
	}
	if len(misbehaved) != 2 {
		t.Fatalf("replayers not flagged: misbehaved=%v", misbehaved)
	}

	// Fabrication: an attacker invents a self-consistent (commit, reveal) pair
	// with a bogus signature. It reproduces its own bound commitment but is not
	// the device's registered signature → rejected + flagged.
	var fake [96]byte
	fake[0] = 0xEE
	fabCommits := map[types.Address][]IndexCommitment{
		R1: commitOf(R1),
		R2: {{Index: idx, Commitment: ReporterBoundCommitment(fake, R2)}},
		R3: {{Index: idx, Commitment: ReporterBoundCommitment(fake, R3)}},
	}
	fabReveals := map[types.Address]ReporterReveal{
		R1: revealOf(R1),
		R2: {Reporter: R2, Sigs: map[MobileIndex]RevealedSig{idx: {Sig: fake, Root: root}}},
		R3: {Reporter: R3, Sigs: map[MobileIndex]RevealedSig{idx: {Sig: fake, Root: root}}},
	}
	banned, misbehaved = ReconcileWithReveals(3, reg, blockHash, number, fabCommits, fabReveals)
	if len(banned) != 0 {
		t.Fatalf("fabricated reveals censored the device: %v", banned)
	}
	if len(misbehaved) != 2 {
		t.Fatalf("fabricators not flagged: misbehaved=%v", misbehaved)
	}
}
