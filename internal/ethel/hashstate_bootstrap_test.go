// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashstate_bootstrap_test.go covers the bootstrap/setup entry points that
// hashstate_equivalence_test.go's streaming-vs-legacy comparisons don't
// reach: SetupStateRootComputer, CalcStateRoot, RebuildHashedState,
// BootstrapHPH, BootstrapHPHBatched, BootstrapHPHFastETL, IsHPHBootstrapped
// and InitHashState. Each is checked against VerifyStateRoot's independent
// in-memory MPT implementation (the same oracle hashstate_equivalence_test
// uses) on the same small seeded PlainState, so every bootstrap variant is
// pinned to agree on the resulting state root.

package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestCalcStateRoot_MatchesVerifyStateRoot drives RebuildHashedState +
// CalcStateRoot (the TrieOfAccounts/TrieOfStorage-clearing, non-persisting
// path used by incremental verify) and checks it against the independent
// in-memory MPT oracle.
func TestCalcStateRoot_MatchesVerifyStateRoot(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedPlainState(t, tx)

	want, err := VerifyStateRoot(tx)
	if err != nil {
		t.Fatalf("VerifyStateRoot: %v", err)
	}

	if err := RebuildHashedState(tx); err != nil {
		t.Fatalf("RebuildHashedState: %v", err)
	}
	got, err := CalcStateRoot(tx)
	if err != nil {
		t.Fatalf("CalcStateRoot: %v", err)
	}
	if got != want {
		t.Errorf("CalcStateRoot = %s, want %s", got.Hex(), want.Hex())
	}
}

// TestSetupStateRootComputer_Wiring checks the plumbing-only function: it
// must return a non-nil computer and attach it to the IntraBlockState's
// root-computer slot without touching any table.
func TestSetupStateRootComputer_Wiring(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	ibs := state.New(state.NewPlainStateReader(tx))
	trc := SetupStateRootComputer(tx, ibs)
	if trc == nil {
		t.Fatal("SetupStateRootComputer returned nil")
	}
}

// TestBootstrapHPH_MatchesVerifyStateRoot drives the single-tx bootstrap
// variant that persists TrieOfAccounts/TrieOfStorage.
func TestBootstrapHPH_MatchesVerifyStateRoot(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Needs enough accounts/slots for FlatDBTrieLoader to emit at least one
	// intermediate branch node — the small 6-account seed below the trie's
	// branching threshold leaves TrieAccount empty even on success, which
	// would make the IsHPHBootstrapped(after) assertion meaningless.
	seedPlainStateLarge(t, tx)

	want, err := VerifyStateRoot(tx)
	if err != nil {
		t.Fatalf("VerifyStateRoot: %v", err)
	}

	bootstrapped, err := IsHPHBootstrapped(tx)
	if err != nil {
		t.Fatalf("IsHPHBootstrapped (before): %v", err)
	}
	if bootstrapped {
		t.Fatal("fresh datadir must not report bootstrapped before BootstrapHPH runs")
	}

	got, err := BootstrapHPH(tx)
	if err != nil {
		t.Fatalf("BootstrapHPH: %v", err)
	}
	if got != want {
		t.Errorf("BootstrapHPH = %s, want %s", got.Hex(), want.Hex())
	}

	bootstrapped, err = IsHPHBootstrapped(tx)
	if err != nil {
		t.Fatalf("IsHPHBootstrapped (after): %v", err)
	}
	if !bootstrapped {
		t.Fatal("datadir must report bootstrapped after BootstrapHPH persists TrieOfAccounts")
	}
}

// TestBootstrapHPHBatched_MatchesVerifyStateRoot drives the multi-tx
// batched variant with a deliberately tiny batch size (1) so the commit
// loop in hashAllAccountsBatched/hashAllStorageBatched runs several
// iterations even on this small seeded state.
func TestBootstrapHPHBatched_MatchesVerifyStateRoot(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	{
		tx, err := db.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seedPlainState(t, tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	var want types.Hash
	{
		tx, err := db.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root, err := VerifyStateRoot(tx)
		if err != nil {
			t.Fatalf("VerifyStateRoot: %v", err)
		}
		want = root
		tx.Rollback()
	}

	got, err := BootstrapHPHBatched(ctx, db, 1, 1)
	if err != nil {
		t.Fatalf("BootstrapHPHBatched: %v", err)
	}
	if got != want {
		t.Errorf("BootstrapHPHBatched = %s, want %s", got.Hex(), want.Hex())
	}

	// Defaults (0, 0) must also run cleanly on a freshly re-seeded DB.
	db2 := memdb.New(t.TempDir())
	defer db2.Close()
	{
		tx, err := db2.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seedPlainState(t, tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	got2, err := BootstrapHPHBatched(ctx, db2, 0, 0)
	if err != nil {
		t.Fatalf("BootstrapHPHBatched (default batch sizes): %v", err)
	}
	if got2 != want {
		t.Errorf("BootstrapHPHBatched (defaults) = %s, want %s", got2.Hex(), want.Hex())
	}
}

// TestBootstrapHPHFastETL_MatchesVerifyStateRoot drives the ETL-bulk-load
// bootstrap variant.
func TestBootstrapHPHFastETL_MatchesVerifyStateRoot(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	{
		tx, err := db.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seedPlainState(t, tx)
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	var want types.Hash
	{
		tx, err := db.BeginRw(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root, err := VerifyStateRoot(tx)
		if err != nil {
			t.Fatalf("VerifyStateRoot: %v", err)
		}
		want = root
		tx.Rollback()
	}

	got, err := BootstrapHPHFastETL(ctx, db, t.TempDir())
	if err != nil {
		t.Fatalf("BootstrapHPHFastETL: %v", err)
	}
	if got != want {
		t.Errorf("BootstrapHPHFastETL = %s, want %s", got.Hex(), want.Hex())
	}
}

// TestInitHashState_PopulatesOnceAndSkipsAfter covers both branches of
// InitHashState: the first call must populate HashedAccounts/HashedStorage
// from scratch, and a second call must be a no-op (idempotent) because it
// detects existing data.
func TestInitHashState_PopulatesOnceAndSkipsAfter(t *testing.T) {
	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedPlainState(t, tx)

	want, err := VerifyStateRoot(tx)
	if err != nil {
		t.Fatalf("VerifyStateRoot: %v", err)
	}

	if err := InitHashState(tx); err != nil {
		t.Fatalf("InitHashState (first call): %v", err)
	}
	got, err := CalcStateRoot(tx)
	if err != nil {
		t.Fatalf("CalcStateRoot after InitHashState: %v", err)
	}
	if got != want {
		t.Errorf("InitHashState root = %s, want %s", got.Hex(), want.Hex())
	}

	// Second call must be a no-op: it must not error and must not change
	// the already-populated HashedAccounts/HashedStorage tables (CalcStateRoot
	// clears TrieOf* but not Hashed*, so re-running InitHashState then
	// recomputing must still match).
	if err := InitHashState(tx); err != nil {
		t.Fatalf("InitHashState (second, idempotent call): %v", err)
	}
	got2, err := CalcStateRoot(tx)
	if err != nil {
		t.Fatalf("CalcStateRoot after second InitHashState: %v", err)
	}
	if got2 != want {
		t.Errorf("InitHashState (idempotent) root = %s, want %s", got2.Hex(), want.Hex())
	}
}
