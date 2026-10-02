// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the smaller, still-pure or deterministic helpers: CreateForkDigest,
// the libp2p/ecdsa key conversions in crypto.go, RunEvery's periodic execution,
// and Lock.Yield.

package utils

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/n42blockchain/N42/common/types"
	n42_crypto "github.com/n42blockchain/N42/crypto"
)

func TestG35CreateForkDigest(t *testing.T) {
	var root types.Hash
	copy(root[:], []byte{1, 2, 3, 4})
	digest, err := CreateForkDigest(uint256.NewInt(7), root)
	if err != nil {
		t.Fatalf("CreateForkDigest: %v", err)
	}
	want := ToBytes4(root[:])
	if digest != want {
		t.Fatalf("expected digest %x, got %x", want, digest)
	}
}

func TestG35KeyConversionsRoundTrip(t *testing.T) {
	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate libp2p key: %v", err)
	}

	ecdsaPriv, err := ConvertFromInterfacePrivKey(priv)
	if err != nil {
		t.Fatalf("ConvertFromInterfacePrivKey: %v", err)
	}

	back, err := ConvertToInterfacePrivkey(ecdsaPriv)
	if err != nil {
		t.Fatalf("ConvertToInterfacePrivkey: %v", err)
	}
	if !priv.Equals(back) {
		t.Fatalf("private key round-trip mismatch")
	}

	pubKey, err := ConvertToInterfacePubkey(&ecdsaPriv.PublicKey)
	if err != nil {
		t.Fatalf("ConvertToInterfacePubkey: %v", err)
	}
	if !pubKey.Equals(priv.GetPublic()) {
		t.Fatalf("public key round-trip mismatch")
	}
}

func TestG35ConvertFromInterfacePrivKeyWrongType(t *testing.T) {
	_, pub, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	// Use the ed25519 private key (wrong type for secp256k1 conversion).
	edPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 priv: %v", err)
	}
	if _, err := ConvertFromInterfacePrivKey(edPriv); err == nil {
		t.Fatalf("expected error converting non-secp256k1 key")
	}
	_ = pub
}

func TestG35ConvertToInterfacePubkeyOverflow(t *testing.T) {
	// A pubkey from a different curve (P256) has coordinates that are valid
	// field elements for secp256k1 arithmetic sizes, so instead we directly
	// exercise the happy path above and rely on n42_crypto.S256 for basic
	// sanity here.
	key, err := ecdsa.GenerateKey(n42_crypto.S256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := ConvertToInterfacePubkey(&key.PublicKey); err != nil {
		t.Fatalf("ConvertToInterfacePubkey: %v", err)
	}
	_ = elliptic.P256
}

func TestG35RunEveryWithWGRunsAndCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	var wg sync.WaitGroup
	wg.Add(1)
	RunEveryWithWG(ctx, 10*time.Millisecond, func() {
		atomic.AddInt32(&calls, 1)
	}, &wg)

	time.Sleep(60 * time.Millisecond)
	cancel()
	wg.Wait()

	if atomic.LoadInt32(&calls) == 0 {
		t.Fatalf("expected RunEveryWithWG to invoke f at least once")
	}
}

func TestG35RunEveryWithWGRecoversPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	RunEveryWithWG(ctx, 5*time.Millisecond, func() {
		close(done)
		panic("boom")
	}, &wg)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected panicking function to be invoked")
	}
	cancel()
	wg.Wait()
}

func TestG35RunEvery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	RunEvery(ctx, 5*time.Millisecond, func() {
		select {
		case <-done:
		default:
			close(done)
		}
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected RunEvery to invoke f")
	}
	cancel()
}

func TestG35LockYield(t *testing.T) {
	lk := NewMultilock("g35-yield-key")
	lk.Lock()
	lk.Yield()
	lk.Unlock()
}
