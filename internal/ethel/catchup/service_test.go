// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package catchup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// stubNode is a nodeAccessor that records what the service asks of it.
// It never touches a real MDBX or freezer — the production CatchUp
// function is also stubbed out so the tests exercise the orchestration,
// not the executor internals.
type stubNode struct {
	dir string
}

func (s *stubNode) RwDB() kv.RwDB                     { return nil }
func (s *stubNode) LeavesDir() string                 { return s.dir }
func (s *stubNode) InputFreezer() *freezer.Freezer    { return nil }
func (s *stubNode) OutFreezer() *freezer.Freezer      { return nil }
func (s *stubNode) ChainConfig() *params.ChainConfig  { return params.EthereumMainnetChainConfig }
func (s *stubNode) Engine() consensus.Engine          { return nil }

// TestCatchUp_NoManifestJustExecutes confirms the "local freezer only"
// path: no fetcher invocation, but the executor IS called.
func TestCatchUp_NoManifestJustExecutes(t *testing.T) {
	var executed atomic.Bool
	svc := newForTest(
		Config{CatchUp: conf.CatchUpCfg{CommitInterval: 500}},
		&stubNode{dir: t.TempDir()},
		nil, // no fetcher
		func(_ context.Context, cfg ethel.CatchUpConfig) error {
			executed.Store(true)
			if cfg.CommitInterval != 500 {
				return fmt.Errorf("CommitInterval: got %d, want 500", cfg.CommitInterval)
			}
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !executed.Load() {
		t.Fatal("executor was not called")
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestCatchUp_ManifestMissingFetcher pins the wiring-bug guard: a
// manifest was configured but no Fetcher was injected.
func TestCatchUp_ManifestMissingFetcher(t *testing.T) {
	svc := newForTest(
		Config{Manifest: "file:///nope"},
		&stubNode{dir: t.TempDir()},
		nil, // no fetcher
		func(context.Context, ethel.CatchUpConfig) error { return nil },
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "no Fetcher available") {
		t.Fatalf("expected wiring error, got %v", err)
	}
}

// fakeFetcher writes a zero-length file for every asset it is asked to
// fetch. The catchup service does not care about the content — that is
// the executor's job — so this is enough to test the pipeline.
type fakeFetcher struct {
	calls []string
	err   error
}

func (f *fakeFetcher) Kinds() []fetch.SourceKind { return []fetch.SourceKind{fetch.SourceHTTPS} }
func (f *fakeFetcher) Fetch(_ context.Context, a fetch.Asset, dstDir string, _ fetch.ProgressFunc) error {
	f.calls = append(f.calls, a.Name)
	if f.err != nil {
		return f.err
	}
	return os.WriteFile(filepath.Join(dstDir, a.Name), []byte("segment payload"), 0o644)
}

// TestCatchUp_ManifestHappyPath drives the full pipeline: a segments
// manifest on disk → fakeFetcher writes files into the freezer dir →
// executor runs afterward. Pins the ordering contract: fetch all
// segments before calling the executor.
func TestCatchUp_ManifestHappyPath(t *testing.T) {
	dir := t.TempDir()

	payload := []byte("segment payload")
	hash := sha256.Sum256(payload)
	manifest := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindSegments,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{
			{
				Name:      "seg-000000-008192.era",
				SizeBytes: uint64(len(payload)),
				SHA256:    hex.EncodeToString(hash[:]),
				FromBlock: 0,
				ToBlock:   8192,
				Sources: []fetch.ManifestSource{
					{Kind: fetch.SourceHTTPS, URI: "https://example/seg-0", Priority: 100},
				},
			},
			{
				Name:      "seg-008193-016384.era",
				SizeBytes: uint64(len(payload)),
				SHA256:    hex.EncodeToString(hash[:]),
				FromBlock: 8193,
				ToBlock:   16384,
				Sources: []fetch.ManifestSource{
					{Kind: fetch.SourceHTTPS, URI: "https://example/seg-1", Priority: 100},
				},
			},
		},
	}
	manifestBytes, _ := json.Marshal(manifest)
	manifestFile := filepath.Join(dir, "segments.json")
	if err := os.WriteFile(manifestFile, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeFetcher{}
	var executorCalled atomic.Bool
	svc := newForTest(
		Config{
			CatchUp:  conf.CatchUpCfg{CommitInterval: 1000},
			Manifest: manifestFile,
		},
		&stubNode{dir: dir},
		fake,
		func(_ context.Context, _ ethel.CatchUpConfig) error {
			executorCalled.Store(true)
			// By the time the executor runs, both segments must
			// already exist on disk. Assert that.
			for _, name := range []string{"seg-000000-008192.era", "seg-008193-016384.era"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					return fmt.Errorf("segment %s missing when executor ran: %w", name, err)
				}
			}
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("fetch calls: %v", fake.calls)
	}
	if fake.calls[0] != "seg-000000-008192.era" || fake.calls[1] != "seg-008193-016384.era" {
		t.Fatalf("fetch order: %v", fake.calls)
	}
	if !executorCalled.Load() {
		t.Fatal("executor was not called")
	}
}

// TestCatchUp_ManifestWrongKind confirms a leaves manifest is rejected
// by the catchup service even though the JSON is otherwise valid.
func TestCatchUp_ManifestWrongKind(t *testing.T) {
	dir := t.TempDir()
	hash := sha256.Sum256([]byte("x"))
	m := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindLeaves,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{{
			Name:      "leaves.cdat",
			SizeBytes: 1,
			SHA256:    hex.EncodeToString(hash[:]),
			Sources:   []fetch.ManifestSource{{Kind: fetch.SourceHTTPS, URI: "https://x"}},
		}},
	}
	body, _ := json.Marshal(m)
	manifestFile := filepath.Join(dir, "leaves.json")
	if err := os.WriteFile(manifestFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := newForTest(
		Config{Manifest: manifestFile},
		&stubNode{dir: dir},
		&fakeFetcher{},
		func(context.Context, ethel.CatchUpConfig) error { return nil },
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "expected") {
		t.Fatalf("expected wrong-kind error, got %v", err)
	}
}

// TestCatchUp_FetcherFailureAborts pins that a fetcher error aborts
// the whole Start — the executor must NOT run on partial data.
func TestCatchUp_FetcherFailureAborts(t *testing.T) {
	dir := t.TempDir()
	hash := sha256.Sum256([]byte("x"))
	m := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindSegments,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{{
			Name:      "seg.bin",
			SizeBytes: 1,
			SHA256:    hex.EncodeToString(hash[:]),
			Sources:   []fetch.ManifestSource{{Kind: fetch.SourceHTTPS, URI: "https://x"}},
		}},
	}
	body, _ := json.Marshal(m)
	manifestFile := filepath.Join(dir, "segments.json")
	if err := os.WriteFile(manifestFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var executorCalled atomic.Bool
	svc := newForTest(
		Config{Manifest: manifestFile},
		&stubNode{dir: dir},
		&fakeFetcher{err: errors.New("transport exploded")},
		func(context.Context, ethel.CatchUpConfig) error {
			executorCalled.Store(true)
			return nil
		},
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "transport exploded") {
		t.Fatalf("expected wrapped fetcher error, got %v", err)
	}
	if executorCalled.Load() {
		t.Fatal("executor ran despite fetch failure")
	}
}

// TestCatchUp_ExecutorFailureSurfacesWrapped confirms executor errors
// are surfaced with the 'catch-up: executor:' wrapper.
func TestCatchUp_ExecutorFailureSurfacesWrapped(t *testing.T) {
	svc := newForTest(
		Config{},
		&stubNode{dir: t.TempDir()},
		nil,
		func(context.Context, ethel.CatchUpConfig) error {
			return errors.New("executor boom")
		},
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "executor") || !errContains(err, "executor boom") {
		t.Fatalf("expected wrapped executor error, got %v", err)
	}
}

func errContains(err error, sub string) bool {
	if err == nil {
		return false
	}
	return stringContains(err.Error(), sub)
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
