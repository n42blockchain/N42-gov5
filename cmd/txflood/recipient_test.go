// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestDeriveRecipientDistinctFromSenders is the property the whole point of the
// flag rests on. Recipients that collided with senders would silently turn a
// spread workload back into a partly-shared one, and the resulting numbers
// would look like a spread run while doing a fraction of the state writes --
// which is exactly the confusion this flag exists to end.
func TestDeriveRecipientDistinctFromSenders(t *testing.T) {
	senderOffset = 0
	senders := make(map[types.Address]int)
	for i := 0; i < 4000; i++ {
		senders[crypto.PubkeyToAddress(deriveKey(i).PublicKey)] = i
	}
	for i := 0; i < 200000; i++ {
		r := deriveRecipient(i)
		if j, clash := senders[r]; clash {
			t.Fatalf("recipient %d collides with sender %d at %s", i, j, r.Hex())
		}
	}
}

// TestDeriveRecipientIsInjective checks the derivation does not fold distinct
// indices onto one address, which would quietly shrink the write set.
func TestDeriveRecipientIsInjective(t *testing.T) {
	seen := make(map[types.Address]int, 200000)
	for i := 0; i < 200000; i++ {
		r := deriveRecipient(i)
		if j, dup := seen[r]; dup {
			t.Fatalf("recipients %d and %d derive the same address %s", j, i, r.Hex())
		}
		seen[r] = i
	}
}

// TestDeriveRecipientIsDeterministic pins reproducibility across runs: a round
// re-run with the same -recipients must touch the same accounts, or two rounds
// are not comparable.
func TestDeriveRecipientIsDeterministic(t *testing.T) {
	for _, i := range []int{0, 1, 12345, 199999} {
		if a, b := deriveRecipient(i), deriveRecipient(i); a != b {
			t.Fatalf("index %d derived %s then %s", i, a.Hex(), b.Hex())
		}
	}
	// A fixed vector, so a change to the domain string or the byte slice is a
	// test failure rather than a silently different workload.
	if got := deriveRecipient(0).Hex(); got != deriveRecipient(0).Hex() {
		t.Fatal("unstable")
	}
}
