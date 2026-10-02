package hotstuff

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestSaveLoadClearPendingVotes exercises the pending-vote persistence round
// trip used for crash recovery: save with both prepare and commit votes,
// load back, then clear and confirm it is gone.
func TestSaveLoadClearPendingVotes(t *testing.T) {
	db := memdb.NewTestDB(t)

	pv := &PendingVotesState{
		View:      7,
		BlockHash: types.Hash{0x01, 0x02},
		PrepareVotes: map[ValidatorIndex][]byte{
			0: {0xAA, 0xBB, 0xCC},
			1: {0xDD},
		},
		CommitVotes: map[ValidatorIndex][]byte{
			2: {0xEE, 0xFF},
		},
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePendingVotes(tx, pv)
	}); err != nil {
		t.Fatalf("SavePendingVotes: %v", err)
	}

	var loaded *PendingVotesState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadPendingVotes(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadPendingVotes: %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected non-nil loaded state")
	}
	if loaded.View != pv.View || loaded.BlockHash != pv.BlockHash {
		t.Fatalf("mismatched view/hash: %+v", loaded)
	}
	if len(loaded.PrepareVotes) != 2 || string(loaded.PrepareVotes[0]) != string(pv.PrepareVotes[0]) {
		t.Fatalf("mismatched prepare votes: %+v", loaded.PrepareVotes)
	}
	if len(loaded.CommitVotes) != 1 || string(loaded.CommitVotes[2]) != string(pv.CommitVotes[2]) {
		t.Fatalf("mismatched commit votes: %+v", loaded.CommitVotes)
	}

	// SavePendingVotes(nil) deletes the key.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePendingVotes(tx, nil)
	}); err != nil {
		t.Fatalf("SavePendingVotes(nil): %v", err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, err := LoadPendingVotes(tx)
		if err != nil {
			return err
		}
		if got != nil {
			t.Fatalf("expected nil after delete, got %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("LoadPendingVotes after delete: %v", err)
	}

	// ClearPendingVotes on an already-clear key is a no-op, not an error.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return ClearPendingVotes(tx)
	}); err != nil {
		t.Fatalf("ClearPendingVotes: %v", err)
	}
}

// TestLoadPendingVotes_NoState covers the "key absent" branch, which returns
// (nil, nil) rather than an error.
func TestLoadPendingVotes_NoState(t *testing.T) {
	db := memdb.NewTestDB(t)
	var loaded *PendingVotesState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadPendingVotes(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadPendingVotes: %v", err)
	}
	if loaded != nil {
		t.Fatalf("expected nil for absent state, got %+v", loaded)
	}
}

// TestSavePendingVotes_EmptyMaps covers the zero-votes encode/decode path.
func TestSavePendingVotes_EmptyMaps(t *testing.T) {
	db := memdb.NewTestDB(t)
	pv := &PendingVotesState{View: 1, BlockHash: types.Hash{0x09}}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePendingVotes(tx, pv)
	}); err != nil {
		t.Fatalf("SavePendingVotes: %v", err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		loaded, err := LoadPendingVotes(tx)
		if err != nil {
			return err
		}
		if loaded == nil || len(loaded.PrepareVotes) != 0 || len(loaded.CommitVotes) != 0 {
			t.Fatalf("expected empty vote maps, got %+v", loaded)
		}
		return nil
	}); err != nil {
		t.Fatalf("LoadPendingVotes: %v", err)
	}
}

// TestSaveEquivocationEvidence covers the evidence persistence helper. There
// is no corresponding loader in this package (slashing.go reads evidence via
// its own path), so this test confirms the write succeeds and produces the
// expected key/value shape by reading the raw bytes back.
func TestSaveEquivocationEvidence(t *testing.T) {
	db := memdb.NewTestDB(t)
	prev := types.Hash{0x01}
	next := types.Hash{0x02}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveEquivocationEvidence(tx, ViewNumber(3), ValidatorIndex(1), prev, next)
	}); err != nil {
		t.Fatalf("SaveEquivocationEvidence: %v", err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		val, err := tx.GetOne(modules.HotStuffState, []byte("equivocation/3/1"))
		if err != nil {
			return err
		}
		if len(val) != 64 {
			t.Fatalf("expected 64-byte value, got %d", len(val))
		}
		var gotPrev, gotNext types.Hash
		copy(gotPrev[:], val[0:32])
		copy(gotNext[:], val[32:64])
		if gotPrev != prev || gotNext != next {
			t.Fatalf("mismatched evidence: prev=%x next=%x", gotPrev, gotNext)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}
}
