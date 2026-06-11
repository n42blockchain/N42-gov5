// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build amd64 && !purego

package qmdb

import (
	"unsafe"

	"golang.org/x/sys/cpu"

	"lukechampine.com/blake3/guts"
)

// compressNodes8AVX2 is the vendored 8-way BLAKE3 single-block compression
// (simd_amd64.s) with the flags argument applied VERBATIM — see the .s header.
//
//go:noescape
func compressNodes8AVX2(parents *[8][8]uint32, cvs *[16][8]uint32, key *[8]uint32, flags uint32)

// prefetcht0 hints the cache line at p into all cache levels
// (prefetch_amd64.s). Used to overlap the index/twig DRAM misses of upcoming
// batch ops with current work.
//
//go:noescape
func prefetcht0(p unsafe.Pointer)

// compressNodes16AVX512 / compressLeaves16AVX512 are avo-generated 16-way
// ZMM kernels (simd16_amd64.s; generator vendored at the repo sibling
// C:\N42\avogen, modeled on blake3's avo/gen.go). Both apply flags VERBATIM.
// The leaves kernel chains two blocks per lane (block 1: full 64 B, flags1;
// block 2: per-lane length from blockLens, flags2) over 16 zero-padded
// 128-byte messages.
//
//go:noescape
func compressNodes16AVX512(parents *[16][8]uint32, cvs *[32][8]uint32, key *[8]uint32, flags uint32)

//go:noescape
func compressLeaves16AVX512(out *[16][8]uint32, buf *[2048]byte, key *[8]uint32, blockLens *[16]uint32, flags1 uint32, flags2 uint32)

var haveAVX2 = cpu.X86.HasAVX2
var haveAVX512 = cpu.X86.HasAVX512F

// hashNodeFlags is the exact flag set hashNode passes to CompressNode.
const hashNodeFlags = guts.FlagChunkStart | guts.FlagChunkEnd | guts.FlagRoot

// ivKey mirrors guts.IV as the [8]uint32 key argument.
var ivKey = guts.IV

// hashNodes8 computes out[i] = hashNode(pairs[2i], pairs[2i+1]) for 8 pairs in
// one AVX2 pass. pairs/out may NOT alias. Bit-identical to 8 hashNode calls
// (equivalence is asserted by TestHashNodes8).
//
// Layout note: a Hash is 32 little-endian bytes; reinterpreting it as
// [8]uint32 on amd64 yields exactly the words CompressNode sees via
// BytesToWords, so the casts below are pure views, no conversion.
func hashNodes8(out *[8]Hash, pairs *[16]Hash) {
	if !haveAVX2 {
		for i := 0; i < 8; i++ {
			out[i] = hashNode(pairs[2*i], pairs[2*i+1])
		}
		return
	}
	compressNodes8AVX2(
		(*[8][8]uint32)(unsafe.Pointer(out)),
		(*[16][8]uint32)(unsafe.Pointer(pairs)),
		&ivKey,
		uint32(hashNodeFlags),
	)
}

// hashNodes16 computes out[i] = hashNode(pairs[2i], pairs[2i+1]) for 16 pairs
// in one AVX-512 pass. pairs/out may NOT alias.
func hashNodes16(out *[16]Hash, pairs *[32]Hash) {
	compressNodes16AVX512(
		(*[16][8]uint32)(unsafe.Pointer(out)),
		(*[32][8]uint32)(unsafe.Pointer(pairs)),
		&ivKey,
		uint32(hashNodeFlags),
	)
}

// hashNodesRun folds n consecutive parents IN PLACE inside a node heap:
// nodes[base+i] = hashNode(nodes[2*(base+i)], nodes[2*(base+i)+1]) for i < n.
// The children of consecutive parents are themselves consecutive, so 16- or
// 8-parent groups compress straight out of / into the heap, zero gather copies.
func hashNodesRun(nodes []Hash, base, n int) {
	i := 0
	if haveAVX512 {
		for ; i+16 <= n; i += 16 {
			p := base + i
			compressNodes16AVX512(
				(*[16][8]uint32)(unsafe.Pointer(&nodes[p])),
				(*[32][8]uint32)(unsafe.Pointer(&nodes[2*p])),
				&ivKey,
				uint32(hashNodeFlags),
			)
		}
	}
	if haveAVX2 {
		for ; i+8 <= n; i += 8 {
			p := base + i
			compressNodes8AVX2(
				(*[8][8]uint32)(unsafe.Pointer(&nodes[p])),
				(*[16][8]uint32)(unsafe.Pointer(&nodes[2*p])),
				&ivKey,
				uint32(hashNodeFlags),
			)
		}
	}
	for ; i < n; i++ {
		p := base + i
		nodes[p] = hashNode(nodes[2*p], nodes[2*p+1])
	}
}

// leafFlags1/leafFlags2 are the two block flag sets of hashLeaf's chained
// fast path (first block of the chunk; final block + root).
const (
	leafFlags1 = guts.FlagChunkStart
	leafFlags2 = guts.FlagChunkEnd | guts.FlagRoot
)

// hashLeaves16 computes 16 two-block leaf hashes from 16 zero-padded 128-byte
// messages with per-lane second-block lengths (33+len(value)-64, in 1..64).
// Bit-identical to 16 hashLeaf calls on the same messages.
func hashLeaves16(out *[16]Hash, buf *[2048]byte, blockLens *[16]uint32) {
	compressLeaves16AVX512(
		(*[16][8]uint32)(unsafe.Pointer(out)),
		buf,
		&ivKey,
		blockLens,
		uint32(leafFlags1),
		uint32(leafFlags2),
	)
}
