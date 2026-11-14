// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestHSStateJournalRoutesSteadyPersist: with N42_HS_STATE_JOURNAL on and a
// separate vote journal, the steady-state persist writes the journal and
// leaves the chain database's record alone; the shutdown persist still
// writes the chain database; without the switch (or without a journal) the
// steady-state persist writes the chain database as before.
func TestHSStateJournalRoutesSteadyPersist(t *testing.T) {
	for _, on := range []bool{false, true} {
		s, _, _, _ := hsTNewNotifyService(t)
		jdb := openTestVoteJournal(t)
		s.SetVoteJournalDB(jdb)
		vjSave(t, s.db, &ConsensusState{View: 5})
		ce := s.engine.Engine()
		ce.RestoreState(20, QuorumCertificate{View: 18, BlockHash: types.Hash{0x18}}, QuorumCertificate{View: 17, BlockHash: types.Hash{0x17}}, 0)

		restore := SetHSStateJournalForTest(on)
		s.persistState()
		restore()
		chain, journal := vjLoad(t, s.db), vjLoad(t, jdb)
		if on {
			if chain.View != 5 {
				t.Fatalf("on: chain record moved to view %d", chain.View)
			}
			if journal == nil || journal.View != 20 || journal.LockedQC.View != 18 || journal.LastCommittedQC.View != 17 {
				t.Fatalf("on: journal record %+v, want view 20 lock 18 committed 17", journal)
			}
		} else {
			if chain.View != 20 || chain.LockedQC.View != 18 {
				t.Fatalf("off: chain record %+v, want view 20 lock 18", chain)
			}
			if journal != nil {
				t.Fatalf("off: journal written: %+v", journal)
			}
		}
		if got := s.getLastPersistedView(); got != 20 {
			t.Fatalf("on=%v: last persisted view %d, want 20", on, got)
		}
		// The shutdown persist stays on the chain database.
		defer SetHSStateJournalForTest(on)()
		ce.RestoreState(21, QuorumCertificate{View: 18, BlockHash: types.Hash{0x18}}, QuorumCertificate{View: 17, BlockHash: types.Hash{0x17}}, 0)
		s.persistStateCtx(s.ctx)
		if got := vjLoad(t, s.db); got.View != 21 {
			t.Fatalf("on=%v: shutdown persist left the chain record at view %d", on, got.View)
		}
	}

	// No journal store: the switch changes nothing.
	s, _, _, _ := hsTNewNotifyService(t)
	defer SetHSStateJournalForTest(true)()
	s.engine.Engine().RestoreState(9, QuorumCertificate{View: 8}, QuorumCertificate{View: 7}, 0)
	s.persistState()
	if got := vjLoad(t, s.db); got == nil || got.View != 9 {
		t.Fatalf("no journal: chain record %+v, want view 9", got)
	}
}

// TestHSStateJournalRecoverySameState: recovery restores the same view,
// lock, committed QC and vote commitments whether the steady-state
// persists went to the chain database (off) or to the journal (on).
func TestHSStateJournalRecoverySameState(t *testing.T) {
	type restored struct {
		view, lock, committed ViewNumber
		voted, commitVoted    ViewNumber
		votedHash             types.Hash
	}
	run := func(on bool) restored {
		defer SetHSStateJournalForTest(on)()
		s, _, _, _ := hsTNewNotifyService(t)
		jdb := openTestVoteJournal(t)
		s.SetVoteJournalDB(jdb)
		if err := s.anchorVoteJournal(); err != nil {
			t.Fatal(err)
		}
		ce := s.engine.Engine()
		// Votes are journalled as they are released; the service persists
		// its snapshot in between (deferred commits, every persistInterval).
		for v := ViewNumber(2); v <= 12; v++ {
			if err := s.JournalVote(&ConsensusState{View: v, LastVotedView: v, LastVotedHash: types.Hash{byte(v)},
				LastCommitVotedView: v - 1, LastCommitVotedHash: types.Hash{byte(v - 1)},
				LockedQC: QuorumCertificate{View: v - 1, BlockHash: types.Hash{byte(v - 1)}}}); err != nil {
				t.Fatal(err)
			}
			if v%3 == 0 {
				ce.RestoreState(v+1, QuorumCertificate{View: v, BlockHash: types.Hash{byte(v)}}, QuorumCertificate{View: v - 1, BlockHash: types.Hash{byte(v - 1)}}, 0)
				s.persistState()
			}
		}
		// Restart.
		s2, _, _, _ := hsTNewNotifyService(t)
		s2.db = s.db
		s2.SetVoteJournalDB(jdb)
		if err := s2.recoverState(); err != nil {
			t.Fatal(err)
		}
		ce2 := s2.engine.Engine()
		voted, votedHash, commitVoted, _ := ce2.roundState.VoteCommitments()
		return restored{view: ce2.CurrentView(), lock: ce2.LockedQC().View, committed: ce2.roundState.LastCommittedQC().View,
			voted: voted, votedHash: votedHash, commitVoted: commitVoted}
	}
	off, on := run(false), run(true)
	if off != on {
		t.Fatalf("recovered state differs: off %+v on %+v", off, on)
	}
	if on.voted != 12 || on.votedHash != (types.Hash{12}) || on.commitVoted != 11 {
		t.Fatalf("recovered vote commitments %+v, want 12/0c and 11", on)
	}
}

// TestHSStateJournalStalePersistKeepsVote is the crash ordering: a vote is
// journalled (durable before it is released) and then a steady-state
// persist whose snapshot predates the vote lands in the journal. The
// journal must keep the vote; a restart at that point restores it, and a
// conflicting vote in that view is refused by the durable record.
func TestHSStateJournalStalePersistKeepsVote(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	s, _, _, _ := hsTNewNotifyService(t)
	jdb := openTestVoteJournal(t)
	s.SetVoteJournalDB(jdb)
	if err := s.anchorVoteJournal(); err != nil {
		t.Fatal(err)
	}
	// The snapshot is taken before the vote ...
	ce := s.engine.Engine()
	ce.RestoreState(30, QuorumCertificate{View: 29, BlockHash: types.Hash{0x29}}, QuorumCertificate{View: 28}, 0)
	stale := s.newStateHook()
	// ... the vote of view 30 is journalled and released ...
	if err := s.JournalVote(&ConsensusState{View: 30, LastVotedView: 30, LastVotedHash: types.Hash{0xaa},
		LockedQC: QuorumCertificate{View: 29, BlockHash: types.Hash{0x29}}}); err != nil {
		t.Fatal(err)
	}
	// ... and the stale snapshot is written after it.
	s.persistHookTo(s.ctx, s.steadyStateDB(stale), stale)
	if got := vjLoad(t, jdb); got.LastVotedView != 30 || got.LastVotedHash != (types.Hash{0xaa}) {
		t.Fatalf("journal lost the vote: %+v", got)
	}
	// Crash and restart: the vote commitment is restored.
	s2, _, _, _ := hsTNewNotifyService(t)
	s2.db = s.db
	s2.SetVoteJournalDB(jdb)
	if err := s2.recoverState(); err != nil {
		t.Fatal(err)
	}
	voted, hash, _, _ := s2.engine.Engine().roundState.VoteCommitments()
	if voted != 30 || hash != (types.Hash{0xaa}) {
		t.Fatalf("restored vote %d/%x, want 30/aa", voted, hash[:1])
	}
	// A conflicting vote in view 30 cannot be made durable.
	if err := s2.JournalVote(&ConsensusState{View: 30, LastVotedView: 30, LastVotedHash: types.Hash{0xbb}}); err == nil {
		t.Fatal("a conflicting vote in an already-voted view was journalled")
	}
}
