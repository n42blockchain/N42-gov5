package hotstuff

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func newTestServiceWithDB(t *testing.T, setup *testSetup) (*Service, *HotStuff) {
	t.Helper()
	db := memdb.NewTestDB(t)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Service{engine: h, db: db, ctx: ctx}
	return s, h
}

// TestService_JournalVote covers the no-store no-op and the db-backed save.
func TestService_JournalVote(t *testing.T) {
	setup := newTestSetup(t, 4)

	// No db configured: no-op.
	sNoDB := &Service{ctx: context.Background()}
	if err := sNoDB.JournalVote(&ConsensusState{View: 1}); err != nil {
		t.Fatalf("JournalVote with no db: unexpected error: %v", err)
	}

	s, _ := newTestServiceWithDB(t, setup)
	state := &ConsensusState{View: 5, ConsecutiveTimeouts: 1}
	if err := s.JournalVote(state); err != nil {
		t.Fatalf("JournalVote: %v", err)
	}
	if err := s.db.View(context.Background(), func(tx kv.Tx) error {
		loaded, err := LoadConsensusState(tx)
		if err != nil {
			return err
		}
		if loaded == nil || loaded.View != 5 {
			t.Fatalf("expected journalled view 5, got %+v", loaded)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
}

// TestService_PersistStateAndNewStateHook covers newStateHook's nil branches
// (no db, no engine) and the success path via persistState/persistStateCtx.
func TestService_PersistStateAndNewStateHook(t *testing.T) {
	setup := newTestSetup(t, 4)

	if (&Service{}).newStateHook() != nil {
		t.Fatalf("expected nil state hook with no db")
	}
	if (&Service{db: memdb.NewTestDB(t), engine: New(nil, nil)}).newStateHook() != nil {
		t.Fatalf("expected nil state hook with no initialized engine")
	}

	s, h := newTestServiceWithDB(t, setup)
	s.persistState()
	if s.lastPersistedView != h.Engine().CurrentView() {
		t.Fatalf("expected lastPersistedView to track the engine's current view, got %d want %d",
			s.lastPersistedView, h.Engine().CurrentView())
	}

	if err := s.db.View(context.Background(), func(tx kv.Tx) error {
		loaded, err := LoadConsensusState(tx)
		if err != nil {
			return err
		}
		if loaded == nil {
			t.Fatalf("expected a persisted state after persistState")
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
}

// TestService_RecoverEpochState covers the nil-db/nil-engine guards and the
// restore of a previously saved active (and staged) validator set.
func TestService_RecoverEpochState(t *testing.T) {
	// Guards: must not panic.
	(&Service{}).recoverEpochState()
	(&Service{db: memdb.NewTestDB(t), engine: New(nil, nil)}).recoverEpochState()

	setup := newTestSetup(t, 4)
	s, h := newTestServiceWithDB(t, setup)

	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := SaveActiveEpoch(tx, 3, setup.validators, setup.f); err != nil {
			return err
		}
		return SaveStagedEpoch(tx, 4, setup.validators[:2], 0)
	}); err != nil {
		t.Fatalf("seed epoch state: %v", err)
	}

	s.recoverEpochState()

	_, vals, f, ok := h.Engine().CurrentEpochInfoSafe()
	if !ok || len(vals) != len(setup.validators) || f != setup.f {
		t.Fatalf("active set not restored: ok=%v vals=%d f=%d", ok, len(vals), f)
	}
	_, stagedVals, _, stagedOk := h.Engine().StagedEpochInfoSafe()
	if !stagedOk || len(stagedVals) != 2 {
		t.Fatalf("staged set not restored: ok=%v vals=%d", stagedOk, len(stagedVals))
	}
}

// TestService_RecoverState covers: no persisted state (no-op), a persisted
// view behind the current one (vote commitments still restored), and a
// corrupted QC-ahead-of-view guard.
func TestService_RecoverState(t *testing.T) {
	setup := newTestSetup(t, 4)

	s, h := newTestServiceWithDB(t, setup)
	if err := s.recoverState(); err != nil {
		t.Fatalf("recoverState with no persisted state: %v", err)
	}

	// Persist a state ahead of the engine's current view and recover it.
	ahead := &ConsensusState{
		View:                 h.Engine().CurrentView() + 5,
		LockedQC:             QuorumCertificate{View: h.Engine().CurrentView() + 1},
		LastCommittedQC:      QuorumCertificate{View: h.Engine().CurrentView()},
		LastVotedView:        h.Engine().CurrentView() + 1,
		LastVotedHash:        types.Hash{0x01},
		LastCommitVotedView:  h.Engine().CurrentView(),
		LastCommitVotedHash:  types.Hash{0x02},
	}
	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, ahead)
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := s.recoverState(); err != nil {
		t.Fatalf("recoverState: %v", err)
	}
	if h.Engine().CurrentView() != ahead.View {
		t.Fatalf("expected engine view restored to %d, got %d", ahead.View, h.Engine().CurrentView())
	}

	// Corrupted: LockedQC.View > state.View must be rejected (logged, not
	// applied) rather than panicking.
	s2, h2 := newTestServiceWithDB(t, setup)
	corrupted := &ConsensusState{
		View:     1,
		LockedQC: QuorumCertificate{View: 99},
	}
	if err := s2.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, corrupted)
	}); err != nil {
		t.Fatalf("seed corrupted state: %v", err)
	}
	beforeView := h2.Engine().CurrentView()
	if err := s2.recoverState(); err != nil {
		t.Fatalf("recoverState(corrupted): %v", err)
	}
	if h2.Engine().CurrentView() != beforeView {
		t.Fatalf("expected view unchanged after rejecting corrupted state, got %d want %d",
			h2.Engine().CurrentView(), beforeView)
	}
}
