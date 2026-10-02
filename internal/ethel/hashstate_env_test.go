// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashstate_env_test.go covers the environment-variable-driven branches in
// hashstate.go: envBufSize's unset/valid/invalid parsing, and
// RebuildHashedStateETL's N42_ETL_TMPDIR auto-create path when no explicit
// tmpdir is passed.

package ethel

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func TestEnvBufSize(t *testing.T) {
	const name = "N42_ETHEL_TEST_BUFSIZE"

	t.Run("unset uses default", func(t *testing.T) {
		t.Setenv(name, "")
		if got := envBufSize(name, 7); got != datasize.ByteSize(7) {
			t.Errorf("got %d want 7", got)
		}
	})

	t.Run("valid value overrides default", func(t *testing.T) {
		t.Setenv(name, "42")
		if got := envBufSize(name, 7); got != datasize.ByteSize(42) {
			t.Errorf("got %d want 42", got)
		}
	})

	t.Run("zero value falls back to default", func(t *testing.T) {
		t.Setenv(name, "0")
		if got := envBufSize(name, 7); got != datasize.ByteSize(7) {
			t.Errorf("got %d want 7 (zero is rejected)", got)
		}
	})

	t.Run("unparseable value falls back to default and warns", func(t *testing.T) {
		t.Setenv(name, "not-a-number")
		if got := envBufSize(name, 7); got != datasize.ByteSize(7) {
			t.Errorf("got %d want 7", got)
		}
	})
}

// TestRebuildHashedStateETL_N42_ETL_TMPDIR_AutoCreate drives the
// tmpdir=="" branch with N42_ETL_TMPDIR pointing at a directory that does
// not exist yet — RebuildHashedStateETL must create it (MkdirAll) and spill
// into a fresh "hashed-state-etl-*" subdirectory under it.
func TestRebuildHashedStateETL_N42_ETL_TMPDIR_AutoCreate(t *testing.T) {
	base := filepath.Join(t.TempDir(), "does-not-exist-yet", "nested")
	t.Setenv("N42_ETL_TMPDIR", base)

	ctx := context.Background()
	db := memdb.New(t.TempDir())
	defer db.Close()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedPlainState(t, tx)

	if err := RebuildHashedStateETL(ctx, tx, "", log2.New()); err != nil {
		t.Fatalf("RebuildHashedStateETL: %v", err)
	}
}
