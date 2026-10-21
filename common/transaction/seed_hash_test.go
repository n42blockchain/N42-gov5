// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func TestSeedHashFromEncoding(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	chainID := big.NewInt(1142)
	seeded := 0
	for _, src := range mixedTxs(t, rng, 200, chainID) {
		if src == nil {
			continue
		}
		enc, err := EncodeEthereumTransaction(src)
		if err != nil {
			t.Fatal(err)
		}
		dec := NewEthBatchDecoder(1)
		tx, err := dec.DecodeAliased(enc)
		if err != nil {
			t.Fatal(err)
		}
		want := tx.inner.hash()
		h := crypto.Keccak256Hash(enc)
		// A different encoding is never accepted.
		other := append(append([]byte(nil), enc...), 0)
		if tx.SeedHashFromEncoding(other, h) {
			t.Fatal("seeded under a different encoding")
		}
		ok := tx.SeedHashFromEncoding(enc, h)
		if ok != hashFromEncoding(tx.Type()) {
			t.Fatalf("type %d: seeded=%v", tx.Type(), ok)
		}
		if ok {
			seeded++
			if h != want {
				t.Fatalf("type %d: keccak(enc) %x is not the tx hash %x", tx.Type(), h, want)
			}
			// A copy of the bytes is accepted too, but the hash is set now.
			if tx.SeedHashFromEncoding(append([]byte(nil), enc...), h) {
				t.Fatal("seeded twice")
			}
		}
		if tx.Hash() != want {
			t.Fatalf("type %d: hash %x want %x", tx.Type(), tx.Hash(), want)
		}
	}
	if seeded == 0 {
		t.Fatal("nothing seeded")
	}
}
