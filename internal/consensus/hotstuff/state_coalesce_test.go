// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// countingDB counts the write transactions opened on a store.
type countingDB struct {
	kv.RwDB
	updates atomic.Int64
}

func (c *countingDB) Update(ctx context.Context, f func(tx kv.RwTx) error) error {
	c.updates.Add(1)
	return c.RwDB.Update(ctx, f)
}

// coalesceService is a service with a counted vote journal, its chain
// record anchored.
func coalesceService(t *testing.T) (*Service, *countingDB) {
	t.Helper()
	s, _, _, _ := hsTNewNotifyService(t)
	jdb := &countingDB{RwDB: openTestVoteJournal(t)}
	s.SetVoteJournalDB(jdb)
	if err := s.anchorVoteJournal(); err != nil {
		t.Fatal(err)
	}
	return s, jdb
}

// voteState is what the engine journals for a vote: its snapshot with the
// vote commitment.
func voteState(s *Service, view ViewNumber, hash types.Hash) *ConsensusState {
	st := s.engine.Engine().SnapshotState()
	st.LastVotedView, st.LastVotedHash = view, hash
	return st
}

func qc(v ViewNumber) QuorumCertificate {
	return QuorumCertificate{View: v, BlockHash: types.Hash{byte(v)}}
}

// TestHSStateCoalescePiggyback (N42_HS_STATE_COALESCE): the steady-state
// persist opens no transaction; the next vote's journal transaction carries
// it, so a persist and a vote cost one transaction instead of two, and the
// journal ends with the same record as without the switch.
func TestHSStateCoalescePiggyback(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	run := func(on bool) (*ConsensusState, int64, ViewNumber) {
		defer SetHSStateCoalesceForTest(on, time.Hour)()
		s, jdb := coalesceService(t)
		ce := s.engine.Engine()
		ce.RestoreState(20, qc(18), qc(17), 0)
		base := jdb.updates.Load()
		s.persistState()
		if on {
			if n := jdb.updates.Load() - base; n != 0 {
				t.Fatalf("coalesced persist opened %d transactions", n)
			}
			if got := vjLoad(t, s.db); got.View == 20 {
				t.Fatal("coalesced persist wrote the chain database")
			}
			if s.getLastPersistedView() == 20 {
				t.Fatal("last persisted view advanced before the record was written")
			}
		}
		ce.RestoreState(21, qc(20), qc(18), 0)
		if err := s.JournalVote(voteState(s, 21, types.Hash{0x21})); err != nil {
			t.Fatal(err)
		}
		if s.coalesce.has() {
			t.Fatal("pending record left after the vote's transaction")
		}
		return vjLoad(t, jdb), jdb.updates.Load() - base, s.getLastPersistedView()
	}
	offRec, offTx, offView := run(false)
	onRec, onTx, onView := run(true)
	if offTx != 2 || onTx != 1 {
		t.Fatalf("journal transactions: off %d on %d, want 2 and 1", offTx, onTx)
	}
	if !reflect.DeepEqual(offRec, onRec) {
		t.Fatalf("journal record differs: off %+v on %+v", offRec, onRec)
	}
	if onRec.View != 21 || onRec.LastVotedView != 21 || onRec.LockedQC.View != 20 || onRec.LastCommittedQC.View != 18 {
		t.Fatalf("journal record %+v", onRec)
	}
	if offView != 20 || onView != 20 {
		t.Fatalf("last persisted view off %d on %d, want 20", offView, onView)
	}
}

// TestHSStateCoalesceIdleFlush: without a vote the pending record is
// written by the idle flush in one journal transaction; persists while it
// is pending merge into one record.
func TestHSStateCoalesceIdleFlush(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	defer SetHSStateCoalesceForTest(true, 20*time.Millisecond)()
	s, jdb := coalesceService(t)
	defer s.stopCoalesce()
	ce := s.engine.Engine()
	base := jdb.updates.Load()
	ce.RestoreState(40, qc(38), qc(37), 0)
	s.persistState()
	ce.RestoreState(41, qc(40), qc(38), 0)
	s.persistState()
	deadline := time.Now().Add(5 * time.Second)
	for s.getLastPersistedView() != 41 {
		if time.Now().After(deadline) {
			t.Fatal("idle flush did not write the pending record")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if n := jdb.updates.Load() - base; n != 1 {
		t.Fatalf("idle flush: %d transactions, want 1", n)
	}
	if got := vjLoad(t, jdb); got.View != 41 || got.LockedQC.View != 40 || got.LastCommittedQC.View != 38 {
		t.Fatalf("journal record %+v", got)
	}
	if s.coalesce.flushed.Load() != 1 || s.coalesce.coalesced.Load() != 2 {
		t.Fatalf("counters flushed %d coalesced %d", s.coalesce.flushed.Load(), s.coalesce.coalesced.Load())
	}
}

// TestHSStateCoalesceCrashBeforePersist is the crash ordering: a vote is
// journalled and released, the node commits further and the steady-state
// persist is pending, and the node crashes before any write carries it.
// The restart restores the vote (a conflicting vote in that view is
// refused) and exactly the state a node without the switch restores after
// a crash before its persist transaction committed.
func TestHSStateCoalesceCrashBeforePersist(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	type restored struct {
		view, lock, committed ViewNumber
		voted, commitVoted    ViewNumber
		votedHash             types.Hash
	}
	run := func(on bool) restored {
		defer SetHSStateCoalesceForTest(on, time.Hour)()
		s, jdb := coalesceService(t)
		ce := s.engine.Engine()
		ce.RestoreState(30, qc(29), qc(28), 0)
		if err := s.JournalVote(voteState(s, 30, types.Hash{0xaa})); err != nil {
			t.Fatal(err)
		}
		// Committed further; the persist after the commit ...
		ce.RestoreState(31, qc(30), qc(29), 0)
		if on {
			s.persistState() // ... is pending when the node crashes;
		} // without the switch the crash comes before its transaction.
		s.stopCoalesce()

		s2, _, _, _ := hsTNewNotifyService(t)
		s2.db = s.db
		s2.SetVoteJournalDB(jdb.RwDB)
		if err := s2.recoverState(); err != nil {
			t.Fatal(err)
		}
		ce2 := s2.engine.Engine()
		voted, votedHash, commitVoted, _ := ce2.roundState.VoteCommitments()
		if err := s2.JournalVote(&ConsensusState{View: 30, LastVotedView: 30, LastVotedHash: types.Hash{0xbb}}); err == nil {
			t.Fatalf("on=%v: a conflicting vote in an already-voted view was journalled", on)
		}
		return restored{view: ce2.CurrentView(), lock: ce2.LockedQC().View, committed: ce2.roundState.LastCommittedQC().View,
			voted: voted, votedHash: votedHash, commitVoted: commitVoted}
	}
	off, on := run(false), run(true)
	if off != on {
		t.Fatalf("recovered state differs: off %+v on %+v", off, on)
	}
	if on.voted != 30 || on.votedHash != (types.Hash{0xaa}) || on.lock != 29 {
		t.Fatalf("recovered %+v, want vote 30/aa under lock 29", on)
	}
}

// TestHSStateCoalesceRecoverySameState: votes and steady-state persists
// interleaved over many views, then a restart after the last vote. The
// restored state is the same with the switch off and on.
func TestHSStateCoalesceRecoverySameState(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	type restored struct {
		view, lock, committed ViewNumber
		voted, commitVoted    ViewNumber
		votedHash             types.Hash
	}
	run := func(on bool) (restored, int64) {
		defer SetHSStateCoalesceForTest(on, time.Hour)()
		s, jdb := coalesceService(t)
		ce := s.engine.Engine()
		base := jdb.updates.Load()
		for v := ViewNumber(2); v <= 12; v++ {
			ce.RestoreState(v, qc(v-1), qc(v-2), 0)
			if err := s.JournalVote(voteState(s, v, types.Hash{byte(v)})); err != nil {
				t.Fatal(err)
			}
			if v%3 == 0 {
				ce.RestoreState(v, qc(v), qc(v-1), 0)
				s.persistState()
			}
		}
		// One more vote after the last persist.
		ce.RestoreState(13, qc(12), qc(11), 0)
		if err := s.JournalVote(voteState(s, 13, types.Hash{13})); err != nil {
			t.Fatal(err)
		}
		s.stopCoalesce()
		n := jdb.updates.Load() - base
		s2, _, _, _ := hsTNewNotifyService(t)
		s2.db = s.db
		s2.SetVoteJournalDB(jdb.RwDB)
		if err := s2.recoverState(); err != nil {
			t.Fatal(err)
		}
		ce2 := s2.engine.Engine()
		voted, votedHash, commitVoted, _ := ce2.roundState.VoteCommitments()
		return restored{view: ce2.CurrentView(), lock: ce2.LockedQC().View, committed: ce2.roundState.LastCommittedQC().View,
			voted: voted, votedHash: votedHash, commitVoted: commitVoted}, n
	}
	off, offTx := run(false)
	on, onTx := run(true)
	if off != on {
		t.Fatalf("recovered state differs: off %+v on %+v", off, on)
	}
	if on.voted != 13 || on.votedHash != (types.Hash{13}) || on.lock != 12 {
		t.Fatalf("recovered %+v", on)
	}
	if offTx != 16 || onTx != 12 {
		t.Fatalf("journal transactions off %d on %d, want 16 and 12", offTx, onTx)
	}
}

// TestHSStateCoalesceConcurrent: persists from the commit loop race the
// votes on the engine goroutine (under -race); the journal ends with the
// last vote and the highest lock, and every pending record is written.
func TestHSStateCoalesceConcurrent(t *testing.T) {
	defer SetHSStateJournalForTest(true)()
	defer SetHSStateCoalesceForTest(true, 2*time.Millisecond)()
	s, jdb := coalesceService(t)
	defer s.stopCoalesce()
	ce := s.engine.Engine()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.persistState()
			time.Sleep(100 * time.Microsecond)
		}
	}()
	for v := ViewNumber(2); v <= 60; v++ {
		ce.RestoreState(v, qc(v-1), qc(v-2), 0)
		if err := s.JournalVote(voteState(s, v, types.Hash{byte(v)})); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for s.coalesce.has() {
		if time.Now().After(deadline) {
			t.Fatal("pending record never written")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond) // a flush that took the record finishes
	got := vjLoad(t, jdb)
	if got.LastVotedView != 60 || got.LastVotedHash != (types.Hash{60}) || got.LockedQC.View != 59 || got.View != 60 {
		t.Fatalf("journal record %+v", got)
	}
}
