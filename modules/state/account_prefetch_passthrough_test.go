// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// The AccountPrefetch wrapper's storage/code reads delegate to base
// unconditionally -- only ReadAccountData is intercepted by the seed.
func TestAccountPrefetchPassThroughAndBase(t *testing.T) {
	store := &countingReader{}
	pf := NewAccountPrefetch(store)

	if pf.Base() != StateReader(store) {
		t.Fatal("Base() did not return the wrapped reader")
	}

	var addr types.Address
	addr[0] = 0x9

	if _, err := pf.ReadAccountStorage(addr, &types.Hash{}); err != nil {
		t.Fatalf("ReadAccountStorage: %v", err)
	}
	if _, err := pf.ReadAccountCode(addr, types.Hash{}); err != nil {
		t.Fatalf("ReadAccountCode: %v", err)
	}
	if _, err := pf.ReadAccountCodeSize(addr, types.Hash{}); err != nil {
		t.Fatalf("ReadAccountCodeSize: %v", err)
	}

	// Before Seed, reads fall straight through and count against the base.
	if _, err := pf.ReadAccountData(addr); err != nil {
		t.Fatalf("ReadAccountData: %v", err)
	}
	if store.reads != 1 {
		t.Fatalf("store.reads = %d, want 1 (fallthrough before Seed)", store.reads)
	}
	if pf.Hits() != 0 {
		t.Fatalf("Hits() = %d, want 0 before any seeded hit", pf.Hits())
	}
}
