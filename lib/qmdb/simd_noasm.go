// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !amd64 || purego

package qmdb

// Portable fallbacks for the AVX2 batch compression (simd_amd64.go).

import "unsafe"

const haveAVX2 = false

func prefetcht0(p unsafe.Pointer) {}

func hashNodes8(out *[8]Hash, pairs *[16]Hash) {
	for i := 0; i < 8; i++ {
		out[i] = hashNode(pairs[2*i], pairs[2*i+1])
	}
}

func hashNodesRun(nodes []Hash, base, n int) {
	for i := 0; i < n; i++ {
		p := base + i
		nodes[p] = hashNode(nodes[2*p], nodes[2*p+1])
	}
}
