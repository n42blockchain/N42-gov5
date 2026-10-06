// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package csidh

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type csidhVector struct {
	Id      int
	Pk1     string
	Pr1     string
	Pk2     string
	Ss      string
	Status  string
	Comment string
}

type csidhVectorFile struct {
	Vectors []csidhVector
}

func loadVectors(t *testing.T) []csidhVector {
	t.Helper()
	data, err := os.ReadFile("testdata/csidh_testvectors.json")
	if err != nil {
		t.Fatalf("failed to read test vectors: %v", err)
	}
	var f csidhVectorFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("failed to parse test vectors: %v", err)
	}
	if len(f.Vectors) == 0 {
		t.Fatal("no test vectors found")
	}
	return f.Vectors
}

// TestGeneratePublicKeyAgainstVectors checks that GeneratePublicKey(Pr1) from
// the base curve reproduces Pk2 for a handful of known-answer vectors. The
// group action's internal random point sampling does not affect the
// deterministic mathematical result.
func TestGeneratePublicKeyAgainstVectors(t *testing.T) {
	vectors := loadVectors(t)
	// Only exercise a handful: full cSIDH-512 group actions, while fast
	// (~30ms each), add up across hundreds of vectors.
	limit := 3
	if len(vectors) < limit {
		limit = len(vectors)
	}
	for _, v := range vectors[:limit] {
		v := v
		t.Run(hex.EncodeToString([]byte{byte(v.Id)}), func(t *testing.T) {
			prBytes, err := hex.DecodeString(v.Pr1)
			if err != nil {
				t.Fatalf("decode Pr1: %v", err)
			}
			var prv PrivateKey
			if !prv.Import(prBytes) {
				t.Fatal("PrivateKey.Import failed")
			}

			var pub PublicKey
			if err := GeneratePublicKey(&pub, &prv, rand.Reader); err != nil {
				t.Fatalf("GeneratePublicKey: %v", err)
			}

			got := make([]byte, PublicKeySize)
			if !pub.Export(got) {
				t.Fatal("PublicKey.Export failed")
			}

			// Pk1 is this vector's own public key, i.e. GeneratePublicKey(Pr1)
			// acting on the base curve; Pk2 is the peer's public key used by
			// TestDeriveSecretAgainstVectors below.
			want, err := hex.DecodeString(v.Pk1)
			if err != nil {
				t.Fatalf("decode Pk1: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("GeneratePublicKey mismatch:\n got  %x\n want %x", got, want)
			}
		})
	}
}

// TestDeriveSecretAgainstVectors checks the full shared-secret agreement:
// DeriveSecret(Pk1, Pr1) must equal Ss for valid vectors.
func TestDeriveSecretAgainstVectors(t *testing.T) {
	vectors := loadVectors(t)
	limit := 3
	count := 0
	for _, v := range vectors {
		if v.Status != "valid" {
			continue
		}
		if count >= limit {
			break
		}
		count++
		v := v
		t.Run(hex.EncodeToString([]byte{byte(v.Id)}), func(t *testing.T) {
			// Shared secret agreement: this vector's private key (Pr1) acts
			// on the peer's public key (Pk2).
			pk2Bytes, err := hex.DecodeString(v.Pk2)
			if err != nil {
				t.Fatalf("decode Pk2: %v", err)
			}
			var pub PublicKey
			if !pub.Import(pk2Bytes) {
				t.Fatal("PublicKey.Import failed")
			}

			prBytes, err := hex.DecodeString(v.Pr1)
			if err != nil {
				t.Fatalf("decode Pr1: %v", err)
			}
			var prv PrivateKey
			if !prv.Import(prBytes) {
				t.Fatal("PrivateKey.Import failed")
			}

			var out [64]byte
			ok, err := DeriveSecret(&out, &pub, &prv, rand.Reader)
			if err != nil {
				t.Fatalf("DeriveSecret error: %v", err)
			}
			if !ok {
				t.Fatal("DeriveSecret reported invalid public key for a 'valid' vector")
			}

			want, err := hex.DecodeString(v.Ss)
			if err != nil {
				t.Fatalf("decode Ss: %v", err)
			}
			if !bytes.Equal(out[:len(want)], want) {
				t.Errorf("DeriveSecret mismatch:\n got  %x\n want %x", out[:len(want)], want)
			}
		})
	}
}

// cyclicReader deterministically repeats a fixed byte pattern forever, so
// GeneratePrivateKey's rejection-sampling loop can always find enough
// qualifying bytes regardless of how much of the stream it consumes.
type cyclicReader struct {
	pattern []byte
	pos     int
}

func (r *cyclicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.pattern[r.pos]
		r.pos = (r.pos + 1) % len(r.pattern)
	}
	return len(p), nil
}

// TestGenerateKeyPairDeterminism checks that key generation from a seeded
// reader is deterministic: the same byte stream produces the same private
// and public key, and a different stream produces a different key pair.
func TestGenerateKeyPairDeterminism(t *testing.T) {
	seed := []byte{0x42, 0x01, 0x99, 0x7f, 0x30, 0x55, 0xaa, 0x0c}

	var prv1 PrivateKey
	if err := GeneratePrivateKey(&prv1, &cyclicReader{pattern: seed}); err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	var prv2 PrivateKey
	if err := GeneratePrivateKey(&prv2, &cyclicReader{pattern: seed}); err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}

	buf1 := make([]byte, PrivateKeySize)
	buf2 := make([]byte, PrivateKeySize)
	if !prv1.Export(buf1) || !prv2.Export(buf2) {
		t.Fatal("Export failed")
	}
	if !bytes.Equal(buf1, buf2) {
		t.Fatalf("same seed produced different private keys:\n%x\n%x", buf1, buf2)
	}

	otherSeed := []byte{0x24, 0x88, 0x11, 0xef, 0x03, 0x5c, 0x90, 0xd4}
	var prv3 PrivateKey
	if err := GeneratePrivateKey(&prv3, &cyclicReader{pattern: otherSeed}); err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	buf3 := make([]byte, PrivateKeySize)
	if !prv3.Export(buf3) {
		t.Fatal("Export failed")
	}
	if bytes.Equal(buf1, buf3) {
		t.Fatal("different seeds produced the same private key")
	}

	// Public keys derived from the two different private keys must differ,
	// and shared-secret agreement between the two parties must be symmetric.
	var pub1, pub3 PublicKey
	if err := GeneratePublicKey(&pub1, &prv1, rand.Reader); err != nil {
		t.Fatalf("GeneratePublicKey(prv1): %v", err)
	}
	if err := GeneratePublicKey(&pub3, &prv3, rand.Reader); err != nil {
		t.Fatalf("GeneratePublicKey(prv3): %v", err)
	}

	pk1 := make([]byte, PublicKeySize)
	pk3 := make([]byte, PublicKeySize)
	pub1.Export(pk1)
	pub3.Export(pk3)
	if bytes.Equal(pk1, pk3) {
		t.Fatal("different private keys produced the same public key")
	}

	var secretA, secretB [64]byte
	okA, err := DeriveSecret(&secretA, &pub3, &prv1, rand.Reader)
	if err != nil || !okA {
		t.Fatalf("DeriveSecret(A) = %v, %v", okA, err)
	}
	okB, err := DeriveSecret(&secretB, &pub1, &prv3, rand.Reader)
	if err != nil || !okB {
		t.Fatalf("DeriveSecret(B) = %v, %v", okB, err)
	}
	if secretA != secretB {
		t.Fatalf("shared secret agreement failed:\nA=%x\nB=%x", secretA, secretB)
	}
}

// TestValidateRejectsOutOfRangeOrSmoothKeys checks the documented invalid
// public key rejections: an out-of-field value, and the two values (2, -2)
// that correspond to smooth (non-supersingular) Montgomery curves.
func TestValidateRejectsOutOfRangeOrSmoothKeys(t *testing.T) {
	// Overflowing value: all 0xFF bytes is larger than the field prime p.
	tooBig := make([]byte, PublicKeySize)
	for i := range tooBig {
		tooBig[i] = 0xFF
	}
	var pubBig PublicKey
	if !pubBig.Import(tooBig) {
		t.Fatal("Import should accept any 64-byte buffer regardless of range")
	}
	ok, err := Validate(&pubBig, rand.Reader)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if ok {
		t.Error("expected Validate to reject an out-of-field public key")
	}

	// a == 2 encodes a smooth curve; must be rejected regardless of range.
	zero := make([]byte, PublicKeySize)
	var pubSmooth PublicKey
	pubSmooth.Import(zero)
	pubSmooth.a = two
	ok, err = Validate(&pubSmooth, rand.Reader)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if ok {
		t.Error("expected Validate to reject a == 2 (smooth curve)")
	}

	pubSmooth.a = twoNeg
	ok, err = Validate(&pubSmooth, rand.Reader)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if ok {
		t.Error("expected Validate to reject a == -2 (smooth curve)")
	}
}

// TestValidateAcceptsGeneratedKey checks that a freshly generated public key
// (the base curve acted on by a random private key) always validates.
func TestValidateAcceptsGeneratedKey(t *testing.T) {
	var prv PrivateKey
	if err := GeneratePrivateKey(&prv, rand.Reader); err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	var pub PublicKey
	if err := GeneratePublicKey(&pub, &prv, rand.Reader); err != nil {
		t.Fatalf("GeneratePublicKey: %v", err)
	}
	ok, err := Validate(&pub, rand.Reader)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !ok {
		t.Error("expected a freshly generated public key to validate")
	}
}

// TestImportExportRoundTrips exercises the size-checked Import/Export paths
// for both key types, including the undersized-buffer rejection branches.
func TestImportExportRoundTrips(t *testing.T) {
	var prv PrivateKey
	if err := GeneratePrivateKey(&prv, rand.Reader); err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	buf := make([]byte, PrivateKeySize)
	if !prv.Export(buf) {
		t.Fatal("PrivateKey.Export failed")
	}
	var prv2 PrivateKey
	if !prv2.Import(buf) {
		t.Fatal("PrivateKey.Import failed")
	}
	buf2 := make([]byte, PrivateKeySize)
	prv2.Export(buf2)
	if !bytes.Equal(buf, buf2) {
		t.Fatal("private key round trip mismatch")
	}

	if prv.Import(make([]byte, PrivateKeySize-1)) {
		t.Error("expected Import to reject an undersized buffer")
	}
	if prv.Export(make([]byte, PrivateKeySize-1)) {
		t.Error("expected Export to reject an undersized buffer")
	}

	var pub PublicKey
	if err := GeneratePublicKey(&pub, &prv, rand.Reader); err != nil {
		t.Fatalf("GeneratePublicKey: %v", err)
	}
	pbuf := make([]byte, PublicKeySize)
	if !pub.Export(pbuf) {
		t.Fatal("PublicKey.Export failed")
	}
	var pub2 PublicKey
	if !pub2.Import(pbuf) {
		t.Fatal("PublicKey.Import failed")
	}
	pbuf2 := make([]byte, PublicKeySize)
	pub2.Export(pbuf2)
	if !bytes.Equal(pbuf, pbuf2) {
		t.Fatal("public key round trip mismatch")
	}

	if pub.Import(make([]byte, PublicKeySize-1)) {
		t.Error("expected Import to reject a wrong-sized buffer")
	}
	if pub.Export(make([]byte, PublicKeySize+1)) {
		t.Error("expected Export to reject a wrong-sized buffer")
	}
}
