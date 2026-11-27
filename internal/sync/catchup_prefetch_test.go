// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// A long gap is fetched in consecutive windows, each block requested and
// executed once, and the round keeps going past the window cap.
func TestCatchUpWindowsCoverGapOnce(t *testing.T) {
	t.Cleanup(setCatchUpWindowForTest(64))
	all := harnessBase(300)
	chain := newHarnessChain(all[:1]...)
	net := newHarnessNet(t, chain)
	p := net.addPeer(300, harnessHonest, all...)

	net.svc.catchUpTo(300, []peer.ID{p.id})

	if got := chain.headNumber(); got != 300 {
		t.Fatalf("head = %d, want 300", got)
	}
	ranges := p.askedRanges()
	next := uint64(1)
	for _, r := range ranges {
		if r[0] != next || r[1]-r[0]+1 > 64 {
			t.Fatalf("ranges = %v, want consecutive windows of at most 64 from 1", ranges)
		}
		next = r[1] + 1
	}
	if next != 301 {
		t.Fatalf("ranges = %v, want them to end at 300", ranges)
	}
	chain.mu.Lock()
	defer chain.mu.Unlock()
	for h, n := range chain.perHash {
		if n != 1 {
			t.Fatalf("block %x executed %d times", h[:4], n)
		}
	}
}

// While a window executes the next one is already in transfer: with a slow
// peer and slow execution the total is close to the larger of the two, not
// their sum.
func TestCatchUpPrefetchOverlapsFetchWithImport(t *testing.T) {
	t.Cleanup(setCatchUpWindowForTest(50))
	all := harnessBase(200)
	chain := newHarnessChain(all[:1]...)
	chain.insertDelay = 2 * time.Millisecond // 100 ms a window
	net := newHarnessNet(t, chain)
	p := net.addPeer(200, harnessHonest, all...)
	p.perBlock = 2 * time.Millisecond // 100 ms a window

	t0 := time.Now()
	net.svc.catchUpTo(200, []peer.ID{p.id})
	took := time.Since(t0)

	if got := chain.headNumber(); got != 200 {
		t.Fatalf("head = %d, want 200", got)
	}
	// Serial: 4 x (100 + 100) ms = 800 ms. Overlapped: ~500 ms.
	if took > 700*time.Millisecond {
		t.Fatalf("catch-up took %v, want the fetch of a window to overlap the previous import (< 700ms)", took)
	}
	if n := p.askedCount(); n != 4 {
		t.Fatalf("peer asked %d times, want 4 (each window once)", n)
	}
}

// A prefetch is used only for exactly the request it was made for.
func TestCatchUpPrefetchMatchesExactRequest(t *testing.T) {
	all := harnessBase(40)
	chain := newHarnessChain(all[:1]...)
	net := newHarnessNet(t, chain)
	t.Cleanup(setCatchUpWindowForTest(10))
	p := net.addPeer(40, harnessHonest, all...)
	q := net.addPeer(40, harnessHonest, all...)

	net.svc.startCatchUpPrefetch(p.id, 11, 40)
	if got := net.svc.takeCatchUpPrefetch(q.id, rangeReq(11, 10, 1)); got != nil {
		t.Fatal("prefetch from p served a request to q")
	}
	net.svc.startCatchUpPrefetch(p.id, 11, 40)
	if got := net.svc.takeCatchUpPrefetch(p.id, rangeReq(12, 10, 1)); got != nil {
		t.Fatal("prefetch at 11 served a request at 12")
	}
	net.svc.startCatchUpPrefetch(p.id, 11, 40)
	got := net.svc.takeCatchUpPrefetch(p.id, rangeReq(11, 10, 1))
	if got == nil {
		t.Fatal("matching prefetch not served")
	}
	<-got.done
	if got.err != nil || len(got.blocks) != 10 || got.blocks[0].Number64().Uint64() != 11 {
		t.Fatalf("prefetch result: %d blocks, err %v", len(got.blocks), got.err)
	}
	// The window is bounded by the target.
	net.svc.startCatchUpPrefetch(p.id, 35, 40)
	if got := net.svc.takeCatchUpPrefetch(p.id, rangeReq(35, 6, 1)); got == nil || got.count != 6 {
		t.Fatal("prefetch near the target not bounded by it")
	}
	net.svc.startCatchUpPrefetch(p.id, 41, 40)
	if got := net.svc.takeCatchUpPrefetch(p.id, rangeReq(41, 1, 1)); got != nil {
		t.Fatal("prefetch above the target")
	}
}
