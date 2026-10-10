// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame

import (
	"bytes"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

// TestEncodeWithSigner: Encode and EncodeWith over the same key produce the
// same bytes; a signer that returns a malformed signature is refused;
// GatewayOf reads the signer address from the header.
func TestEncodeWithSigner(t *testing.T) {
	key, _ := crypto.GenerateKey()
	txs := [][]byte{{1, 2, 3}, {4, 5}}
	a, ha, err := Encode(key, 3, TxTypeEthereum, txs)
	if err != nil {
		t.Fatal(err)
	}
	s := KeySigner(key)
	b, hb, err := EncodeWith(s, 3, TxTypeEthereum, txs)
	if err != nil || ha != hb || !bytes.Equal(a, b) {
		t.Fatalf("EncodeWith differs from Encode: %v", err)
	}
	if gw, ok := GatewayOf(a); !ok || gw != s.Address {
		t.Fatal("GatewayOf")
	}
	if _, ok := GatewayOf(a[:HeaderSize-1]); ok {
		t.Fatal("GatewayOf on a short buffer")
	}
	bad := &Signer{Address: s.Address, SignHash: func([]byte) ([]byte, error) { return make([]byte, 64), nil }}
	if _, _, err := EncodeWith(bad, 1, TxTypeEthereum, txs); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("short signature: %v", err)
	}
}

// TestDeriveLocalKey: deterministic per secret, distinct across secrets,
// and the frames it signs verify.
func TestDeriveLocalKey(t *testing.T) {
	k1, err := DeriveLocalKey([]byte("validator one"))
	if err != nil {
		t.Fatal(err)
	}
	k1b, _ := DeriveLocalKey([]byte("validator one"))
	k2, _ := DeriveLocalKey([]byte("validator two"))
	a1, a1b, a2 := KeySigner(k1).Address, KeySigner(k1b).Address, KeySigner(k2).Address
	if a1 != a1b || a1 == a2 {
		t.Fatalf("derivation: %s %s %s", a1, a1b, a2)
	}
	if _, err := DeriveLocalKey(nil); err == nil {
		t.Fatal("empty secret accepted")
	}
	raw, _, err := EncodeWith(KeySigner(k1), 1, TxTypeEthereum, [][]byte{{9}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Verify(); err != nil || f.Gateway != a1 {
		t.Fatalf("verify: %v", err)
	}
}
