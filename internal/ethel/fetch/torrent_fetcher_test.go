// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anacrolix/torrent"
)

func TestNormaliseMagnet(t *testing.T) {
	cases := map[string]string{
		"magnet:?xt=urn:btih:abc":                  "magnet:?xt=urn:btih:abc",
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef": "magnet:?xt=urn:btih:deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF": "magnet:?xt=urn:btih:DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF",
		"not-a-magnet-or-hash":                     "not-a-magnet-or-hash",
	}
	for in, want := range cases {
		if got := normaliseMagnet(in); got != want {
			t.Errorf("normaliseMagnet(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestIsHexString(t *testing.T) {
	cases := map[string]bool{
		"":         true,
		"0123abcd": true,
		"FFFF":     true,
		"AbCdEf":   true,
		"xyz":      false,
		"00 11":    false,
		"hello":    false,
	}
	for in, want := range cases {
		if got := isHexString(in); got != want {
			t.Errorf("isHexString(%q): got %v, want %v", in, got, want)
		}
	}
}

// TestTorrentFetcher_KindsContract pins the supported kinds. Both BT
// v1 (`bt`) and BT v2 (`bt-v2`) are accepted: the upstream
// anacrolix/torrent client pinned in go.mod handles both wire
// versions from a single AddMagnet code path, so the fetcher simply
// advertises both to MultiSourceFetcher.
func TestTorrentFetcher_KindsContract(t *testing.T) {
	f := newTorrentFetcher(nil, TorrentFetcherOptions{})
	kinds := f.Kinds()
	if len(kinds) != 2 {
		t.Fatalf("Kinds: got %v, want 2 entries", kinds)
	}
	have := map[SourceKind]bool{}
	for _, k := range kinds {
		have[k] = true
	}
	if !have[SourceBT] || !have[SourceBTV2] {
		t.Fatalf("Kinds: got %v, want {bt, bt-v2}", kinds)
	}
}

// TestTorrentFetcher_AcceptsBTV2 verifies the dispatch guard accepts
// the `bt-v2` source kind. Before the BT v2 upgrade this returned the
// "unsupported source kind" error; the regression test pins the fix.
func TestTorrentFetcher_AcceptsBTV2(t *testing.T) {
	stub := stubTorrentClient{addErr: errors.New("stop here before AddMagnet completes")}
	f := newTorrentFetcher(stub, TorrentFetcherOptions{MaxAttempts: 1})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources: []Source{{
			Kind: SourceBTV2,
			URI:  "magnet:?xt=urn:btmh:1220abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab",
		}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected stub-injected error, got nil")
	}
	// Any error is acceptable as long as it is NOT the "unsupported
	// source kind" rejection — we only want to confirm dispatch reached
	// the magnet add path.
	if strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatalf("BTV2 dispatch still rejected: %v", err)
	}
}

// TestTorrentFetcher_RejectsWrongKind verifies the dispatch guard.
// MultiSourceFetcher already filters by kind, but the per-fetcher check
// catches misuse from direct callers.
func TestTorrentFetcher_RejectsWrongKind(t *testing.T) {
	f := newTorrentFetcher(stubTorrentClient{}, TorrentFetcherOptions{})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceHTTPS, URI: "https://x"}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatalf("expected unsupported source kind, got %v", err)
	}
}

// TestTorrentFetcher_NilClient verifies the explicit nil-client guard.
// A misconfigured Node could pass nil if --torrent.enabled=false; the
// fetcher must fail loudly rather than panic.
func TestTorrentFetcher_NilClient(t *testing.T) {
	f := newTorrentFetcher(nil, TorrentFetcherOptions{})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceBT, URI: "magnet:?xt=urn:btih:abc"}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "no torrent client configured") {
		t.Fatalf("expected nil-client error, got %v", err)
	}
}

// TestTorrentFetcher_AddMagnetError covers the error path when
// AddMagnet itself fails (malformed magnet, swarm unreachable, etc.).
// The fetcher should propagate the error wrapped, after the configured
// retry budget.
func TestTorrentFetcher_AddMagnetError(t *testing.T) {
	want := errors.New("simulated add failure")
	stub := stubTorrentClient{addErr: want}
	f := newTorrentFetcher(stub, TorrentFetcherOptions{MaxAttempts: 2})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceBT, URI: "magnet:?xt=urn:btih:abc"}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "simulated add failure") {
		t.Fatalf("expected wrapped add failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "2 attempts") {
		t.Fatalf("expected retry-budget message, got %v", err)
	}
}

// stubTorrentClient is a minimal torrentClient for tests that exercise
// the validation / nil / error paths. We do not test the happy path
// here because *torrent.Torrent has no in-memory constructor — that
// requires a real anacrolix client and a real swarm. The HTTPFetcher
// suite already covers the streaming/checksum logic that the torrent
// download path reuses.
type stubTorrentClient struct {
	addErr error
}

func (s stubTorrentClient) AddMagnet(_ string) (*torrent.Torrent, error) {
	if s.addErr != nil {
		return nil, s.addErr
	}
	// Returning (nil, nil) would cause the fetcher to deref nil; the
	// success path simply is not testable with a stub. Return an
	// explicit error so any test that gets here fails loudly.
	return nil, errors.New("stub: AddMagnet success path requires a real anacrolix client")
}

func (s stubTorrentClient) RemoveTorrent(_ [20]byte) {}
