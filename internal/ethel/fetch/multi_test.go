// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

// fakeFetcher records every Fetch call and returns whatever the test
// function provides. It supports a single SourceKind so MultiSourceFetcher
// dispatch can be exercised.
type fakeFetcher struct {
	kind  SourceKind
	calls atomic.Int32
	fn    func(asset Asset) error
}

func (f *fakeFetcher) Kinds() []SourceKind { return []SourceKind{f.kind} }
func (f *fakeFetcher) Fetch(_ context.Context, asset Asset, _ string, _ ProgressFunc) error {
	f.calls.Add(1)
	if f.fn == nil {
		return nil
	}
	return f.fn(asset)
}

func TestMultiSourceFetcher_DispatchByKind(t *testing.T) {
	httpF := &fakeFetcher{kind: SourceHTTPS}
	btF := &fakeFetcher{kind: SourceBT}
	multi := NewMultiSourceFetcher(httpF, btF)

	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources: []Source{
			{Kind: SourceHTTPS, URI: "https://a", Priority: 100},
		},
	}
	if err := multi.Fetch(context.Background(), asset, t.TempDir(), nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if httpF.calls.Load() != 1 {
		t.Fatalf("https calls: got %d, want 1", httpF.calls.Load())
	}
	if btF.calls.Load() != 0 {
		t.Fatalf("bt calls: got %d, want 0", btF.calls.Load())
	}
}

func TestMultiSourceFetcher_PriorityOrder(t *testing.T) {
	var order []string
	httpF := &fakeFetcher{kind: SourceHTTPS, fn: func(a Asset) error {
		order = append(order, a.Sources[0].URI)
		return errors.New("simulated http failure")
	}}
	btF := &fakeFetcher{kind: SourceBT, fn: func(a Asset) error {
		order = append(order, a.Sources[0].URI)
		return nil
	}}
	multi := NewMultiSourceFetcher(httpF, btF)

	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources: []Source{
			{Kind: SourceBT, URI: "magnet:low", Priority: 10},
			{Kind: SourceHTTPS, URI: "https://hi", Priority: 100},
			{Kind: SourceHTTPS, URI: "https://mid", Priority: 50},
		},
	}
	if err := multi.Fetch(context.Background(), asset, t.TempDir(), nil); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []string{"https://hi", "https://mid", "magnet:low"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order: got %v, want %v", order, want)
	}
}

func TestMultiSourceFetcher_NoSources(t *testing.T) {
	multi := NewMultiSourceFetcher(&fakeFetcher{kind: SourceHTTPS})
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources:   []Source{{Kind: SourceBT, URI: "magnet:1"}},
	}
	err := multi.Fetch(context.Background(), asset, t.TempDir(), nil)
	if !errors.Is(err, ErrNoSourcesAvailable) {
		t.Fatalf("expected ErrNoSourcesAvailable, got %v", err)
	}
}

func TestMultiSourceFetcher_ContextCancellationStopsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	httpF := &fakeFetcher{kind: SourceHTTPS, fn: func(a Asset) error {
		cancel()
		return context.Canceled
	}}
	btCalled := false
	btF := &fakeFetcher{kind: SourceBT, fn: func(a Asset) error {
		btCalled = true
		return nil
	}}
	multi := NewMultiSourceFetcher(httpF, btF)
	asset := Asset{
		Name:      "x",
		SizeBytes: 1,
		Sources: []Source{
			{Kind: SourceHTTPS, URI: "https://hi", Priority: 100},
			{Kind: SourceBT, URI: "magnet:lo", Priority: 50},
		},
	}
	err := multi.Fetch(ctx, asset, t.TempDir(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if btCalled {
		t.Fatalf("bt fallback should not run after context cancellation")
	}
}

func TestMultiSourceFetcher_KindsUnion(t *testing.T) {
	multi := NewMultiSourceFetcher(
		&fakeFetcher{kind: SourceHTTPS},
		&fakeFetcher{kind: SourceBT},
	)
	got := multi.Kinds()
	if len(got) != 2 {
		t.Fatalf("Kinds: %v", got)
	}
	// sorted
	if got[0] != SourceBT || got[1] != SourceHTTPS {
		t.Fatalf("Kinds order: %v", got)
	}
}
