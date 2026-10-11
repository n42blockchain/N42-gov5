// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !nacl && !js && cgo && !gofuzz && !darwin

package crypto

import (
	"testing"

	"github.com/erigontech/secp256k1"
)

// BenchmarkEcrecover measures one secp256k1 public key recovery through
// the cgo binding (P8a: the per-signature cost a node pays per sender).
func BenchmarkEcrecover(b *testing.B) {
	key, _ := GenerateKey()
	msg := Keccak256([]byte("p8a"))
	sig, err := Sign(msg, key)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Ecrecover(msg, sig); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEcrecoverParallel is BenchmarkEcrecover on every P.
func BenchmarkEcrecoverParallel(b *testing.B) {
	key, _ := GenerateKey()
	msg := Keccak256([]byte("p8a"))
	sig, err := Sign(msg, key)
	if err != nil {
		b.Fatal(err)
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := Ecrecover(msg, sig); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkCgoCallFloor bounds what batching recoveries per cgo call could
// save: the same cgo call with an all-zero signature, which libsecp256k1
// rejects in its first few instructions, so the cost is the cgo transition.
func BenchmarkCgoCallFloor(b *testing.B) {
	msg := Keccak256([]byte("p8a"))
	sig := make([]byte, 65)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := secp256k1.RecoverPubkey(msg, sig); err == nil {
			b.Fatal("zero signature recovered")
		}
	}
}
