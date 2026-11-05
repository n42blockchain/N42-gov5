// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// Run 36 B2 shape: a light follower holds a PrepareQC for a block it has
// checked, but the block's parent is not linked yet, so the committed-floor
// walk cannot reach the committed block (f343 in the s131 chain).

var (
	deferMiddle = types.Hash{0xd0} // 14131248, child of f343, header not seen yet
	deferTip    = types.Hash{0xd1} // 14131249, the block the PrepareQC certifies
)

// deferFollower is a two-phase, vote-on-availability follower committed at
// f343 that has checked deferTip (parent deferMiddle unknown) and then
// received deferTip's PrepareQC for view 1.
func deferFollower(t *testing.T, tip, parent types.Hash) (*ConsensusEngine, chan EngineOutput, *fakeCommittedChain) {
	t.Helper()
	follower, outputCh, setup := twoPhaseFollower(t)
	follower.SetVoteOnAvailability(true)
	chain := s131Chain()
	follower.SetCommittedChain(chain)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: tip, ParentHash: parent}); err != nil {
		t.Fatalf("checked: %v", err)
	}
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, tip))
	return follower, outputCh, chain
}

func commitVotes(ch chan EngineOutput) int {
	return countOutputs(drainOutputs(ch), OutputSendToValidator, MsgCommitVote)
}

func TestVoteDeferRetryParentLinksAfterPrepareQC(t *testing.T) {
	for _, on := range []bool{false, true} {
		restore := SetVoteDeferRetryForTest(on)
		follower, outputCh, _ := deferFollower(t, deferTip, deferMiddle)
		if n := commitVotes(outputCh); n != 0 {
			restore()
			t.Fatalf("flag=%v: commit vote with an unresolved ancestry: %d", on, n)
		}
		// The parent's header arrives and links deferTip to f343.
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
			Hash: deferMiddle, ParentHash: s131Committed, Number: 14131248}); err != nil {
			restore()
			t.Fatal(err)
		}
		want := 0
		if on {
			want = 1
		}
		if n := commitVotes(outputCh); n != want {
			restore()
			t.Fatalf("flag=%v: commit votes after the parent linked = %d, want %d", on, n, want)
		}
		// Later events never send a second commit vote.
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
			Hash: deferMiddle, ParentHash: s131Committed}); err != nil {
			restore()
			t.Fatal(err)
		}
		if n := commitVotes(outputCh); n != 0 {
			restore()
			t.Fatalf("flag=%v: second commit vote: %d", on, n)
		}
		restore()
	}
}

// The ancestry resolves to a sibling of the committed block: no commit vote
// with the switch on or off, and nothing stays held.
func TestVoteDeferRetryNonExtendingSiblingNeverVotes(t *testing.T) {
	sibMiddle := types.Hash{0xe0} // 14131247: a sibling of f343 (parent s131Parent)
	sibTip := types.Hash{0xe1}
	for _, on := range []bool{false, true} {
		restore := SetVoteDeferRetryForTest(on)
		follower, outputCh, _ := deferFollower(t, sibTip, sibMiddle)
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
			Hash: sibMiddle, ParentHash: s131Parent, Number: 14131247}); err != nil {
			restore()
			t.Fatal(err)
		}
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockImported,
			Hash: sibMiddle, ParentHash: s131Parent}); err != nil {
			restore()
			t.Fatal(err)
		}
		if n := commitVotes(outputCh); n != 0 {
			restore()
			t.Fatalf("flag=%v: commit vote for a block on a sibling of the committed block: %d", on, n)
		}
		follower.mu.Lock()
		held := follower.floorHeld
		follower.mu.Unlock()
		if held != nil {
			restore()
			t.Fatalf("flag=%v: a provably non-extending vote is still held", on)
		}
		restore()

		// Direct sibling (refuse on first check): never held either.
		restore = SetVoteDeferRetryForTest(on)
		follower, outputCh, _ = deferFollower(t, s131Sibling, s131Parent)
		follower.mu.Lock()
		held = follower.floorHeld
		follower.mu.Unlock()
		if n := commitVotes(outputCh); n != 0 || held != nil {
			restore()
			t.Fatalf("flag=%v: sibling of the committed block voted (%d) or held (%v)", on, n, held != nil)
		}
		restore()
	}
}

func TestVoteDeferRetryViewChangeDropsHeld(t *testing.T) {
	defer SetVoteDeferRetryForTest(true)()
	follower, outputCh, _ := deferFollower(t, deferTip, deferMiddle)
	follower.mu.Lock()
	if follower.floorHeld == nil {
		follower.mu.Unlock()
		t.Fatal("the unresolved commit vote was not held")
	}
	if err := follower.advanceToView(2); err != nil {
		follower.mu.Unlock()
		t.Fatal(err)
	}
	held := follower.floorHeld
	follower.mu.Unlock()
	if held != nil {
		t.Fatal("view change kept the held commit vote")
	}
	drainOutputs(outputCh)
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: deferMiddle, ParentHash: s131Committed, Number: 14131248}); err != nil {
		t.Fatal(err)
	}
	if n := commitVotes(outputCh); n != 0 {
		t.Fatalf("commit vote for a view already left: %d", n)
	}
}

// The committed head advances to the missing parent with no block event
// for it: the fallback timer (or RecheckDeferredVotes) releases the vote.
func TestVoteDeferRetryCommittedHeadAdvance(t *testing.T) {
	defer SetVoteDeferRetryForTest(true)()
	follower, outputCh, chain := deferFollower(t, deferTip, deferMiddle)
	follower.mu.Lock()
	chain.headers[deferMiddle] = fakeFloorHeader{14131248, s131Committed}
	chain.canonical[14131248] = deferMiddle
	chain.head, chain.headNum = deferMiddle, 14131248
	follower.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	got := 0
	for time.Now().Before(deadline) && got == 0 {
		select {
		case out := <-outputCh:
			if out.Type == OutputSendToValidator && out.Message != nil && out.Message.Type == MsgCommitVote {
				got++
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got != 1 {
		t.Fatalf("held commit vote not released after the committed head advanced: %d", got)
	}
	follower.RecheckDeferredVotes()
	if n := commitVotes(outputCh); n != 0 {
		t.Fatalf("second commit vote after re-check: %d", n)
	}
}

// N42_FLOOR_LAG_HEADERS: a light node 100 blocks behind on import still
// resolves the floor walk through the header-known links.
func TestFloorLagHeadersResolvesLongLag(t *testing.T) {
	for _, on := range []bool{false, true} {
		restore := SetFloorLagHeadersForTest(on)
		follower, _, _ := twoPhaseFollower(t)
		follower.SetCommittedChain(s131Chain())
		parent := s131Committed
		var tip types.Hash
		for i := 1; i <= 100; i++ {
			tip = types.Hash{0xa0, byte(i)}
			if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
				Hash: tip, ParentHash: parent, Number: 14131247 + uint64(i)}); err != nil {
				restore()
				t.Fatal(err)
			}
			parent = tip
		}
		follower.mu.Lock()
		v := follower.checkCommittedFloor(tip)
		follower.mu.Unlock()
		want := floorWait
		if on {
			want = floorAllow
		}
		if v != want {
			restore()
			t.Fatalf("flag=%v: verdict %v, want %v", on, v, want)
		}
		restore()
	}
}

// N42_FLOOR_LAG_HEADERS keeps refusing a long chain built on a sibling.
func TestFloorLagHeadersStillRefusesSiblingChain(t *testing.T) {
	defer SetFloorLagHeadersForTest(true)()
	follower, _, _ := twoPhaseFollower(t)
	follower.SetCommittedChain(s131Chain())
	parent := s131Sibling // 14131247, sibling of f343
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: s131Sibling, ParentHash: s131Parent, Number: 14131247}); err != nil {
		t.Fatal(err)
	}
	var tip types.Hash
	for i := 1; i <= 100; i++ {
		tip = types.Hash{0xb0, byte(i)}
		if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
			Hash: tip, ParentHash: parent, Number: 14131247 + uint64(i)}); err != nil {
			t.Fatal(err)
		}
		parent = tip
	}
	follower.mu.Lock()
	v := follower.checkCommittedFloor(tip)
	follower.mu.Unlock()
	if v != floorRefuse {
		t.Fatalf("sibling chain verdict %v, want refuse", v)
	}
}

// commitJournalEarly path: the commit vote is journaled while it waits for
// the check, then the release meets an unresolved ancestry and is held.
func TestVoteDeferRetryJournaledRelease(t *testing.T) {
	defer SetVoteDeferRetryForTest(true)()
	defer SetCommitJournalEarlyForTest(true)()
	follower, outputCh, setup := twoPhaseFollower(t)
	follower.SetVoteOnAvailability(true)
	follower.SetCommittedChain(s131Chain())
	deliverPrepareQC(t, follower, buildPrepareQCMsg(t, setup, deferTip))
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: deferTip, ParentHash: deferMiddle}); err != nil {
		t.Fatal(err)
	}
	follower.mu.Lock()
	held := follower.floorHeld
	follower.mu.Unlock()
	if held == nil || !held.journaled {
		t.Fatalf("journaled release not held on an unresolved ancestry: %+v", held)
	}
	if n := commitVotes(outputCh); n != 0 {
		t.Fatalf("commit vote with an unresolved ancestry: %d", n)
	}
	if err := follower.ProcessEvent(ConsensusEvent{Type: EventBlockHeaderKnown,
		Hash: deferMiddle, ParentHash: s131Committed, Number: 14131248}); err != nil {
		t.Fatal(err)
	}
	if n := commitVotes(outputCh); n != 1 {
		t.Fatalf("journaled held vote not released once the parent linked: %d", n)
	}
}
