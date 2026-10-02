// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestPlainStateReaderCodeSourceAndCodeSize covers SetCodeSource, the
// codes.cdat fast path (including the keccak-mismatch fallback to MDBX),
// and ReadAccountCodeSize.
func TestPlainStateReaderCodeSourceAndCodeSize(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	r := NewPlainStateReader(tx)

	var addr types.Address
	addr[0] = 0x91
	code := []byte{0x60, 0x01, 0x60, 0x02}
	codeHash := crypto.Keccak256Hash(code)

	// No code source, no MDBX row: miss.
	got, err := r.ReadAccountCode(addr, codeHash)
	if err != nil || got != nil {
		t.Fatalf("ReadAccountCode(miss) = %v, %v, want nil, nil", got, err)
	}

	// Code source returns bytes whose keccak does NOT match codeHash:
	// falls through to MDBX, which also misses.
	r.SetCodeSource(stubCodeSource{code: []byte("wrong")})
	got, err = r.ReadAccountCode(addr, codeHash)
	if err != nil || got != nil {
		t.Fatalf("ReadAccountCode(mismatched source) = %v, %v, want nil, nil", got, err)
	}

	// Code source returns the right bytes: served without touching MDBX.
	r.SetCodeSource(stubCodeSource{code: code})
	got, err = r.ReadAccountCode(addr, codeHash)
	if err != nil {
		t.Fatalf("ReadAccountCode: %v", err)
	}
	if string(got) != string(code) {
		t.Fatalf("ReadAccountCode = %x, want %x", got, code)
	}

	size, err := r.ReadAccountCodeSize(addr, codeHash)
	if err != nil || size != len(code) {
		t.Fatalf("ReadAccountCodeSize = %d, %v, want %d, nil", size, err, len(code))
	}

	// Disable the source; MDBX still has nothing, but a direct Code row
	// makes ReadAccountCode fall through correctly.
	r.SetCodeSource(nil)
	if err := tx.Put(modules.Code, codeHash[:], code); err != nil {
		t.Fatalf("seed Code row: %v", err)
	}
	got, err = r.ReadAccountCode(addr, codeHash)
	if err != nil || string(got) != string(code) {
		t.Fatalf("ReadAccountCode(mdbx fallback) = %x, %v, want %x, nil", got, err, code)
	}

	// Empty code hash short-circuits.
	if c, err := r.ReadAccountCode(addr, types.BytesToHash(emptyCodeHash)); err != nil || c != nil {
		t.Fatalf("ReadAccountCode(emptyCodeHash) = %v, %v, want nil, nil", c, err)
	}
}
