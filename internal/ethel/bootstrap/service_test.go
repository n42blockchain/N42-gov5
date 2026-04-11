// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/lib/kv"
)

// stubNode is a nodeAccessor that records what the service asks of it.
// It holds no real MDBX — HasPopulatedState returns whatever the test
// plants, RwDB returns a typed nil that never gets dereferenced because
// the stubRebuild does not use it.
type stubNode struct {
	dir       string
	populated bool
	probeErr  error
}

func (s *stubNode) RwDB() kv.RwDB        { return nil }
func (s *stubNode) LeavesDir() string    { return s.dir }
func (s *stubNode) HasPopulatedState(_ context.Context) (bool, error) {
	return s.populated, s.probeErr
}

// TestBootstrap_DisabledNoop confirms the service does nothing (no
// probes, no rebuild) when Enabled=false.
func TestBootstrap_DisabledNoop(t *testing.T) {
	var rebuildCalled atomic.Bool
	svc := newForTest(
		conf.BootstrapCfg{Enabled: false},
		&stubNode{},
		nil,
		func(context.Context, kv.RwDB, string, uint64) error {
			rebuildCalled.Store(true)
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rebuildCalled.Load() {
		t.Fatalf("rebuild called when bootstrap disabled")
	}
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestBootstrap_SkipPopulated confirms the service does not re-run
// rebuild on a warm chaindata.
func TestBootstrap_SkipPopulated(t *testing.T) {
	var rebuildCalled atomic.Bool
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true},
		&stubNode{populated: true},
		nil,
		func(context.Context, kv.RwDB, string, uint64) error {
			rebuildCalled.Store(true)
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rebuildCalled.Load() {
		t.Fatalf("rebuild called on populated chaindata")
	}
}

// TestBootstrap_LocalOnly exercises the "no manifest configured" path:
// the service calls rebuild directly with whatever is already on disk.
func TestBootstrap_LocalOnly(t *testing.T) {
	dir := t.TempDir()
	var gotDir string
	var gotEnd uint64
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true, EndBlock: 12345},
		&stubNode{dir: dir},
		nil,
		func(_ context.Context, _ kv.RwDB, d string, endBlock uint64) error {
			gotDir = d
			gotEnd = endBlock
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if gotDir != dir {
		t.Fatalf("rebuild leavesDir: got %q, want %q", gotDir, dir)
	}
	if gotEnd != 12345 {
		t.Fatalf("rebuild endBlock: got %d, want 12345", gotEnd)
	}
}

// TestBootstrap_ManifestMissingFetcher exercises the guard that refuses
// to pretend the bootstrap ran when a manifest was configured but no
// Fetcher was injected — that state is almost always a wiring bug.
func TestBootstrap_ManifestMissingFetcher(t *testing.T) {
	dir := t.TempDir()
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true, Manifest: "file:///nope"},
		&stubNode{dir: dir},
		nil, // no fetcher
		func(context.Context, kv.RwDB, string, uint64) error { return nil },
	)
	err := svc.Start(context.Background())
	if err == nil {
		t.Fatal("expected error when manifest set but fetcher nil")
	}
	if !errContains(err, "no Fetcher available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// fakeFetcher records every Fetch call and optionally writes a file so
// we can verify the full download → rebuild pipeline.
type fakeFetcher struct {
	written map[string][]byte
	calls   []string
	err     error
}

func (f *fakeFetcher) Kinds() []fetch.SourceKind { return []fetch.SourceKind{fetch.SourceHTTPS} }
func (f *fakeFetcher) Fetch(_ context.Context, a fetch.Asset, dstDir string, _ fetch.ProgressFunc) error {
	f.calls = append(f.calls, a.Name)
	if f.err != nil {
		return f.err
	}
	data, ok := f.written[a.Name]
	if !ok {
		return fmt.Errorf("fakeFetcher: unknown asset %q", a.Name)
	}
	return os.WriteFile(filepath.Join(dstDir, a.Name), data, 0o644)
}

// TestBootstrap_ManifestHappyPath drives the full pipeline: a manifest
// served over HTTP → Fetcher writes bytes to leaves dir → rebuild is
// called with the right arguments. It pins the order of operations:
// fetch every asset before rebuild.
func TestBootstrap_ManifestHappyPath(t *testing.T) {
	dir := t.TempDir()

	leaves := []byte("leaves-segment-contents")
	hash := sha256.Sum256(leaves)

	manifest := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindLeaves,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{
			{
				Name:      "leaves-0.bin",
				SizeBytes: uint64(len(leaves)),
				SHA256:    hex.EncodeToString(hash[:]),
				Sources: []fetch.ManifestSource{
					{Kind: fetch.SourceHTTPS, URI: "https://example/leaves-0.bin", Priority: 100},
				},
			},
		},
	}
	manifestBytes, _ := json.Marshal(manifest)

	var manifestHits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestHits.Add(1)
		_, _ = w.Write(manifestBytes)
	}))
	defer srv.Close()

	// We cannot use the httptest TLS server directly via LoadManifest
	// (self-signed cert), so write the manifest to a temp file and
	// point the service at it.
	manifestFile := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestFile, manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	fake := &fakeFetcher{
		written: map[string][]byte{"leaves-0.bin": leaves},
	}
	var rebuildCalled atomic.Bool
	var gotDir string
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true, Manifest: manifestFile},
		&stubNode{dir: dir},
		fake,
		func(_ context.Context, _ kv.RwDB, d string, _ uint64) error {
			rebuildCalled.Store(true)
			gotDir = d
			// By the time rebuild runs, the fetcher must have already
			// written the asset file. Assert it.
			data, err := os.ReadFile(filepath.Join(d, "leaves-0.bin"))
			if err != nil {
				return fmt.Errorf("asset not on disk before rebuild: %w", err)
			}
			if string(data) != string(leaves) {
				return fmt.Errorf("asset content wrong: %q", data)
			}
			return nil
		},
	)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "leaves-0.bin" {
		t.Fatalf("fetch calls: %v", fake.calls)
	}
	if !rebuildCalled.Load() {
		t.Fatalf("rebuild was not called")
	}
	if gotDir != dir {
		t.Fatalf("rebuild leavesDir: got %q, want %q", gotDir, dir)
	}
}

// TestBootstrap_ManifestWrongKind confirms a segments manifest is
// refused by the bootstrap service even when the file parses cleanly.
func TestBootstrap_ManifestWrongKind(t *testing.T) {
	dir := t.TempDir()
	hash := sha256.Sum256([]byte("x"))
	m := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindSegments,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{{
			Name:      "x.bin",
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
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true, Manifest: manifestFile},
		&stubNode{dir: dir},
		&fakeFetcher{},
		func(context.Context, kv.RwDB, string, uint64) error { return nil },
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "expected") {
		t.Fatalf("expected wrong-kind error, got %v", err)
	}
}

// TestBootstrap_FetcherFailureAborts pins that a fetcher error aborts
// the whole Start — rebuild must NOT be called on partial data.
func TestBootstrap_FetcherFailureAborts(t *testing.T) {
	dir := t.TempDir()
	hash := sha256.Sum256([]byte("x"))
	m := fetch.Manifest{
		Version: fetch.ManifestVersion,
		Kind:    fetch.ManifestKindLeaves,
		ChainID: 1,
		Assets: []fetch.ManifestAsset{{
			Name:      "x.bin",
			SizeBytes: 1,
			SHA256:    hex.EncodeToString(hash[:]),
			Sources:   []fetch.ManifestSource{{Kind: fetch.SourceHTTPS, URI: "https://x"}},
		}},
	}
	body, _ := json.Marshal(m)
	manifestFile := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeErr := errors.New("transport exploded")
	var rebuildCalled atomic.Bool
	svc := newForTest(
		conf.BootstrapCfg{Enabled: true, Manifest: manifestFile},
		&stubNode{dir: dir},
		&fakeFetcher{err: fakeErr},
		func(context.Context, kv.RwDB, string, uint64) error {
			rebuildCalled.Store(true)
			return nil
		},
	)
	err := svc.Start(context.Background())
	if err == nil || !errContains(err, "transport exploded") {
		t.Fatalf("expected wrapped fetcher error, got %v", err)
	}
	if rebuildCalled.Load() {
		t.Fatalf("rebuild must not run after fetch failure")
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
