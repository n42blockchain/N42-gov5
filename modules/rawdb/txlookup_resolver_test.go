// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

type stubTxLookupResolver struct {
	known map[types.Hash]uint64
}

type failingTxLookupResolver struct{ stubTxLookupResolver }

func (*failingTxLookupResolver) LookupTxWithError(types.Hash) (uint64, bool, error) {
	return 0, false, errors.New("segment read failed")
}

func TestOwnedResolverExclusiveAndReadFailure(t *testing.T) {
	SetTxLookupResolver(nil)
	defer SetTxLookupResolver(nil)
	first := &failingTxLookupResolver{}
	removeFirst, ok := TryInstallTxLookupResolver(first)
	if !ok {
		t.Fatal("first resolver did not acquire process slot")
	}
	second := &stubTxLookupResolver{}
	if _, ok := TryInstallTxLookupResolver(second); ok {
		t.Fatal("second resolver displaced first")
	}
	_, tx := memdb.NewTestTx(t)
	if _, err := ReadTxLookupEntry(tx, types.HexToHash("0x05")); err == nil || err.Error() != "segment read failed" {
		t.Fatalf("physical read failure was hidden as a lookup miss: %v", err)
	}
	SetTxLookupResolver(second) // A foreign replacement must survive old Stop.
	if removeFirst() {
		t.Fatal("old owner removed a different resolver")
	}
}

func (s *stubTxLookupResolver) LookupTx(txHash types.Hash) (uint64, bool) {
	n, ok := s.known[txHash]
	return n, ok
}

// TestResolveTxLookupNoResolver verifies ReadTxLookupEntry falls through to
// the table when no resolver is installed.
func TestResolveTxLookupNoResolver(t *testing.T) {
	SetTxLookupResolver(nil)
	_, tx := memdb.NewTestTx(t)

	h := types.HexToHash("0x01")
	n, err := ReadTxLookupEntry(tx, h)
	if err != nil {
		t.Fatalf("ReadTxLookupEntry: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil for unknown hash with no resolver, got %v", n)
	}
}

// TestResolveTxLookupWithResolver verifies an installed resolver is
// consulted before the MDBX table, and that removing it (SetTxLookupResolver
// (nil)) restores table-only behavior.
func TestResolveTxLookupWithResolver(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	known := types.HexToHash("0x02")
	resolver := &stubTxLookupResolver{known: map[types.Hash]uint64{known: 77}}
	SetTxLookupResolver(resolver)
	t.Cleanup(func() { SetTxLookupResolver(nil) })

	n, err := ReadTxLookupEntry(tx, known)
	if err != nil {
		t.Fatalf("ReadTxLookupEntry: %v", err)
	}
	if n == nil || *n != 77 {
		t.Fatalf("ReadTxLookupEntry via resolver = %v, want 77", n)
	}

	// A hash the resolver doesn't cover falls through to the (empty) table.
	unrelated := types.HexToHash("0x03")
	n, err = ReadTxLookupEntry(tx, unrelated)
	if err != nil {
		t.Fatalf("ReadTxLookupEntry(unrelated): %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil for hash not covered by resolver or table, got %v", n)
	}

	// Removing the resolver restores the no-resolver path.
	SetTxLookupResolver(nil)
	n, err = ReadTxLookupEntry(tx, known)
	if err != nil {
		t.Fatalf("ReadTxLookupEntry after removing resolver: %v", err)
	}
	if n != nil {
		t.Fatalf("expected nil after resolver removed, got %v", n)
	}
}
