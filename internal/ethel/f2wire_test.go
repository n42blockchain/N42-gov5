// Copyright 2022-2026 The N42 Authors

// f2wire_test.go — cover the F2 ledger-reader process-wide hooks: nil
// (unconfigured) error paths for F2LedgerBody / F2BlockHashes /
// F2TxLocByHash, and the Set*/accessor round trips. Globals are saved and
// restored around each test so this file stays order-independent of other
// tests in the package that might configure the same hooks.

package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
	"github.com/n42blockchain/N42/internal/history"
)

func withF2Globals(t *testing.T, fn func()) {
	t.Helper()
	savedReader := defaultF2Reader
	savedHashIdx := defaultF2HashIdx
	savedHashes := defaultF2Hashes
	t.Cleanup(func() {
		defaultF2Reader = savedReader
		defaultF2HashIdx = savedHashIdx
		defaultF2Hashes = savedHashes
	})
	fn()
}

func TestF2LedgerBody_Unconfigured(t *testing.T) {
	withF2Globals(t, func() {
		SetF2Reader(nil)
		if r := F2Reader(); r != nil {
			t.Fatalf("expected nil F2Reader, got %v", r)
		}
		_, err := F2LedgerBody(1)
		if err == nil {
			t.Fatal("expected error when no F2 reader is configured")
		}
	})
}

func TestF2Reader_SetAndGet(t *testing.T) {
	withF2Globals(t, func() {
		r := &bodyf2.Reader{}
		SetF2Reader(r)
		if got := F2Reader(); got != r {
			t.Fatalf("F2Reader() = %p, want %p", got, r)
		}
	})
}

func TestF2HashIndex_Unconfigured(t *testing.T) {
	withF2Globals(t, func() {
		SetF2HashIndex(nil)
		if idx := F2HashIndex(); idx != nil {
			t.Fatalf("expected nil F2HashIndex, got %v", idx)
		}
		block, index, ok := F2TxLocByHash(types.Hash{})
		if ok || block != 0 || index != 0 {
			t.Fatalf("expected ok=false,0,0 with no index configured, got %d %d %v", block, index, ok)
		}
	})
}

func TestF2HashIndex_SetAndGet(t *testing.T) {
	withF2Globals(t, func() {
		idx := &history.MPHFReader{}
		SetF2HashIndex(idx)
		if got := F2HashIndex(); got != idx {
			t.Fatalf("F2HashIndex() = %p, want %p", got, idx)
		}
	})
}

func TestF2Hashes_Unconfigured(t *testing.T) {
	withF2Globals(t, func() {
		SetF2Hashes(nil)
		if h := F2Hashes(); h != nil {
			t.Fatalf("expected nil F2Hashes, got %v", h)
		}
		_, err := F2BlockHashes(1)
		if err == nil {
			t.Fatal("expected error when no F2 hash sidecar is configured")
		}
	})
}

func TestF2Hashes_SetAndGet(t *testing.T) {
	withF2Globals(t, func() {
		h := &bodyf2.HashReader{}
		SetF2Hashes(h)
		if got := F2Hashes(); got != h {
			t.Fatalf("F2Hashes() = %p, want %p", got, h)
		}
	})
}
