// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestProcessPendingSlashing_NilValidatorSetErrors covers the valSet()==nil
// guard.
func TestProcessPendingSlashing_NilValidatorSetErrors(t *testing.T) {
	se := NewSlashingExecutor(nil, func() *ValidatorSet { return nil })
	if _, err := se.ProcessPendingSlashing(context.Background()); err == nil {
		t.Fatal("expected an error when the validator set is unavailable")
	}
}

// TestProcessPendingSlashing_SlashesPersistedEvidence covers the full success
// path: persisted equivocation evidence is resolved to an address, the
// deposit is deleted, a slash record is written, and the evidence is cleared.
func TestProcessPendingSlashing_SlashesPersistedEvidence(t *testing.T) {
	setup := newTestSetup(t, 4)
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveEquivocationEvidence(tx, 1, 0, setup.validators[0].Address.Hash(), setup.validators[1].Address.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	se := NewSlashingExecutor(db, func() *ValidatorSet { return setup.vs })
	n, err := se.ProcessPendingSlashing(context.Background())
	if err != nil {
		t.Fatalf("ProcessPendingSlashing: %v", err)
	}
	if n != 1 {
		t.Fatalf("slashed count = %d, want 1", n)
	}

	// Evidence must be cleared after processing.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		records, err := LoadEquivocationEvidence(tx)
		if err != nil {
			return err
		}
		if len(records) != 0 {
			t.Fatalf("expected evidence to be cleared, got %+v", records)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestProcessPendingSlashing_UnknownValidatorIndexSkipped covers the
// "validator index not found" continue branch: evidence for an index outside
// the validator set is skipped rather than failing the whole batch.
func TestProcessPendingSlashing_UnknownValidatorIndexSkipped(t *testing.T) {
	setup := newTestSetup(t, 4)
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveEquivocationEvidence(tx, 1, 99, setup.validators[0].Address.Hash(), setup.validators[1].Address.Hash())
	}); err != nil {
		t.Fatal(err)
	}

	se := NewSlashingExecutor(db, func() *ValidatorSet { return setup.vs })
	n, err := se.ProcessPendingSlashing(context.Background())
	if err != nil {
		t.Fatalf("ProcessPendingSlashing: %v", err)
	}
	if n != 0 {
		t.Fatalf("slashed count = %d, want 0 (unknown index skipped)", n)
	}
}
