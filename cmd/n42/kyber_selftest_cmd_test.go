// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import "testing"

func TestKyberSelftest(t *testing.T) {
	if _, err := kyberSelftest([]byte("n42 kyber selftest"), 25, true); err != nil {
		t.Fatal(err)
	}
	if _, err := kyberSelftest(nil, 3, false); err != nil {
		t.Fatal(err)
	}
}

func TestKyberDerive(t *testing.T) {
	a := kyberDerive([]byte("s"), 1, 'k', 100)
	b := kyberDerive([]byte("s"), 1, 'k', 100)
	c := kyberDerive([]byte("s"), 2, 'k', 100)
	d := kyberDerive([]byte("s"), 1, 'e', 100)
	if len(a) != 100 || string(a) != string(b) || string(a) == string(c) || string(a) == string(d) {
		t.Fatal("derivation is not deterministic per (round, label)")
	}
}
