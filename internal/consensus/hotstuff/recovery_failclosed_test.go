package hotstuff

import (
	"context"
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestServiceRejectsCorruptRecovery(t *testing.T) {
	for _, mode := range []string{"consensus", "active-epoch", "staged-epoch", "future-qc", "future-qc-behind"} {
		t.Run(mode, func(t *testing.T) {
			setup := newTestSetup(t, 4)
			e, _ := newTestEngine(t, setup, 0)
			db := memdb.NewTestDB(t)
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				key := hotstuffStateKey
				switch mode {
				case "active-epoch":
					key = activeEpochKey
				case "staged-epoch":
					key = stagedEpochKey
				case "future-qc", "future-qc-behind":
					if mode == "future-qc-behind" {
						e.roundState.AdvanceView(20)
					}
					st := &ConsensusState{View: 10, LockedQC: GenesisQC(), LastCommittedQC: GenesisQC(), LastVotedView: 10, LastVotedHash: types.Hash{42}}
					st.LockedQC.View = 11
					return SaveConsensusState(tx, st)
				}
				return tx.Put(modules.HotStuffState, key, []byte{1, 2, 3})
			}); err != nil {
				t.Fatal(err)
			}
			s := NewService(&HotStuff{engine: e}, nil, db, "test", "test")
			defer s.cancel()
			if err := s.Start(); err == nil || !strings.Contains(err.Error(), "recover durable consensus state") {
				t.Fatalf("startup accepted corrupt %s: %v", mode, err)
			}
			if e.voteJournal != nil {
				t.Fatal("startup enabled voting after failed recovery")
			}
		})
	}
}

func TestServiceRecoveryPreservesVoteCommitments(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)
	db := memdb.NewTestDB(t)
	s := NewService(&HotStuff{engine: e}, nil, db, "test", "test")
	defer s.cancel()
	if err := s.recoverState(); err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	st := &ConsensusState{View: 10, LockedQC: GenesisQC(), LastCommittedQC: GenesisQC(), LastVotedView: 10, LastVotedHash: types.Hash{42}, LastCommitVotedView: 9, LastCommitVotedHash: types.Hash{41}}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error { return SaveConsensusState(tx, st) }); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if e.CurrentView() != 10 || !e.roundState.HasVotedInView(10) || !e.roundState.HasCommitVotedInView(9) {
		t.Fatal("recovery lost durable vote commitments")
	}
}

func TestRecoveryRecordBounds(t *testing.T) {
	db := memdb.NewTestDB(t)
	readers := []struct {
		key  []byte
		read func(kv.Tx) error
	}{
		{hotstuffStateKey, func(tx kv.Tx) error { _, e := LoadConsensusState(tx); return e }},
		{activeEpochKey, func(tx kv.Tx) error { _, _, _, _, e := LoadActiveEpoch(tx); return e }},
		{stagedEpochKey, func(tx kv.Tx) error { _, _, _, e := LoadStagedEpoch(tx); return e }},
	}
	for _, reader := range readers {
		for size := 0; size < 16; size++ {
			if err := db.Update(context.Background(), func(tx kv.RwTx) error { return tx.Put(modules.HotStuffState, reader.key, make([]byte, size)) }); err != nil {
				t.Fatal(err)
			}
			err := db.View(context.Background(), reader.read)
			if err == nil {
				t.Fatalf("%s accepted %d-byte record", reader.key, size)
			}
		}
	}
	for _, reader := range readers[1:] {
		for _, size := range []int{16, 40} {
			record := make([]byte, size)
			binary.LittleEndian.PutUint32(record[12:16], math.MaxUint32)
			if size == 40 {
				binary.LittleEndian.PutUint32(record[12:16], 1)
				binary.LittleEndian.PutUint32(record[36:40], math.MaxUint32)
			}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error { return tx.Put(modules.HotStuffState, reader.key, record) }); err != nil {
				t.Fatal(err)
			}
			if err := db.View(context.Background(), reader.read); err == nil {
				t.Fatalf("%s accepted oversized count or key", reader.key)
			}
		}
	}
}

func TestRecoveryLengthPrefixBounds(t *testing.T) {
	for _, length := range []uint32{math.MaxInt32, math.MaxUint32} {
		encoded := make([]byte, 8)
		binary.LittleEndian.PutUint32(encoded[4:], length)
		if _, _, err := readLenPrefixed(encoded, 4, "qc"); err == nil {
			t.Fatal("oversized QC prefix accepted")
		}
	}
	if _, _, err := readLenPrefixed(nil, -1, "qc"); err == nil {
		t.Fatal("negative offset accepted")
	}
}
