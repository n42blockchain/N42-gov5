// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package publicrpc

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// writeHeadHeaderAt writes a header at number n and points the head-header
// pointer at it, so rawdb.ReadCurrentBlockNumber (and therefore
// headBlockNumber) resolves to n.
func writeHeadHeaderAt(t *testing.T, tx kv.RwTx, n uint64) {
	t.Helper()
	h := mkHeader(n, types.Hash{})
	rawdb.WriteHeader(tx, h)
	if err := rawdb.WriteHeadHeaderHash(tx, h.Hash()); err != nil {
		t.Fatal(err)
	}
}

func TestHeadBlockNumber_CurrentBlockMarker(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writeHeadHeaderAt(t, tx, 7)
	if n := headBlockNumber(tx); n != 7 {
		t.Errorf("headBlockNumber = %d, want 7", n)
	}
}

func TestHeadBlockNumber_HeadMarkerFallback(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ethel.WriteHeadMarker(tx, 11); err != nil {
		t.Fatal(err)
	}
	if n := headBlockNumber(tx); n != 11 {
		t.Errorf("headBlockNumber = %d, want 11", n)
	}
}

func TestHeadBlockNumber_Zero(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if n := headBlockNumber(tx); n != 0 {
		t.Errorf("headBlockNumber = %d, want 0", n)
	}
}

func TestService_CurrentHead(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeHeadHeaderAt(t, tx, 5)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	svc := &Service{db: db}
	if got := svc.currentHead(); got != 5 {
		t.Errorf("currentHead() = %d, want 5", got)
	}
}

func TestService_CurrentHead_NilDB(t *testing.T) {
	svc := &Service{}
	if got := svc.currentHead(); got != 0 {
		t.Errorf("currentHead() with nil db = %d, want 0", got)
	}
}

func TestBuildStateReader_NilTx(t *testing.T) {
	if _, err := buildStateReader(Config{}, nil, 0); err == nil {
		t.Fatal("expected error for nil tx")
	}
}

func TestBuildStateReader_FullArchive(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, mode := range []rpccaps.Mode{rpccaps.Full, rpccaps.Archive} {
		r, err := buildStateReader(Config{Mode: mode}, tx, 10)
		if err != nil || r == nil {
			t.Errorf("%s: buildStateReader = %v, %v", mode, r, err)
		}
	}
}

func TestBuildStateReader_M1NoSnapshot(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := buildStateReader(Config{Mode: rpccaps.M1}, tx, 0); err == nil {
		t.Fatal("expected error: M1 needs a snapshot segment")
	}
}

func TestBuildStateReader_UnsupportedMode(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := buildStateReader(Config{Mode: rpccaps.M0}, tx, 0); err == nil {
		t.Fatal("expected error: M0 is not state-backed")
	}
}

func TestBuildStateReader_HashedCanonical_Latest(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writeHeadHeaderAt(t, tx, 5)
	r, err := buildStateReader(Config{HashedCanonical: true, Mode: rpccaps.Archive}, tx, 5)
	if err != nil || r == nil {
		t.Fatalf("buildStateReader(hashed, latest) = %v, %v", r, err)
	}
}

func TestBuildStateReader_HashedCanonical_HistoricalNonArchive(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writeHeadHeaderAt(t, tx, 10)
	if _, err := buildStateReader(Config{HashedCanonical: true, Mode: rpccaps.Full}, tx, 3); err == nil {
		t.Fatal("expected error: historical state not available in Full mode")
	}
}

func TestBuildStateReader_HashedCanonical_HistoricalArchive(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	writeHeadHeaderAt(t, tx, 10)
	r, err := buildStateReader(Config{HashedCanonical: true, Mode: rpccaps.Archive}, tx, 3)
	if err != nil || r == nil {
		t.Fatalf("buildStateReader(hashed, historical, archive) = %v, %v", r, err)
	}
}

// TestNew_NilChainConfig covers New's early guard.
func TestNew_NilChainConfig(t *testing.T) {
	if _, err := New(Config{}, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error for nil chain config")
	}
}

// TestNew_Minimal builds a full Service with no DATC dir, over an empty
// memdb, and exercises Name/Stop (Start is skipped since cfg.Enabled is
// false by default, matching Disabled-like behavior for binding a listener).
func TestNew_Minimal(t *testing.T) {
	db := memdb.NewTestDB(t)
	svc, err := New(Config{Mode: rpccaps.Archive}, params.MainnetChainConfig, nil, db, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.Name() != "publicRPC" {
		t.Errorf("Name() = %q", svc.Name())
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start (disabled): %v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestNew_StartEnabled actually binds a listener on an ephemeral port and
// shuts it down, covering the enabled Start/Stop path.
func TestNew_StartEnabled(t *testing.T) {
	db := memdb.NewTestDB(t)
	svc, err := New(Config{Enabled: true, Host: "127.0.0.1", Port: 0, Mode: rpccaps.Archive}, params.MainnetChainConfig, nil, db, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
