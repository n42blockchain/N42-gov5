// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// AM35 tests: on profile 1 the commit (R2) vote certifies a durably written
// block. The early executed notice (EventBlockImported with PrePersist) may
// release the prepare (R1) vote but never the commit vote; the post-persist
// notice releases it.

func am35EarlyImport(t *testing.T, e *ConsensusEngine, hash, parent types.Hash) {
	t.Helper()
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: hash, ParentHash: parent, PrePersist: true}); err != nil {
		t.Fatalf("early import: %v", err)
	}
}

// A PrepareQC held for the block is not released by the early executed
// notice; the durable notice releases it.
func TestAM35CommitVoteHeldUntilDurableNotice(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xD1}, types.Hash{0xC1}
	e, ch := newP1Engine(t, setup, 0)

	pqc := &PrepareQCMsg{View: 1, BlockHash: blk, QC: p1VoteQC(t, setup, p1TestIdentity, 1, blk)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	am35EarlyImport(t, e, blk, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote on the early executed notice alone: %d", n)
	}
	if e.commitVoteAttested(blk) {
		t.Fatal("commit gate open before the durable notice")
	}
	// A repeated early notice does not open it either.
	am35EarlyImport(t, e, blk, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote on a repeated early notice: %d", n)
	}
	p1Event(t, e, EventBlockImported, blk, parent) // durable notice
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("after the durable notice: %d commit votes, want 1", n)
	}
}

// The PrepareQC arriving after the early notice is held, not voted, and the
// durable notice casts it.
func TestAM35PrepareQCAfterEarlyNoticeIsHeld(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xD2}, types.Hash{0xC2}
	e, ch := newP1Engine(t, setup, 0)

	am35EarlyImport(t, e, blk, parent)
	pqc := &PrepareQCMsg{View: 1, BlockHash: blk, QC: p1VoteQC(t, setup, p1TestIdentity, 1, blk)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote for an executed but not durable block: %d", n)
	}
	e.mu.Lock()
	held := e.pendingCommitQC != nil && e.pendingCommitQC.BlockHash == blk
	e.mu.Unlock()
	if !held {
		t.Fatal("the PrepareQC was not held for the durable notice")
	}
	p1Event(t, e, EventBlockImported, blk, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("after the durable notice: %d commit votes, want 1", n)
	}
}

// A durable notice is final: a late early notice for the same block does not
// close the gate again.
func TestAM35DurableNoticeIsFinal(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xD3}, types.Hash{0xC3}
	e, ch := newP1Engine(t, setup, 0)
	p1Event(t, e, EventBlockImported, blk, parent)
	am35EarlyImport(t, e, blk, parent)
	pqc := &PrepareQCMsg{View: 1, BlockHash: blk, QC: p1VoteQC(t, setup, p1TestIdentity, 1, blk)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("durable block: %d commit votes, want 1", n)
	}
}

// R1 is unaffected: the early executed notice of the parent still releases
// the prepare vote of a checked child on profile 1.
func TestAM35PrepareVoteStillFollowsEarlyNotice(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xD4}, types.Hash{0xC4}
	e, ch := newP1Engine(t, setup, 0)
	e.mu.Lock()
	if err := e.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()
	drainOutputs(ch)

	justify := p1VoteQC(t, setup, p1TestIdentity, 1, parent)
	if err := p1Deliver(e, MsgProposal, p1Proposal(setup, p1TestIdentity, 2, blk, justify, 2)); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockChecked, blk, parent)
	if n := p1Votes(ch, MsgVote); n != 0 {
		t.Fatalf("voted before the parent executed: %d", n)
	}
	am35EarlyImport(t, e, parent, types.Hash{})
	if n := p1Votes(ch, MsgVote); n != 1 {
		t.Fatalf("after the parent's early notice: %d prepare votes, want 1", n)
	}
}

// Outside profile 1 nothing changes: the early notice releases the held
// two-phase commit vote as before.
func TestAM35LegacyCommitVoteOnEarlyNotice(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xD5}, types.Hash{0xC5}
	e, ch := newTestEngine(t, setup, 0)
	e.SetTwoPhaseVote(true)
	qc := buildPaperTestQC(t, setup, 1, blk)
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{View: 1, BlockHash: blk, QC: *qc}}}); err != nil {
		t.Fatal(err)
	}
	if n := p1Votes(ch, MsgCommitVote); n != 0 {
		t.Fatalf("legacy: commit vote before any import: %d", n)
	}
	am35EarlyImport(t, e, blk, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("legacy: %d commit votes on the early notice, want 1", n)
	}
}

// Service level: on a profile-1 chain NotifyBlockExecuted (the blockchain's
// executed hook, before the MDBX write) does not release the commit vote; the
// post-persist NotifyBlockImported does, although the early notice already
// stamped the dedup set.
func TestAM35ServiceCommitVoteWaitsForPostPersistNotice(t *testing.T) {
	s, setup, _, fetcher := hsTNewNotifyService(t)
	s.engine.chainConfig = &params.ChainConfig{N42Profile: params.N42Profile1}
	e := s.engine.Engine()
	e.EnableH2V4(p1TestIdentity)
	e.SetProfile1(setup.vs)
	out := s.engine.outputCh

	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), ParentHash: p1TestIdentity.GenesisHash}
	blk := hsTWriteHeader(t, s, header)

	pqc := &PrepareQCMsg{View: 1, BlockHash: blk, QC: p1VoteQC(t, setup, p1TestIdentity, 1, blk)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	drainOutputs(out)

	s.NotifyBlockExecuted(blk, types.Hash{}, header.ParentHash, 1, nil)
	if n := p1Votes(out, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote on the executed hook alone: %d", n)
	}
	// Not yet applied (the write has not committed): still withheld.
	fetcher.applied = false
	s.NotifyBlockImported(blk, types.Hash{})
	if n := p1Votes(out, MsgCommitVote); n != 0 {
		t.Fatalf("commit vote before the block was persisted: %d", n)
	}
	fetcher.applied = true
	s.NotifyBlockImported(blk, types.Hash{})
	if n := p1Votes(out, MsgCommitVote); n != 1 {
		t.Fatalf("after the post-persist notice: %d commit votes, want 1", n)
	}
	s.pendingMu.Lock()
	_, early := s.notifiedEarly[blk]
	s.pendingMu.Unlock()
	if early {
		t.Fatal("the durable upgrade was not consumed")
	}
	// Re-gossip after that is deduplicated as before.
	s.NotifyBlockImported(blk, types.Hash{})
	if n := p1Votes(out, MsgCommitVote); n != 0 {
		t.Fatalf("duplicate notice produced %d commit votes", n)
	}
}

// Service level, legacy chain: the executed hook still releases the commit
// vote and does not arm the durable upgrade.
func TestAM35ServiceLegacyUnchanged(t *testing.T) {
	s, setup, _, _ := hsTNewNotifyService(t)
	e := s.engine.Engine()
	e.SetTwoPhaseVote(true)
	out := s.engine.outputCh
	blk := types.Hash{0xD6}
	qc := buildPaperTestQC(t, setup, 1, blk)
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgPrepareQC, Payload: &PrepareQCMsg{View: 1, BlockHash: blk, QC: *qc}}}); err != nil {
		t.Fatal(err)
	}
	drainOutputs(out)
	s.NotifyBlockExecuted(blk, types.Hash{}, types.Hash{0xC6}, 1, nil)
	if n := p1Votes(out, MsgCommitVote); n != 1 {
		t.Fatalf("legacy: %d commit votes on the executed hook, want 1", n)
	}
	if _, early := s.notifiedEarly[blk]; early {
		t.Fatal("legacy chain armed the durable upgrade")
	}
}
