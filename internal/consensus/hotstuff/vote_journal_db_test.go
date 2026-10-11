// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// openTestVoteJournal opens an on-disk, durable journal store the way the
// node does (openVoteJournalDB).
func openTestVoteJournal(t testing.TB) kv.RwDB {
	t.Helper()
	db, err := mdbx.NewMDBX(log.New()).Path(t.TempDir()).Label(kv.ConsensusDB).
		WithTableCfg(VoteJournalTables).MapSize(64 * datasize.MB).GrowthStep(4 * datasize.MB).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func vjLoad(t *testing.T, db kv.RwDB) *ConsensusState {
	t.Helper()
	var st *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		st, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func vjSave(t *testing.T, db kv.RwDB, st *ConsensusState) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestVoteJournalSeparateStore: with a journal store set, JournalVote writes
// there and not to the chain database, and recovery merges the journal's
// newer vote commitments into the chain record.
func TestVoteJournalSeparateStore(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	jdb := openTestVoteJournal(t)
	s.SetVoteJournalDB(jdb)

	vjSave(t, s.db, &ConsensusState{View: 10, LastVotedView: 10, LastVotedHash: types.Hash{0x0a}})
	if err := s.JournalVote(&ConsensusState{View: 12, LastVotedView: 12, LastVotedHash: types.Hash{0x0c},
		LastCommitVotedView: 11, LastCommitVotedHash: types.Hash{0x0b}}); err != nil {
		t.Fatal(err)
	}
	if got := vjLoad(t, s.db); got.LastVotedView != 10 {
		t.Fatalf("chain record moved to vote view %d; the journal must not write it", got.LastVotedView)
	}
	if got := vjLoad(t, jdb); got == nil || got.LastVotedView != 12 {
		t.Fatalf("journal record = %+v, want vote view 12", got)
	}
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	ce := s.engine.Engine()
	view, hash, cview, chash := ce.roundState.VoteCommitments()
	if view != 12 || hash != (types.Hash{0x0c}) || cview != 11 || chash != (types.Hash{0x0b}) {
		t.Fatalf("restored commitments %d %x %d %x, want the journal's 12/0c 11/0b", view, hash[:1], cview, chash[:1])
	}
}

// TestVoteJournalDiscardedWithoutChainRecord: a reset tool deletes the chain
// record only; the journal must not resurrect the state it deleted, and Start
// anchors a fresh chain record before any vote.
func TestVoteJournalDiscardedWithoutChainRecord(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	jdb := openTestVoteJournal(t)
	s.SetVoteJournalDB(jdb)
	vjSave(t, jdb, &ConsensusState{View: 500, LastVotedView: 500, LastVotedHash: types.Hash{0x55}})

	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if got := vjLoad(t, jdb); got != nil {
		t.Fatalf("journal kept %+v after recovering a datadir with no chain record", got)
	}
	if view, _, _, _ := s.engine.Engine().roundState.VoteCommitments(); view == 500 {
		t.Fatal("restored the discarded journal's vote")
	}
	if err := s.anchorVoteJournal(); err != nil {
		t.Fatal(err)
	}
	if vjLoad(t, s.db) == nil {
		t.Fatal("no chain record anchored before the first vote")
	}
	// Anchored: a journalled vote now survives a restart.
	if err := s.JournalVote(&ConsensusState{View: 3, LastVotedView: 3, LastVotedHash: types.Hash{0x03}}); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if view, _, _, _ := s.engine.Engine().roundState.VoteCommitments(); view != 3 {
		t.Fatalf("restored vote view %d, want 3", view)
	}
}

// TestVoteJournalUnderBlockWrites is the A/B of the separate journal: a
// background writer holds the chain database's write transaction the way the
// lag writer does at 163k-tx blocks (~90 ms held, ~60 ms free); JournalVote
// latency is measured with the journal in the chain database (A) and in its
// own durable store (B). Run with TMPDIR on a real disk to include the
// journal's fsync (t.TempDir on tmpfs makes it free).
func TestVoteJournalUnderBlockWrites(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	measure := func(separate bool) (p50, p90 time.Duration) {
		chain := memdb.NewTestDB(t)
		s := &Service{db: chain, ctx: context.Background()}
		if separate {
			s.SetVoteJournalDB(openTestVoteJournal(t))
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = chain.Update(context.Background(), func(tx kv.RwTx) error {
					time.Sleep(90 * time.Millisecond)
					return tx.Put(modules.HotStuffState, []byte("filler"), []byte{1})
				})
				time.Sleep(60 * time.Millisecond)
			}
		}()
		rng := rand.New(rand.NewSource(42))
		var ds []time.Duration
		for i := 1; i <= 40; i++ {
			// Arrivals independent of the write cycle (150 ms period).
			time.Sleep(time.Duration(rng.Int63n(int64(150 * time.Millisecond))))
			start := time.Now()
			if err := s.JournalVote(&ConsensusState{View: ViewNumber(i), LastVotedView: ViewNumber(i), LastVotedHash: types.Hash{byte(i)}}); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, time.Since(start))
		}
		close(stop)
		wg.Wait()
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2], ds[len(ds)*9/10]
	}
	a50, a90 := measure(false)
	b50, b90 := measure(true)
	t.Logf("JournalVote under block writes: chain store p50 %v p90 %v; separate store p50 %v p90 %v", a50, a90, b50, b90)
	if b90 > 30*time.Millisecond {
		t.Fatalf("separate journal still waits: p90 %v", b90)
	}
}
