//go:build (android || (linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64)) && !blst_disabled

// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package blst

import (
	"testing"

	"github.com/n42blockchain/N42/crypto/bls/common"
)

func sameMessageSet(t *testing.T, n int, msg []byte) ([]common.Signature, []common.PublicKey) {
	t.Helper()
	sigs := make([]common.Signature, n)
	pks := make([]common.PublicKey, n)
	for i := 0; i < n; i++ {
		sk, err := RandKey()
		if err != nil {
			t.Fatal(err)
		}
		sigs[i] = sk.Sign(msg)
		pks[i] = sk.PublicKey()
	}
	return sigs, pks
}

func TestVerifySameMessageBatch(t *testing.T) {
	msg := []byte("N42H2V4 timeout preimage, 56 bytes in production......")
	for _, n := range []int{1, 2, 7, 67} {
		sigs, pks := sameMessageSet(t, n, msg)
		ok, err := VerifySameMessageBatch(sigs, pks, msg)
		if err != nil || !ok {
			t.Fatalf("n=%d: valid batch refused (ok=%v err=%v)", n, ok, err)
		}
		if ok, _ := VerifySameMessageBatch(sigs, pks, []byte("another message")); ok {
			t.Fatalf("n=%d: batch verified over the wrong message", n)
		}
	}

	// One signature over another message among n.
	sigs, pks := sameMessageSet(t, 10, msg)
	other, _ := sameMessageSet(t, 1, []byte("other"))
	sigs[3] = other[0]
	if ok, _ := VerifySameMessageBatch(sigs, pks, msg); ok {
		t.Fatal("batch with one bad signature verified")
	}

	// Two swapped signatures: each is invalid for its key, their sum equals
	// the valid sum, so a plain aggregate check passes. The random
	// coefficients must catch it.
	sigs, pks = sameMessageSet(t, 5, msg)
	aggOK := AggregateSignatures(sigs).Verify(AggregateMultiplePubkeys(pks), msg)
	if !aggOK {
		t.Fatal("aggregate of valid set does not verify")
	}
	sigs[0], sigs[1] = sigs[1], sigs[0]
	if !AggregateSignatures(sigs).Verify(AggregateMultiplePubkeys(pks), msg) {
		t.Fatal("precondition: swapped set should still pass a plain aggregate check")
	}
	if ok, _ := VerifySameMessageBatch(sigs, pks, msg); ok {
		t.Fatal("swapped (cancelling) signatures passed the batch check")
	}

	if _, err := VerifySameMessageBatch(sigs, pks[:2], msg); err == nil {
		t.Fatal("length mismatch accepted")
	}
}
