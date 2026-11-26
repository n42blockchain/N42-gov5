// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"

	"github.com/n42blockchain/N42/internal/syncanchor"
)

// Two peers have range serving off (multistream refuses /1, no /2) and one
// serves ten blocks per reply, so one round issues several range requests.
// The refusing peers are asked once each, then skipped; they are neither
// scored nor backed off (§3.5: not speaking a protocol is not a fault).
func TestCatchUpSkipsPeersRefusingRangeProtocols(t *testing.T) {
	all := anchorChain(40, allVerified)
	net, _ := newAnchorNet(t, syncanchor.Enforce, all[:1])
	r1 := net.addPeer(100, harnessNoProtocol)
	r2 := net.addPeer(100, harnessNoProtocol)
	server := net.addPeer(40, harnessHonest, all...)
	server.maxServe = 10

	net.svc.CatchUpTo(40)

	if got := net.chain.headNumber(); got != 38 {
		t.Fatalf("head = %d, want 38 (39-40 wait for an anchor)", got)
	}
	if server.askedCount() < 4 {
		t.Fatalf("server asked %d times; the round should need several ranges", server.askedCount())
	}
	for _, r := range []*harnessPeer{r1, r2} {
		if got := r.askedCount(); got != 1 {
			t.Fatalf("refusing peer asked %d times, want 1", got)
		}
		if net.badResponses(r) != 0 {
			t.Fatal("refusing peer scored")
		}
		if st := net.svc.catchUpBook.snapshot(r.id); st.failures != 0 {
			t.Fatalf("refusing peer backed off: %+v", st)
		}
	}

	// A later round skips them from the start.
	net.svc.CatchUpTo(41)
	if r1.askedCount() != 1 || r2.askedCount() != 1 {
		t.Fatalf("refusing peers asked again: %d %d", r1.askedCount(), r2.askedCount())
	}
}
