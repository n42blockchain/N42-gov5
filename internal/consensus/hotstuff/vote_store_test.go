package hotstuff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func prepareVoteStore(t *testing.T, setup *testSetup, db kv.RwDB, path string, requested bool, epochLength ...uint64) (*Service, *ConsensusEngine, chan EngineOutput) {
	t.Helper()
	e, ch := newTestEngine(t, setup, 0)
	e.SetSelfAddress(setup.validators[0].Address)
	if len(epochLength) > 0 {
		e.epochManager = NewEpochManagerWithLength(setup.vs, epochLength[0])
	}
	s := NewService(&HotStuff{engine: e}, nil, db, "test", "test")
	if err := s.ConfigureVoteStore(path, types.Hash{42}, requested); err != nil {
		t.Fatal(err)
	}
	if err := s.openVoteStore(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if err := s.seedVoteStore(); err != nil {
		t.Fatal(err)
	}
	e.SetVoteJournal(s)
	t.Cleanup(func() { s.cancel(); s.closeVoteStore() })
	return s, e, ch
}

func TestIndependentVoteStoreBypassesMainWriterAndRecovers(t *testing.T) {
	setup := newTestSetup(t, 4)
	db := memdb.NewTestDB(t)
	path := filepath.Join(t.TempDir(), "votes")
	s, e, ch := prepareVoteStore(t, setup, db, path, true)
	e.roundState.AdvanceView(1)
	writer, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	hash := types.Hash{10}
	go func() { done <- importAndPropose(t, e, setup, 1, hash) }()
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		writer.Rollback()
		t.Fatal("vote waited for main chain writer")
	}
	writer.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if findVote(drainOutputs(ch)) == nil {
		t.Fatal("durable vote not released")
	}
	if err = s.voteDB.View(context.Background(), func(tx kv.Tx) error {
		st, e := LoadConsensusState(tx)
		if e != nil {
			return e
		}
		if st == nil || st.LastVotedView != 1 || st.LastVotedHash != hash {
			return errors.New("vote not durable")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Crash boundary: close only the journal; do not copy the new vote into the
	// main checkpoint through Service.Stop. Removing the opt-in must still read it.
	s.cancel()
	s.closeVoteStore()
	_, recovered, out := prepareVoteStore(t, setup, db, path, false)
	if !recovered.roundState.HasVotedInView(1) {
		t.Fatal("lost vote across restart")
	}
	_ = importAndPropose(t, recovered, setup, 1, types.Hash{11})
	if findVote(drainOutputs(out)) != nil {
		t.Fatal("restart equivocated in the same view")
	}
}

type failingVoteDB struct{ kv.RwDB }

func (failingVoteDB) Update(context.Context, func(kv.RwTx) error) error {
	return errors.New("injected durable write failure")
}

func TestIndependentVoteStoreWriteFailureAbstains(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, e, ch := prepareVoteStore(t, setup, memdb.NewTestDB(t), filepath.Join(t.TempDir(), "votes"), true)
	s.voteDB = failingVoteDB{s.voteDB}
	e.roundState.AdvanceView(1)
	if err := importAndPropose(t, e, setup, 1, types.Hash{10}); err == nil {
		t.Fatal("vote ignored write failure")
	}
	if findVote(drainOutputs(ch)) != nil || e.roundState.HasVotedInView(1) {
		t.Fatal("failed write released or recorded a vote")
	}
}

func TestIndependentVoteStoreMissingOrWrongIdentityFails(t *testing.T) {
	for _, mode := range []string{"missing", "genesis", "key"} {
		t.Run(mode, func(t *testing.T) {
			setup := newTestSetup(t, 4)
			db := memdb.NewTestDB(t)
			path := filepath.Join(t.TempDir(), "votes")
			s, _, _ := prepareVoteStore(t, setup, db, path, true)
			s.cancel()
			s.closeVoteStore()
			genesis := types.Hash{42}
			nextSetup := setup
			switch mode {
			case "missing":
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
			case "genesis":
				genesis = types.Hash{43}
			case "key":
				nextSetup = newTestSetup(t, 4)
			}
			e, _ := newTestEngine(t, nextSetup, 0)
			next := NewService(&HotStuff{engine: e}, nil, db, "test", "test")
			defer next.cancel()
			if err := next.ConfigureVoteStore(path, genesis, false); err != nil {
				t.Fatal(err)
			}
			if err := next.Start(); err == nil {
				next.Stop()
				t.Fatal("unsafe recovery started")
			}
		})
	}
}

func TestIndependentVoteStoreEpochSnapshot(t *testing.T) {
	setup := newTestSetup(t, 4)
	nextSet := newTestSetup(t, 7)
	db := memdb.NewTestDB(t)
	path := filepath.Join(t.TempDir(), "votes")
	s, e, _ := prepareVoteStore(t, setup, db, path, true, 10)
	e.epochManager = NewEpochManagerWithLength(setup.vs, 10)
	e.epochManager.StageNextEpoch(nextSet.validators, nextSet.f)
	e.roundState.AdvanceView(9)
	if err := s.JournalVote(e.SnapshotState()); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.closeVoteStore()
	s2, e2, _ := prepareVoteStore(t, setup, db, path, false, 10)
	if !e2.epochManager.HasStagedNext() {
		t.Fatal("lost staged epoch")
	}
	e2.epochManager.epochLength = 10
	if !e2.epochManager.AdvanceEpoch(11) {
		t.Fatal("epoch did not activate")
	}
	e2.roundState.AdvanceView(11)
	if err := s2.JournalVote(e2.SnapshotState()); err != nil {
		t.Fatal(err)
	}
	s2.cancel()
	s2.closeVoteStore()
	_, e3, _ := prepareVoteStore(t, setup, db, path, false, 10)
	if e3.ValidatorCount() != 7 || e3.epochManager.HasStagedNext() {
		t.Fatal("activation was not atomically recovered")
	}
}

func TestIndependentVoteStoreMergesNewerCheckpoint(t *testing.T) {
	setup := newTestSetup(t, 4)
	db := memdb.NewTestDB(t)
	path := filepath.Join(t.TempDir(), "votes")
	old := &ConsensusState{View: 5, LockedQC: GenesisQC(), LastCommittedQC: GenesisQC(), LastVotedView: 4, LastVotedHash: types.Hash{4}}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return SaveConsensusState(tx, old) }); err != nil {
		t.Fatal(err)
	}
	s, e, ch := prepareVoteStore(t, setup, db, path, true)
	if !e.roundState.HasVotedInView(4) {
		t.Fatal("migration lost old vote")
	}
	e.roundState.AdvanceView(6)
	hash := types.Hash{6}
	if err := importAndPropose(t, e, setup, 6, hash); err != nil {
		t.Fatal(err)
	}
	drainOutputs(ch)
	newer := *old
	newer.View = 7
	newer.LockedQC.View = 5
	newer.LockedQC.BlockHash = types.Hash{5}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return SaveConsensusState(tx, &newer) }); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.closeVoteStore()
	_, restored, _ := prepareVoteStore(t, setup, db, path, false)
	snapshot := restored.SnapshotState()
	if snapshot.View != 7 || snapshot.LockedQC.View != 5 || snapshot.LastVotedView != 6 || snapshot.LastVotedHash != hash {
		t.Fatal("recovery did not merge independent safety fields")
	}
}

func TestIndependentVoteStoreRejectsConflictingCheckpoint(t *testing.T) {
	setup := newTestSetup(t, 4)
	db := memdb.NewTestDB(t)
	path := filepath.Join(t.TempDir(), "votes")
	s, e, _ := prepareVoteStore(t, setup, db, path, true)
	e.roundState.AdvanceView(1)
	if err := importAndPropose(t, e, setup, 1, types.Hash{1}); err != nil {
		t.Fatal(err)
	}
	bad := e.SnapshotState()
	bad.LastVotedHash = types.Hash{2}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return SaveConsensusState(tx, bad) }); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.closeVoteStore()
	nextEngine, _ := newTestEngine(t, setup, 0)
	next := NewService(&HotStuff{engine: nextEngine}, nil, db, "test", "test")
	defer next.cancel()
	if err := next.ConfigureVoteStore(path, types.Hash{42}, false); err != nil {
		t.Fatal(err)
	}
	if err := next.Start(); err == nil {
		next.Stop()
		t.Fatal("conflicting durable votes were accepted")
	}
}

func TestIndependentVoteStoreEpochAheadOfCheckpoint(t *testing.T) {
	setup := newTestSetup(t, 4)
	nextSet := newTestSetup(t, 7)
	db := memdb.NewTestDB(t)
	path := filepath.Join(t.TempDir(), "votes")
	s, e, _ := prepareVoteStore(t, setup, db, path, true, 10)
	e.roundState.AdvanceView(9)
	e.epochManager.StageNextEpoch(nextSet.validators, nextSet.f)
	if err := s.JournalVote(e.SnapshotState()); err != nil {
		t.Fatal(err)
	}
	// The service persisted activation, but crashed before the round checkpoint.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return ActivatePersistedEpoch(tx, 1, nextSet.validators, nextSet.f) }); err != nil {
		t.Fatal(err)
	}
	s.cancel()
	s.closeVoteStore()
	_, restored, _ := prepareVoteStore(t, setup, db, path, false, 10)
	if restored.CurrentView() != 11 || restored.epochManager.CurrentEpoch() != 1 || restored.ValidatorCount() != 7 || restored.epochManager.HasStagedNext() {
		t.Fatal("recovery regressed the active epoch")
	}
}
