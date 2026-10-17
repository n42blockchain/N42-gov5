// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !nacl && !js && cgo && !gofuzz && !darwin

package crypto

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/holiman/uint256"
)

// refRecover recovers with the pure-Go decred implementation, an
// independent reference for the libsecp256k1 binding. sig is [R || S || V]
// with V in 0..3.
func refRecover(msg, sig []byte) ([]byte, bool) {
	if len(sig) != SignatureLength || sig[64] > 3 {
		return nil, false
	}
	compact := make([]byte, 65)
	compact[0] = 27 + sig[64]
	copy(compact[1:], sig[:64])
	pk, _, err := ecdsa.RecoverCompact(compact, msg)
	if err != nil {
		return nil, false
	}
	return pk.SerializeUncompressed(), true
}

// TestEcrecoverMatchesReference pins the bytes Ecrecover returns (and
// whether it fails) to an independent implementation over many random keys
// and messages, random (mostly invalid) signatures, and edge vectors. Any
// change of the secp256k1 binding (library version, compile flags, a fork)
// must keep this test green.
func TestEcrecoverMatchesReference(t *testing.T) {
	n := 2000
	if testing.Short() {
		n = 200
	}
	rng := rand.New(rand.NewSource(42))
	msg := make([]byte, 32)
	for i := 0; i < n; i++ {
		key, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		rng.Read(msg)
		sig, err := Sign(msg, key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Ecrecover(msg, sig)
		if err != nil {
			t.Fatalf("valid signature %d did not recover: %v", i, err)
		}
		if want := FromECDSAPub(&key.PublicKey); !bytes.Equal(got, want) {
			t.Fatalf("signature %d recovered %x, want the signer %x", i, got, want)
		}
		ref, ok := refRecover(msg, sig)
		if !ok || !bytes.Equal(got, ref) {
			t.Fatalf("signature %d: binding %x, reference %x (ok=%v)", i, got, ref, ok)
		}
		// The other parity recovers a different key or fails, in both.
		flip := append([]byte(nil), sig...)
		flip[64] ^= 1
		compareWithReference(t, msg, flip)
	}
	// Random signatures: almost all r are not an x coordinate on the curve.
	sig := make([]byte, SignatureLength)
	for i := 0; i < n; i++ {
		rng.Read(msg)
		rng.Read(sig[:64])
		sig[64] = byte(rng.Intn(4))
		compareWithReference(t, msg, sig)
	}
}

func compareWithReference(t *testing.T, msg, sig []byte) {
	t.Helper()
	got, err := Ecrecover(msg, sig)
	ref, ok := refRecover(msg, sig)
	if (err == nil) != ok || (ok && !bytes.Equal(got, ref)) {
		t.Fatalf("sig %x msg %x: binding %x err %v, reference %x ok %v", sig, msg, got, err, ref, ok)
	}
}

// TestEcrecoverEdgeVectors pins the binding's verdicts on edge signatures
// and the transaction-level rules applied before it (ValidateSignatureValues).
func TestEcrecoverEdgeVectors(t *testing.T) {
	key, _ := GenerateKey()
	msg := Keccak256([]byte("edge"))
	sig, err := Sign(msg, key)
	if err != nil {
		t.Fatal(err)
	}
	want := FromECDSAPub(&key.PublicKey)
	mut := func(f func(s []byte)) []byte {
		s := append([]byte(nil), sig...)
		f(s)
		return s
	}
	put := func(dst []byte, v *uint256.Int) { v.WriteToSlice(dst) }

	// High s: the same key recovers from (r, n-s) with the other parity.
	// The library accepts it; the homestead rule rejects it.
	var s, ns uint256.Int
	s.SetBytes(sig[32:64])
	ns.Sub(secp256k1N, &s)
	high := mut(func(b []byte) { put(b[32:64], &ns); b[64] ^= 1 })
	if s.Gt(secp256k1halfN) || !ns.Gt(secp256k1halfN) {
		t.Fatal("Sign produced a high-s signature")
	}
	got, err := Ecrecover(msg, high)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("high-s: got %x err %v, want %x", got, err, want)
	}
	var r uint256.Int
	r.SetBytes(sig[:32])
	if ValidateSignatureValues(high[64], &r, &ns, true) {
		t.Fatal("homestead rule accepted a high-s signature")
	}
	if !ValidateSignatureValues(high[64], &r, &ns, false) {
		t.Fatal("frontier rule rejected a high-s signature")
	}
	compareWithReference(t, msg, high)

	var zero uint256.Int
	for name, bad := range map[string][]byte{
		"zero r":     mut(func(b []byte) { clear(b[:32]) }),
		"zero s":     mut(func(b []byte) { clear(b[32:64]) }),
		"r = n":      mut(func(b []byte) { put(b[:32], secp256k1N) }),
		"s = n":      mut(func(b []byte) { put(b[32:64], secp256k1N) }),
		"r all ones": mut(func(b []byte) { copy(b[:32], bytes.Repeat([]byte{0xff}, 32)) }),
		"s all ones": mut(func(b []byte) { copy(b[32:64], bytes.Repeat([]byte{0xff}, 32)) }),
		"recid 4":    mut(func(b []byte) { b[64] = 4 }),
		"recid 27":   mut(func(b []byte) { b[64] = 27 }),
		"all zero":   make([]byte, SignatureLength),
	} {
		if got, err := Ecrecover(msg, bad); err == nil {
			t.Errorf("%s: recovered %x, want an error", name, got)
		}
	}
	if _, err := Ecrecover(msg, sig[:64]); err == nil {
		t.Error("64-byte signature recovered")
	}
	if _, err := Ecrecover(msg[:31], sig); err == nil {
		t.Error("31-byte message recovered")
	}
	if ValidateSignatureValues(0, &zero, &s, true) || ValidateSignatureValues(0, &r, &zero, true) {
		t.Error("zero r or s passed ValidateSignatureValues")
	}
	if ValidateSignatureValues(2, &r, &s, true) {
		t.Error("recid 2 passed ValidateSignatureValues")
	}
}
