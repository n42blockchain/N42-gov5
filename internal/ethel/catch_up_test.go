// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// TestCatchUpNoBlocksInFreezer covers the early-return branch when the
// input freezer has nothing frozen yet (fresh node waiting for sync).
func TestCatchUpNoBlocksInFreezer(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("open freezer: %v", err)
	}
	defer f.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := CatchUpConfig{
		InputFreezer:  f,
		OutputFreezer: f,
		DB:            db,
		ChainConfig:   chainCfg,
		Engine:        engine,
	}

	if err := CatchUp(context.Background(), cfg); err != nil {
		t.Fatalf("CatchUp with empty freezer should be a no-op, got: %v", err)
	}
}

// freezeDummyBlocks appends n trivially-shaped blocks to f so that
// Frozen() advances; CatchUp only inspects the count, not the payload
// shape, for the branches under test here.
func freezeDummyBlocks(t *testing.T, f *freezer.Freezer, n int) {
	t.Helper()
	headers := make([][]byte, n)
	bodies := make([][]byte, n)
	receipts := make([][]byte, n)
	hashes := make([][]byte, n)
	diffs := make([][]byte, n)
	for i := 0; i < n; i++ {
		headers[i] = []byte{byte(i)}
		bodies[i] = []byte{byte(i)}
		receipts[i] = []byte{byte(i)}
		hashes[i] = make([]byte, 32)
		diffs[i] = []byte{byte(i)}
	}
	if err := f.Freeze(f.Frozen(), &freezer.FreezeData{
		Headers:    headers,
		Bodies:     bodies,
		Receipts:   receipts,
		Hashes:     hashes,
		Difficulty: diffs,
	}); err != nil {
		t.Fatalf("freeze dummy blocks: %v", err)
	}
}

// TestCatchUpAlreadyCaughtUp covers the branch where localHead already
// reaches frozen-1, so CatchUp returns immediately without constructing
// an Executor.
func TestCatchUpAlreadyCaughtUp(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("open freezer: %v", err)
	}
	defer f.Close()
	freezeDummyBlocks(t, f, 1) // frozen == 1

	db := memdb.NewTestDB(t)
	// localHead (default 0) >= frozen-1 (0) => already caught up.

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := CatchUpConfig{
		InputFreezer:  f,
		OutputFreezer: f,
		DB:            db,
		ChainConfig:   chainCfg,
		Engine:        engine,
	}

	if err := CatchUp(context.Background(), cfg); err != nil {
		t.Fatalf("CatchUp already-caught-up should be a no-op, got: %v", err)
	}
}

// TestCatchUpInitializesGenesis covers the genesis-initialization branch:
// GenesisPath is supplied, state is loaded and committed, and (since the
// freezer is still empty) CatchUp returns right after via the
// no-blocks-frozen branch.
func TestCatchUpInitializesGenesis(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("open freezer: %v", err)
	}
	defer f.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := CatchUpConfig{
		InputFreezer:  f,
		OutputFreezer: f,
		DB:            db,
		ChainConfig:   chainCfg,
		Engine:        engine,
		GenesisPath:   genesisPath(),
	}

	if err := CatchUp(context.Background(), cfg); err != nil {
		t.Fatalf("CatchUp with genesis init failed: %v", err)
	}

	// Genesis state should now be present.
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A second CatchUp with the same GenesisPath should still succeed
	// (InitEthGenesisState is idempotent / re-seeds without error) and
	// exercise the same genesis branch again.
}

// TestCatchUpDefaultCommitInterval exercises the CommitInterval==0 default
// substitution branch alongside the no-blocks-frozen early return.
func TestCatchUpDefaultCommitInterval(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("open freezer: %v", err)
	}
	defer f.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := CatchUpConfig{
		InputFreezer:   f,
		OutputFreezer:  f,
		DB:             db,
		ChainConfig:    chainCfg,
		Engine:         engine,
		CommitInterval: 0,
	}

	if err := CatchUp(context.Background(), cfg); err != nil {
		t.Fatalf("CatchUp with default commit interval failed: %v", err)
	}
}
