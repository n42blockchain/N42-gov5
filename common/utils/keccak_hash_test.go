// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package utils

import (
	"testing"

	"golang.org/x/crypto/sha3"
)

// Keccak256Hash used to return the zero hash for every input: Sum appended
// to h[:] instead of writing into it.
func TestKeccak256HashWritesTheDigest(t *testing.T) {
	d := sha3.NewLegacyKeccak256()
	d.Write([]byte("ab"))
	want := d.Sum(nil)
	got := Keccak256Hash([]byte("a"), []byte("b"))
	if string(got[:]) != string(want) {
		t.Fatalf("Keccak256Hash = %x, want %x", got, want)
	}
	if Keccak256Hash([]byte("a")) == Keccak256Hash([]byte("b")) {
		t.Fatal("different inputs hash equal")
	}
}
