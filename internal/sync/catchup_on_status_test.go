// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

func statusAt(h uint64) *sync_pb.Status {
	return &sync_pb.Status{CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(h))}
}

// A validated status from a peer ahead of the head starts the catch-up at
// once (no wait for the 8 s tick) on a leader-driven chain.
func TestCatchUpOnStatusStartsCatchUp(t *testing.T) {
	all := harnessBase(40)
	chain := newHarnessChain(all[:1]...)
	net := newHarnessNet(t, chain)
	net.svc.cfg.blockImportNotifier = &recordingNotifier{}
	net.addPeer(40, harnessHonest, all...)

	net.svc.catchUpOnStatus(statusAt(40))
	deadline := time.Now().Add(5 * time.Second)
	for chain.headNumber() != 40 {
		if time.Now().After(deadline) {
			t.Fatalf("head = %d after the status kick, want 40", chain.headNumber())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// No kick when the peer is not ahead, or on a chain without the HotStuff
// import notifier (classic chains sync through initial sync).
func TestCatchUpOnStatusNoKick(t *testing.T) {
	all := harnessBase(40)

	chain := newHarnessChain(all[:11]...)
	net := newHarnessNet(t, chain)
	net.svc.cfg.blockImportNotifier = &recordingNotifier{}
	p := net.addPeer(40, harnessHonest, all...)
	net.svc.catchUpOnStatus(statusAt(10))
	net.svc.catchUpOnStatus(nil)

	chain2 := newHarnessChain(all[:11]...)
	net2 := newHarnessNet(t, chain2)
	p2 := net2.addPeer(40, harnessHonest, all...)
	net2.svc.catchUpOnStatus(statusAt(40))

	time.Sleep(100 * time.Millisecond)
	if chain.headNumber() != 10 || p.askedCount() != 0 {
		t.Fatalf("not-ahead status: head %d, asked %d; want 10 and 0", chain.headNumber(), p.askedCount())
	}
	if chain2.headNumber() != 10 || p2.askedCount() != 0 {
		t.Fatalf("no notifier: head %d, asked %d; want 10 and 0", chain2.headNumber(), p2.askedCount())
	}
}
