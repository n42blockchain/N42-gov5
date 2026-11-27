// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// AM35 lock pitfall: on profile 1 a lock reaches disk only once this node
// holds the locked block durably, so a whole-set crash cannot restart the
// set locked on a block nobody can serve. The in-memory lock and the lock
// written with a commit vote are unaffected.

type recordingJournal struct{ states []*ConsensusState }

func (j *recordingJournal) JournalVote(st *ConsensusState) error {
	c := *st
	j.states = append(j.states, &c)
	return nil
}

func (j *recordingJournal) last(t *testing.T) *ConsensusState {
	t.Helper()
	if len(j.states) == 0 {
		t.Fatal("nothing journalled")
	}
	return j.states[len(j.states)-1]
}

// am35LockedFollower is a follower at view 2 that received a proposal for
// child justified by PrepareQC(parent, view 1) and checked it. The parent is
// executed (early notice) but not yet durable.
func am35LockedFollower(t *testing.T, profile1 bool) (*ConsensusEngine, chan EngineOutput, *recordingJournal, QuorumCertificate, types.Hash, types.Hash) {
	t.Helper()
	setup := newTestSetup(t, 4)
	child, parent := types.Hash{0xE1}, types.Hash{0xF1}
	var (
		e  *ConsensusEngine
		ch chan EngineOutput
	)
	if profile1 {
		e, ch = newP1Engine(t, setup, 0)
	} else {
		e, ch = newTestEngine(t, setup, 0)
		e.SetTwoPhaseVote(true)
	}
	j := &recordingJournal{}
	e.SetVoteJournal(j)
	e.mu.Lock()
	if err := e.advanceToView(2); err != nil {
		t.Fatal(err)
	}
	e.mu.Unlock()
	drainOutputs(ch)

	var justify QuorumCertificate
	var prop *Proposal
	if profile1 {
		justify = p1VoteQC(t, setup, p1TestIdentity, 1, parent)
		prop = p1Proposal(setup, p1TestIdentity, 2, child, justify, 2)
	} else {
		justify = *buildPaperTestQC(t, setup, 1, parent)
		prop = signedProposal(setup, 2, child, justify)
		prop.Proposer = 2
		prop.Signature = setup.keys[2].Sign(SigningMessage(2, child)).Marshal()
	}
	if err := p1Deliver(e, MsgProposal, prop); err != nil {
		t.Fatal(err)
	}
	if got := e.LockedQC(); got.View != 1 || got.BlockHash != parent {
		t.Fatalf("in-memory lock = view %d, want the proposal's justify (view 1)", got.View)
	}
	p1Event(t, e, EventBlockChecked, child, parent)
	am35EarlyImport(t, e, parent, types.Hash{})
	return e, ch, j, justify, child, parent
}

func TestAM35LockNotPersistedBeforeTheBlockIsDurable(t *testing.T) {
	e, ch, j, _, _, parent := am35LockedFollower(t, true)
	if n := p1Votes(ch, MsgVote); n != 1 {
		t.Fatalf("prepare votes = %d, want 1 (R1 still follows the early notice)", n)
	}
	if got := j.last(t).LockedQC; got.View != 0 {
		t.Fatalf("R1 journal wrote a lock on view %d %x before its block was durable", got.View, got.BlockHash[:2])
	}
	if got := e.SnapshotState().LockedQC; got.View != 0 {
		t.Fatalf("periodic snapshot wrote a lock on view %d before its block was durable", got.View)
	}
	// The in-memory lock still governs voting.
	if got := e.LockedQC(); got.View != 1 {
		t.Fatalf("in-memory lock lowered to view %d", got.View)
	}
	// Once the block is durable the lock is written.
	p1Event(t, e, EventBlockImported, parent, types.Hash{})
	if got := e.SnapshotState().LockedQC; got.View != 1 || got.BlockHash != parent {
		t.Fatalf("after the durable notice: persisted lock view %d, want 1", got.View)
	}
}

// Outside profile 1 the persisted lock is the in-memory lock, as before.
func TestAM35LegacyPersistsTheInMemoryLock(t *testing.T) {
	e, _, _, _, _, parent := am35LockedFollower(t, false)
	if got := e.SnapshotState().LockedQC; got.View != 1 || got.BlockHash != parent {
		t.Fatalf("legacy persisted lock view %d, want 1", got.View)
	}
}

// The lock journalled with a commit vote is never clamped.
func TestAM35CommitVoteJournalsTheFullLock(t *testing.T) {
	setup := newTestSetup(t, 4)
	blk, parent := types.Hash{0xE2}, types.Hash{0xF2}
	e, ch := newP1Engine(t, setup, 0)
	j := &recordingJournal{}
	e.SetVoteJournal(j)
	pqc := &PrepareQCMsg{View: 1, BlockHash: blk, QC: p1VoteQC(t, setup, p1TestIdentity, 1, blk)}
	if err := p1Deliver(e, MsgPrepareQC, pqc); err != nil {
		t.Fatal(err)
	}
	p1Event(t, e, EventBlockImported, blk, parent)
	if n := p1Votes(ch, MsgCommitVote); n != 1 {
		t.Fatalf("commit votes = %d, want 1", n)
	}
	st := j.last(t)
	if st.LastCommitVotedHash != blk || st.LockedQC.View != 1 || st.LockedQC.BlockHash != blk {
		t.Fatalf("commit-vote journal: lock view %d %x, commit %x; want the PrepareQC lock", st.LockedQC.View, st.LockedQC.BlockHash[:2], st.LastCommitVotedHash[:2])
	}
}

// A restored lock came from disk and stays persistable.
func TestAM35RestoredLockStaysPersisted(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newP1Engine(t, setup, 0)
	lock := p1VoteQC(t, setup, p1TestIdentity, 4, types.Hash{0xE3})
	e.RestoreState(5, lock, GenesisQC(), 0)
	if got := e.SnapshotState().LockedQC; got.View != 4 || got.BlockHash != lock.BlockHash {
		t.Fatalf("restored lock not persisted: view %d", got.View)
	}
}
