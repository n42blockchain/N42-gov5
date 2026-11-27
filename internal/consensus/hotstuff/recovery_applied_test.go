// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Recovery marks the committed head and the locked block as imported only
// when each is executed locally, judged by hash on the applied-marker
// lineage: a canonical row or a matching height is not execution.

func recWriteHeader(t *testing.T, db kv.RwDB, number uint64, parent types.Hash, tag byte, canonical bool) types.Hash {
	t.Helper()
	h := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		ParentHash: parent,
		Root:       types.Hash{tag},
		Extra:      []byte{tag},
	}
	hash := h.Hash()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, h)
		if canonical {
			return rawdb.WriteCanonicalHash(tx, hash, number)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func recSetApplied(t *testing.T, db kv.RwDB, number uint64, hash types.Hash) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteQMDBApplied(tx, number, hash)
	}); err != nil {
		t.Fatal(err)
	}
}

func recSaveState(t *testing.T, s *Service, st *ConsensusState) {
	t.Helper()
	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatal(err)
	}
}

func recImported(e *ConsensusEngine, h types.Hash) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.importedBlocks[h]
}

// Committed head 10 and lock 11 both applied: both count as imported.
func TestRecoveryMarksAppliedCommittedHeadAndLock(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, h := newTestServiceWithDB(t, setup)
	b9 := recWriteHeader(t, s.db, 9, types.Hash{}, 0x09, true)
	b10 := recWriteHeader(t, s.db, 10, b9, 0x10, true)
	b11 := recWriteHeader(t, s.db, 11, b10, 0x11, false)
	recSetApplied(t, s.db, 11, b11)
	recSaveState(t, s, &ConsensusState{
		View:            20,
		LockedQC:        QuorumCertificate{View: 12, BlockHash: b11},
		LastCommittedQC: QuorumCertificate{View: 11, BlockHash: b10},
	})
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	e := h.Engine()
	if !recImported(e, b10) {
		t.Fatal("applied committed head not marked imported")
	}
	if !recImported(e, b11) {
		t.Fatal("applied locked block not marked imported; a whole-set restart locked above the head could never vote")
	}
}

// The committed head is canonical, but the applied state sits on a sibling at
// the same height: it is not executed here and must not count as imported.
func TestRecoveryDoesNotMarkCanonicalButUnappliedHead(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, h := newTestServiceWithDB(t, setup)
	b9 := recWriteHeader(t, s.db, 9, types.Hash{}, 0x09, true)
	b10 := recWriteHeader(t, s.db, 10, b9, 0x10, true)
	sib10 := recWriteHeader(t, s.db, 10, b9, 0xA0, false)
	recSetApplied(t, s.db, 10, sib10)
	recSaveState(t, s, &ConsensusState{
		View:            20,
		LockedQC:        QuorumCertificate{View: 11, BlockHash: b10},
		LastCommittedQC: QuorumCertificate{View: 11, BlockHash: b10},
	})
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if recImported(h.Engine(), b10) {
		t.Fatal("a canonical block whose state is a sibling's was marked imported")
	}
}

// A locked block at a height the applied lineage covers, but a different hash
// (an unapplied sibling), is not marked; neither is one above the applied head.
func TestRecoveryDoesNotMarkUnappliedLock(t *testing.T) {
	setup := newTestSetup(t, 4)
	for _, tc := range []struct {
		name   string
		number uint64
	}{{"sibling at the applied height", 11}, {"above the applied head", 12}} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := newTestServiceWithDB(t, setup)
			b9 := recWriteHeader(t, s.db, 9, types.Hash{}, 0x09, true)
			b10 := recWriteHeader(t, s.db, 10, b9, 0x10, true)
			b11 := recWriteHeader(t, s.db, 11, b10, 0x11, false)
			recSetApplied(t, s.db, 11, b11)
			parent := b10
			if tc.number == 12 {
				parent = b11
			}
			lock := recWriteHeader(t, s.db, tc.number, parent, 0xB0, false)
			recSaveState(t, s, &ConsensusState{
				View:            20,
				LockedQC:        QuorumCertificate{View: 13, BlockHash: lock},
				LastCommittedQC: QuorumCertificate{View: 11, BlockHash: b10},
			})
			if err := s.recoverState(); err != nil {
				t.Fatal(err)
			}
			if recImported(h.Engine(), lock) {
				t.Fatal("an unapplied locked block was marked imported")
			}
			if !recImported(h.Engine(), b10) {
				t.Fatal("the applied committed head was not marked")
			}
		})
	}
}
