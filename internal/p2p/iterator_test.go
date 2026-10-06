// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// fakeIterator yields a fixed number of nodes (nil is fine: the check
// functions below never dereference it) before exhausting.
type fakeIterator struct {
	remaining int
	closed    bool
}

func (f *fakeIterator) Next() bool {
	if f.remaining <= 0 {
		return false
	}
	f.remaining--
	return true
}
func (f *fakeIterator) Node() *enode.Node { return nil }
func (f *fakeIterator) Close()            { f.closed = true }

func TestFilterIterPassesOnlyAcceptedNodes(t *testing.T) {
	calls := 0
	it := filterNodes(context.Background(), &fakeIterator{remaining: 3}, func(*enode.Node) bool {
		calls++
		// Accept every other node.
		return calls%2 == 0
	})

	if !it.Next() {
		t.Fatal("expected the second call to be accepted")
	}
	if it.Next() {
		t.Fatal("expected no more accepted nodes from a 3-node source")
	}
}

func TestFilterIterRejectsAll(t *testing.T) {
	it := filterNodes(context.Background(), &fakeIterator{remaining: 5}, func(*enode.Node) bool {
		return false
	})
	if it.Next() {
		t.Fatal("expected Next to return false when the filter rejects everything")
	}
}

func TestFilterIterStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	it := filterNodes(ctx, &fakeIterator{remaining: 5}, func(*enode.Node) bool {
		return true
	})
	if it.Next() {
		t.Fatal("expected Next to return false once the context is cancelled")
	}
}

func TestFilterIterExhaustsSource(t *testing.T) {
	src := &fakeIterator{remaining: 0}
	it := filterNodes(context.Background(), src, func(*enode.Node) bool { return true })
	if it.Next() {
		t.Fatal("expected Next to return false on an already-exhausted source")
	}
	it.Close()
	if !src.closed {
		t.Fatal("Close should propagate to the wrapped iterator")
	}
}
