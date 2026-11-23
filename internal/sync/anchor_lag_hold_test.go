// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/syncanchor"
)

// The lag executor's hold probe (anchorHolds) reports exactly the blocks
// enforce mode holds unanchored, and releases them with the anchor.
func TestAnchorHoldProbeCoversTheHeldTail(t *testing.T) {
	all := anchorChain(130, allVerified)
	net, _ := newAnchorNet(t, syncanchor.Enforce, all[:101])
	p := net.addPeer(130, harnessHonest, all...)
	net.svc.catchUpTo(130, []peer.ID{p.id})
	if got := net.chain.headNumber(); got != 128 {
		t.Fatalf("head = %d, want 128", got)
	}
	for n, want := range map[int]bool{128: false, 129: true, 130: true} {
		if got := net.svc.anchorHolds(all[n].Hash()); got != want {
			t.Fatalf("anchorHolds(%d) = %v, want %v", n, got, want)
		}
	}
	// Observe never holds.
	restore := syncanchor.SetForTest(syncanchor.Observe)
	if net.svc.anchorHolds(all[130].Hash()) {
		t.Fatal("observe mode held a block")
	}
	restore()

	net.svc.NoteCommitAnchor(all[130].Hash(), 130)
	waitHead(t, net.chain, 130)
	if net.svc.anchorHolds(all[129].Hash()) || net.svc.anchorHolds(all[130].Hash()) {
		t.Fatal("released blocks still held")
	}
}
