// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"testing"

	"lukechampine.com/blake3"
)

// TestHashLeafFastPath asserts the direct two-block compression equals the
// pooled-hasher Blake3 for every message length across the fast/slow boundary.
func TestHashLeafFastPath(t *testing.T) {
	kh := shKey(42)
	for n := 0; n <= 200; n++ {
		v := make([]byte, n)
		for i := range v {
			v[i] = byte(i*7 + n)
		}
		got := hashLeaf(kh, v)
		h := leafHasherPool.Get().(*blake3.Hasher)
		h.Reset()
		_, _ = h.Write(leafDomain[:])
		_, _ = h.Write(kh[:])
		_, _ = h.Write(v)
		var want Hash
		h.Sum(want[:0])
		leafHasherPool.Put(h)
		if got != want {
			t.Fatalf("value len %d: fast %x != hasher %x", n, got[:8], want[:8])
		}
	}
}

// TestHashNodes16AndLeaves16 asserts the AVX-512 16-way kernels are
// bit-identical to their scalar counterparts.
func TestHashNodes16AndLeaves16(t *testing.T) {
	var pairs [32]Hash
	for i := range pairs {
		pairs[i] = shKey(uint64(i) * 0x517C)
	}
	var out [16]Hash
	hashNodes16(&out, &pairs)
	for i := 0; i < 16; i++ {
		if want := hashNode(pairs[2*i], pairs[2*i+1]); out[i] != want {
			t.Fatalf("pair %d: hashNodes16 %x != hashNode %x", i, out[i][:8], want[:8])
		}
	}

	if !haveAVX512 {
		t.Skip("no AVX-512")
	}
	// 16 leaves with varying value lengths (32..95 = the two-block band).
	var buf [2048]byte
	var lens [16]uint32
	keys := make([]Hash, 16)
	vals := make([][]byte, 16)
	for i := 0; i < 16; i++ {
		keys[i] = shKey(uint64(1000 + i))
		v := make([]byte, 32+(i*63/15)%64)
		for j := range v {
			v[j] = byte(i*31 + j)
		}
		vals[i] = v
		lane := buf[i*128 : (i+1)*128]
		lane[0] = 0x01
		copy(lane[1:33], keys[i][:])
		n := copy(lane[33:], v)
		lens[i] = uint32(33 + n - 64)
	}
	var outs [16]Hash
	hashLeaves16(&outs, &buf, &lens)
	for i := 0; i < 16; i++ {
		if want := hashLeaf(keys[i], vals[i]); outs[i] != want {
			t.Fatalf("leaf %d (val %dB): hashLeaves16 %x != hashLeaf %x", i, len(vals[i]), outs[i][:8], want[:8])
		}
	}
}

// TestHashNodes8 asserts the 8-way batch compression is bit-identical to
// hashNode — the root-compatibility requirement for the vendored asm (which
// must apply the ChunkStart|ChunkEnd|Root flags VERBATIM, no FlagParent).
func TestHashNodes8(t *testing.T) {
	var pairs [16]Hash
	for i := range pairs {
		pairs[i] = shKey(uint64(i) * 0x9E37)
	}
	var out [8]Hash
	hashNodes8(&out, &pairs)
	for i := 0; i < 8; i++ {
		if want := hashNode(pairs[2*i], pairs[2*i+1]); out[i] != want {
			t.Fatalf("pair %d: hashNodes8 %x != hashNode %x", i, out[i][:8], want[:8])
		}
	}

	// In-place heap run, including null leaves and a non-multiple-of-8 tail.
	heap := make([]Hash, 64)
	for i := 32; i < 64; i++ {
		if i%3 != 0 {
			heap[i] = shKey(uint64(i))
		}
	}
	want := make([]Hash, 64)
	copy(want, heap)
	for p := 16; p < 32; p++ {
		want[p] = hashNode(want[2*p], want[2*p+1])
	}
	hashNodesRun(heap, 16, 16)
	for p := 16; p < 32; p++ {
		if heap[p] != want[p] {
			t.Fatalf("run parent %d mismatch", p)
		}
	}
	// Packed cross-twig list through hashPackedList (runs + gather + tail),
	// mixing parents from two different twigs in one gather group.
	tr := New()
	twA, twB := newTwig(), newTwig()
	tr.twigs = []*twig{twA, twB}
	for i := uint64(0); i < TwigSize; i++ {
		if i%5 != 0 {
			twA.nodes[TwigSize+i] = shKey(i)
			twB.nodes[TwigSize+i] = shKey(i * 31)
		}
	}
	wantA, wantB := *twA.nodes, *twB.nodes
	hp := TwigSize / 2 // parent level base
	var g []uint64
	for _, p := range []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 40, 42, 99} {
		wantA[hp+int(p)] = hashNode(wantA[2*(hp+int(p))], wantA[2*(hp+int(p))+1])
		g = append(g, 0<<12|uint64(hp)+p)
	}
	for _, p := range []uint64{7, 13, 500} {
		wantB[hp+int(p)] = hashNode(wantB[2*(hp+int(p))], wantB[2*(hp+int(p))+1])
		g = append(g, 1<<12|uint64(hp)+p)
	}
	tr.hashPackedList(g)
	for j := range wantA {
		if twA.nodes[j] != wantA[j] || twB.nodes[j] != wantB[j] {
			t.Fatalf("packed list node %d mismatch", j)
		}
	}
}

func BenchmarkHashNodes8(b *testing.B) {
	var pairs [16]Hash
	for i := range pairs {
		pairs[i] = shKey(uint64(i))
	}
	var out [8]Hash
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hashNodes8(&out, &pairs)
	}
}

