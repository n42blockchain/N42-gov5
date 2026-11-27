// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	gosync "sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

func (p *harnessPeer) askedRanges() [][2]uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][2]uint64(nil), p.ranges...)
}

// A fresh node catches up 1..146 from genesis. Block n embeds the QC of
// n-2, so 145..146 stay held unanchored, and the canonical head trails the
// executed blocks by two (it moves only with a CommitQC). The round must ask
// commit_qc/1 once for the tail top and fetch no block twice: no re-fetch of
// executed blocks above the canonical head (gate 4b: 145..146 eight times).
func TestCatchUpHeldTailAsksCommitQCOnceWithoutRefetch(t *testing.T) {
	all := anchorChain(146, allVerified)
	net, _ := newAnchorNet(t, syncanchor.Enforce, all[:1])
	net.chain.canonLag = 2
	p1 := net.addPeer(146, harnessHonest, all...)
	p2 := net.addPeer(146, harnessHonest, all...)

	var (
		mu    gosync.Mutex
		asked []syncwire.CommitQCRequest
	)
	net.svc.commitQCAnchorOverride = func(_ peer.ID, req syncwire.CommitQCRequest) ([]*block.Header, comtypes.Hash, bool) {
		mu.Lock()
		asked = append(asked, req)
		mu.Unlock()
		// The QC in block 148 names 146: headers from 146 down to the request.
		return []*block.Header{all[146].Header().(*block.Header)}, all[146].Hash(), true
	}

	net.svc.CatchUpTo(146)

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0].Number != 146 || asked[0].Hash != all[146].Hash() {
		t.Fatalf("commit_qc requests = %+v, want exactly one for the tail top 146", asked)
	}
	// The requests (catchUpWindowBlocks windows) cover 1..146 once each:
	// no block is asked for twice.
	ranges := append(p1.askedRanges(), p2.askedRanges()...)
	next := uint64(1)
	for _, r := range ranges {
		if r[0] != next || r[1] < r[0] {
			t.Fatalf("range requests = %v, want consecutive windows from 1 with no block asked twice", ranges)
		}
		next = r[1] + 1
	}
	if next != 147 {
		t.Fatalf("range requests = %v, want them to end at 146", ranges)
	}
	if got := net.chain.appliedNumber(); got != 146 {
		t.Fatalf("applied head = %d, want 146", got)
	}
	net.chain.mu.Lock()
	for h, n := range net.chain.perHash {
		if n != 1 {
			net.chain.mu.Unlock()
			t.Fatalf("block %x executed %d times", h[:4], n)
		}
	}
	net.chain.mu.Unlock()
}
