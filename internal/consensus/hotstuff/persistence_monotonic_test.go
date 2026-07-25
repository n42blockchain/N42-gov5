package hotstuff

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func monotonicTestQC(view ViewNumber, tag byte) QuorumCertificate {
	return QuorumCertificate{
		View:               view,
		BlockHash:          types.Hash{tag},
		AggregateSignature: bytes.Repeat([]byte{tag}, 48),
		Signers:            []bool{true, true, true, false},
	}
}

func saveState(t *testing.T, db kv.RwDB, st *ConsensusState) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatalf("SaveConsensusState: %v", err)
	}
}

func loadState(t *testing.T, db kv.RwDB) *ConsensusState {
	t.Helper()
	var st *ConsensusState
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		st, err = LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadConsensusState: %v", err)
	}
	if st == nil {
		t.Fatal("no state persisted")
	}
	return st
}

// TestSaveConsensusStateNeverWithdrawsAVote is the H1 regression: persistState
// snapshots the engine under the mutex and writes later, so its record can be
// stale by the time the transaction runs. A stale write must not roll the
// durable vote record back — that is exactly the restart-and-double-vote window
// the journal was added to close.
func TestSaveConsensusStateNeverWithdrawsAVote(t *testing.T) {
	db := memdb.NewTestDB(t)

	// The engine journals a Round-1 and a Round-2 vote at view 7.
	current := &ConsensusState{
		View:                7,
		LockedQC:            monotonicTestQC(6, 0xA1),
		LastCommittedQC:     monotonicTestQC(5, 0xA2),
		LastVotedView:       7,
		LastVotedHash:       types.Hash{0x77},
		LastCommitVotedView: 7,
		LastCommitVotedHash: types.Hash{0x78},
	}
	saveState(t, db, current)

	// A periodic persist whose snapshot predates both votes lands afterwards.
	stale := &ConsensusState{
		View:                5,
		LockedQC:            monotonicTestQC(4, 0xB1),
		LastCommittedQC:     monotonicTestQC(3, 0xB2),
		LastVotedView:       5,
		LastVotedHash:       types.Hash{0x55},
		LastCommitVotedView: 4,
		LastCommitVotedHash: types.Hash{0x54},
	}
	saveState(t, db, stale)

	got := loadState(t, db)
	if got.LastVotedView != 7 || got.LastVotedHash != (types.Hash{0x77}) {
		t.Fatalf("prepare vote regressed: view=%d hash=%x", got.LastVotedView, got.LastVotedHash)
	}
	if got.LastCommitVotedView != 7 || got.LastCommitVotedHash != (types.Hash{0x78}) {
		t.Fatalf("commit vote regressed: view=%d hash=%x", got.LastCommitVotedView, got.LastCommitVotedHash)
	}
	if got.View != 7 {
		t.Fatalf("view regressed: got %d, want 7", got.View)
	}
	if got.LockedQC.View != 6 || got.LockedQC.BlockHash != (types.Hash{0xA1}) {
		t.Fatalf("locked QC regressed to view %d hash %x", got.LockedQC.View, got.LockedQC.BlockHash)
	}
	if got.LastCommittedQC.View != 5 {
		t.Fatalf("committed QC regressed to view %d", got.LastCommittedQC.View)
	}
}

// TestSaveConsensusStateAdvancesForward guards the other direction: monotonicity
// must not freeze the record. A newer snapshot replaces the older one wholesale.
func TestSaveConsensusStateAdvancesForward(t *testing.T) {
	db := memdb.NewTestDB(t)

	saveState(t, db, &ConsensusState{
		View:                7,
		ConsecutiveTimeouts: 1,
		LockedQC:            monotonicTestQC(6, 0xA1),
		LastCommittedQC:     monotonicTestQC(5, 0xA2),
		LastVotedView:       7,
		LastVotedHash:       types.Hash{0x77},
		LastCommitVotedView: 7,
		LastCommitVotedHash: types.Hash{0x78},
	})
	saveState(t, db, &ConsensusState{
		View:                9,
		ConsecutiveTimeouts: 3,
		LockedQC:            monotonicTestQC(8, 0xC1),
		LastCommittedQC:     monotonicTestQC(7, 0xC2),
		LastVotedView:       9,
		LastVotedHash:       types.Hash{0x99},
		LastCommitVotedView: 8,
		LastCommitVotedHash: types.Hash{0x88},
	})

	got := loadState(t, db)
	if got.View != 9 || got.ConsecutiveTimeouts != 3 {
		t.Fatalf("view/timeouts did not advance: view=%d timeouts=%d", got.View, got.ConsecutiveTimeouts)
	}
	if got.LockedQC.View != 8 || got.LockedQC.BlockHash != (types.Hash{0xC1}) {
		t.Fatalf("locked QC did not advance: view=%d hash=%x", got.LockedQC.View, got.LockedQC.BlockHash)
	}
	if got.LastCommittedQC.View != 7 {
		t.Fatalf("committed QC did not advance: view=%d", got.LastCommittedQC.View)
	}
	if got.LastVotedView != 9 || got.LastVotedHash != (types.Hash{0x99}) {
		t.Fatalf("prepare vote did not advance: view=%d hash=%x", got.LastVotedView, got.LastVotedHash)
	}
	if got.LastCommitVotedView != 8 || got.LastCommitVotedHash != (types.Hash{0x88}) {
		t.Fatalf("commit vote did not advance: view=%d hash=%x", got.LastCommitVotedView, got.LastCommitVotedHash)
	}
}

// TestSaveConsensusStateGroupsAreIndependent pins that the round snapshot and the
// vote commitments are merged separately: a write may carry a stale round with a
// newer vote (or the reverse), and each half must take the higher one without
// dragging the other along.
func TestSaveConsensusStateGroupsAreIndependent(t *testing.T) {
	db := memdb.NewTestDB(t)

	saveState(t, db, &ConsensusState{
		View:            20,
		LockedQC:        monotonicTestQC(19, 0xD1),
		LastCommittedQC: monotonicTestQC(18, 0xD2),
		LastVotedView:   5,
		LastVotedHash:   types.Hash{0x05},
	})
	saveState(t, db, &ConsensusState{
		View:            10,
		LockedQC:        monotonicTestQC(9, 0xE1),
		LastCommittedQC: monotonicTestQC(8, 0xE2),
		LastVotedView:   9,
		LastVotedHash:   types.Hash{0x09},
	})

	got := loadState(t, db)
	if got.View != 20 || got.LockedQC.View != 19 || got.LockedQC.BlockHash != (types.Hash{0xD1}) {
		t.Fatalf("round snapshot should have stayed at view 20/QC19: view=%d qc=%d hash=%x",
			got.View, got.LockedQC.View, got.LockedQC.BlockHash)
	}
	if got.LastVotedView != 9 || got.LastVotedHash != (types.Hash{0x09}) {
		t.Fatalf("newer vote should have been adopted: view=%d hash=%x", got.LastVotedView, got.LastVotedHash)
	}
}

// TestSaveConsensusStateOverV1RecordKeepsView covers the migration edge: a v1
// record carries no vote history but does carry a view, so the round snapshot
// still has to be compared against it.
func TestSaveConsensusStateOverV1RecordKeepsView(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Hand-build a v1 record (view 30, no vote fields) the way a pre-v2 binary
	// would have written it.
	raw := encodeConsensusStateV1(t, &ConsensusState{
		View:                30,
		ConsecutiveTimeouts: 2,
		LockedQC:            monotonicTestQC(29, 0xF1),
		LastCommittedQC:     monotonicTestQC(28, 0xF2),
	})
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.HotStuffState, hotstuffStateKey, raw)
	}); err != nil {
		t.Fatalf("seed v1 record: %v", err)
	}

	saveState(t, db, &ConsensusState{
		View:            12,
		LockedQC:        monotonicTestQC(11, 0x11),
		LastCommittedQC: monotonicTestQC(10, 0x10),
		LastVotedView:   12,
		LastVotedHash:   types.Hash{0x12},
	})

	got := loadState(t, db)
	if got.View != 30 || got.LockedQC.View != 29 {
		t.Fatalf("v1 round snapshot regressed: view=%d qc=%d", got.View, got.LockedQC.View)
	}
	// The v1 record had no vote history, so the incoming vote is strictly newer.
	if got.LastVotedView != 12 || got.LastVotedHash != (types.Hash{0x12}) {
		t.Fatalf("vote should have been recorded: view=%d hash=%x", got.LastVotedView, got.LastVotedHash)
	}
}
