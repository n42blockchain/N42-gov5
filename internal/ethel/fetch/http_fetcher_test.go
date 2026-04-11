// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func makePayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

func sha256Of(b []byte) [32]byte {
	return sha256.Sum256(b)
}

func TestHTTPFetcher_HappyPath(t *testing.T) {
	payload := makePayload(4096)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(200)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{Client: srv.Client()})

	asset := Asset{
		Name:      "leaves.bin",
		SizeBytes: uint64(len(payload)),
		SHA256:    sha256Of(payload),
		Sources:   []Source{{Kind: SourceHTTPS, URI: srv.URL}},
	}
	dir := t.TempDir()
	var lastProgress Progress
	err := f.Fetch(context.Background(), asset, dir, func(p Progress) { lastProgress = p })
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "leaves.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload mismatch")
	}
	if lastProgress.Bytes != uint64(len(payload)) {
		t.Fatalf("final progress: %d, want %d", lastProgress.Bytes, len(payload))
	}
}

func TestHTTPFetcher_RejectsPlaintextByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{})
	asset := Asset{
		Name:      "x",
		SizeBytes: 4,
		Sources:   []Source{{Kind: SourceHTTP, URI: srv.URL}},
	}
	err := f.Fetch(context.Background(), asset, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatalf("expected unsupported source kind, got %v", err)
	}
}

func TestHTTPFetcher_AllowsPlaintextWhenOpted(t *testing.T) {
	payload := []byte("hello world")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{AllowPlaintext: true, Client: srv.Client()})
	asset := Asset{
		Name:      "x",
		SizeBytes: uint64(len(payload)),
		SHA256:    sha256Of(payload),
		Sources:   []Source{{Kind: SourceHTTP, URI: srv.URL}},
	}
	if err := f.Fetch(context.Background(), asset, t.TempDir(), nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestHTTPFetcher_ChecksumMismatch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tampered bytes"))
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{Client: srv.Client()})
	asset := Asset{
		Name:      "x",
		SizeBytes: uint64(len("tampered bytes")),
		SHA256:    sha256Of([]byte("expected bytes")),
		Sources:   []Source{{Kind: SourceHTTPS, URI: srv.URL}},
	}
	dir := t.TempDir()
	err := f.Fetch(context.Background(), asset, dir, nil)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	// Partial file must be cleaned up so the next attempt does not see it.
	if _, err := os.Stat(filepath.Join(dir, "x.part")); err == nil {
		t.Fatalf(".part file should be removed on mismatch")
	}
}

func TestHTTPFetcher_ResumeViaRange(t *testing.T) {
	payload := makePayload(2048)
	var serverHits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		var start int64
		if rng := r.Header.Get("Range"); rng != "" {
			// Expect "bytes=NNN-"
			var n int64
			fmt.Sscanf(rng, "bytes=%d-", &n)
			start = n
			w.Header().Set("Content-Length", fmt.Sprintf("%d", int64(len(payload))-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			w.WriteHeader(200)
		}
		_, _ = w.Write(payload[start:])
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{Client: srv.Client()})
	dir := t.TempDir()
	finalPath := filepath.Join(dir, "p.bin")
	partPath := finalPath + ".part"
	// Pre-seed a half-completed .part file.
	half := len(payload) / 2
	if err := os.WriteFile(partPath, payload[:half], 0o644); err != nil {
		t.Fatal(err)
	}

	asset := Asset{
		Name:      "p.bin",
		SizeBytes: uint64(len(payload)),
		SHA256:    sha256Of(payload),
		Sources:   []Source{{Kind: SourceHTTPS, URI: srv.URL}},
	}
	if err := f.Fetch(context.Background(), asset, dir, nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("resumed payload mismatch")
	}
	if serverHits.Load() != 1 {
		t.Fatalf("expected 1 server hit, got %d", serverHits.Load())
	}
}

func TestHTTPFetcher_SkipExistingComplete(t *testing.T) {
	payload := []byte("done already")
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	f := NewHTTPFetcher(HTTPFetcherOptions{Client: srv.Client()})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "done.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	asset := Asset{
		Name:      "done.bin",
		SizeBytes: uint64(len(payload)),
		SHA256:    sha256Of(payload),
		Sources:   []Source{{Kind: SourceHTTPS, URI: srv.URL}},
	}
	if err := f.Fetch(context.Background(), asset, dir, nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server should not be hit when local file already matches; hits=%d", hits.Load())
	}
}

func TestHTTPFetcher_SchemeOf(t *testing.T) {
	cases := map[string]string{
		"https://x":   "https",
		"http://x":    "http",
		"magnet:?xt=": "",
		"":            "",
	}
	for in, want := range cases {
		if got := schemeOf(in); got != want {
			t.Errorf("schemeOf(%q): got %q want %q", in, got, want)
		}
	}
}
