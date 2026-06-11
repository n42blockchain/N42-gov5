//go:build ignore
// +build ignore

// Generator for N42 lib/qmdb 16-way AVX-512 BLAKE3 kernels.
//
// Modeled on lukechampine.com/blake3@v1.4.1 avo/gen.go (BSD), with two
// N42-specific kernels:
//
//   - compressNodes16AVX512: 16 independent single-block compressions with the
//     flags argument applied VERBATIM (no FlagParent injection) — reproduces
//     qmdb.hashNode (ChunkStart|ChunkEnd|Root, counter 0, CV=key) bit-exactly,
//     16 pairs per call.
//   - compressLeaves16AVX512: 16 independent TWO-block chained compressions
//     (one short message each, 65..128 bytes zero-padded to 128) with
//     per-lane second-block lengths — reproduces qmdb.hashLeaf's fast path
//     16 leaves per call.
package main

import (
	"fmt"

	. "github.com/mmcloughlin/avo/build"
	. "github.com/mmcloughlin/avo/operand"
	. "github.com/mmcloughlin/avo/reg"
)

func main() {
	genGlobals()
	genCompressNodes16AVX512()
	genCompressLeaves16AVX512()

	Generate()
}

var globals struct {
	iv  Mem
	seq Mem
}

// broadcastU32Arg broadcasts a uint32 stack argument through a GP register —
// `go vet`'s asmdecl check false-positives on the EVEX VPBROADCASTD-from-FP
// form (it assumes a full vector-width memory read).
func broadcastU32Arg(arg Mem, dst VecVirtual) {
	r := GP32()
	MOVL(arg, r)
	x := XMM()
	VMOVD(r, x)
	VPBROADCASTD(x, dst)
}

func genGlobals() {
	globals.iv = GLOBL("iv", RODATA|NOPTR)
	DATA(0*4, U32(0x6A09E667))
	DATA(1*4, U32(0xBB67AE85))
	DATA(2*4, U32(0x3C6EF372))
	DATA(3*4, U32(0xA54FF53A))

	globals.seq = GLOBL("seq", RODATA|NOPTR)
	for i := 0; i < 16; i++ {
		DATA(i*4, U32(i))
	}
}

// genCompressNodes16AVX512 emits the 16-way single-block kernel.
func genCompressNodes16AVX512() {
	TEXT("compressNodes16AVX512", NOSPLIT, "func(parents *[16][8]uint32, cvs *[32][8]uint32, key *[8]uint32, flags uint32)")
	parents := Mem{Base: Load(Param("parents"), GP64())}
	cvs := Mem{Base: Load(Param("cvs"), GP64())}
	key := Mem{Base: Load(Param("key"), GP64())}
	flags, _ := Param("flags").Resolve()

	var vs, mv [16]VecVirtual
	for i := range vs {
		vs[i], mv[i] = ZMM(), ZMM()
	}

	Comment("Load transposed block (16 lanes x 64-byte block, stride 64)")
	stride := ZMM()
	VMOVDQU32(globals.seq, stride)
	VPSLLD(Imm(6), stride, stride)
	for i, m := range mv {
		KXNORW(K0, K0, K1)
		VPGATHERDD(cvs.Offset(i*4).Idx(stride, 1), K1, m)
	}

	Comment("Initialize state vectors")
	for i, v := range vs {
		switch i {
		case 0, 1, 2, 3, 4, 5, 6, 7: // cv = key
			VPBROADCASTD(key.Offset(i*4), v)
		case 8, 9, 10, 11: // iv
			VPBROADCASTD(globals.iv.Offset((i-8)*4), v)
		case 12, 13: // counter = 0
			VPXORD(v, v, v)
		case 14: // blockLen = 64
			VPBROADCASTD(globals.seq.Offset(1*4), v)
			VPSLLD(Imm(6), v, v)
		case 15: // flags — VERBATIM, no FlagParent OR
			broadcastU32Arg(flags.Addr, v)
		}
	}

	performRoundsAVX512(vs, mv)

	Comment("Finalize and store transposed CVs (stride 32)")
	for i := range vs[:8] {
		VPXORD(vs[i], vs[i+8], vs[i])
	}
	VMOVDQU32(globals.seq, stride)
	VPSLLD(Imm(5), stride, stride)
	for i, v := range vs[:8] {
		KXNORW(K0, K0, K1)
		VPSCATTERDD(v, K1, parents.Offset(i*4).Idx(stride, 1))
	}

	RET()
}

// genCompressLeaves16AVX512 emits the 16-way two-block chained kernel.
// buf holds 16 lanes x 128 bytes (zero-padded messages, stride 128).
// Block 1 is always full (BlockLen 64, flags1); block 2 has per-lane
// lengths from blockLens and flags2.
func genCompressLeaves16AVX512() {
	TEXT("compressLeaves16AVX512", NOSPLIT, "func(out *[16][8]uint32, buf *[2048]byte, key *[8]uint32, blockLens *[16]uint32, flags1 uint32, flags2 uint32)")
	out := Mem{Base: Load(Param("out"), GP64())}
	buf := Mem{Base: Load(Param("buf"), GP64())}
	key := Mem{Base: Load(Param("key"), GP64())}
	blockLens := Mem{Base: Load(Param("blockLens"), GP64())}
	flags1, _ := Param("flags1").Resolve()
	flags2, _ := Param("flags2").Resolve()

	var vs, mv [16]VecVirtual
	for i := range vs {
		vs[i], mv[i] = ZMM(), ZMM()
	}
	cvSpill := AllocLocal(8 * 64)

	Comment("Block 1: load transposed (stride 128)")
	stride := ZMM()
	VMOVDQU32(globals.seq, stride)
	VPSLLD(Imm(7), stride, stride)
	for i, m := range mv {
		KXNORW(K0, K0, K1)
		VPGATHERDD(buf.Offset(i*4).Idx(stride, 1), K1, m)
	}

	Comment("Block 1 state: cv=key, len=64, flags1")
	for i, v := range vs {
		switch i {
		case 0, 1, 2, 3, 4, 5, 6, 7:
			VPBROADCASTD(key.Offset(i*4), v)
		case 8, 9, 10, 11:
			VPBROADCASTD(globals.iv.Offset((i-8)*4), v)
		case 12, 13:
			VPXORD(v, v, v)
		case 14:
			VPBROADCASTD(globals.seq.Offset(1*4), v)
			VPSLLD(Imm(6), v, v)
		case 15:
			broadcastU32Arg(flags1.Addr, v)
		}
	}

	performRoundsAVX512(vs, mv)

	Comment("Chain: cv_i = vs_i ^ vs_{i+8}, spill to stack")
	for i := range vs[:8] {
		VPXORD(vs[i], vs[i+8], vs[i])
		VMOVDQU32(vs[i], cvSpill.Offset(i*64))
	}

	Comment("Block 2: load transposed (offset 64, stride 128)")
	VMOVDQU32(globals.seq, stride)
	VPSLLD(Imm(7), stride, stride)
	for i, m := range mv {
		KXNORW(K0, K0, K1)
		VPGATHERDD(buf.Offset(64+i*4).Idx(stride, 1), K1, m)
	}

	Comment("Block 2 state: cv=chained, per-lane blockLen, flags2")
	for i, v := range vs {
		switch i {
		case 0, 1, 2, 3, 4, 5, 6, 7:
			VMOVDQU32(cvSpill.Offset(i*64), v)
		case 8, 9, 10, 11:
			VPBROADCASTD(globals.iv.Offset((i-8)*4), v)
		case 12, 13:
			VPXORD(v, v, v)
		case 14:
			VMOVDQU32(blockLens, v)
		case 15:
			broadcastU32Arg(flags2.Addr, v)
		}
	}

	performRoundsAVX512(vs, mv)

	Comment("Finalize and store transposed outputs (stride 32)")
	for i := range vs[:8] {
		VPXORD(vs[i], vs[i+8], vs[i])
	}
	VMOVDQU32(globals.seq, stride)
	VPSLLD(Imm(5), stride, stride)
	for i, v := range vs[:8] {
		KXNORW(K0, K0, K1)
		VPSCATTERDD(v, K1, out.Offset(i*4).Idx(stride, 1))
	}

	RET()
}

// performRoundsAVX512 is verbatim from lukechampine.com/blake3 avo/gen.go.
func performRoundsAVX512(vs, mv [16]VecVirtual) {
	g := func(a, b, c, d, mx, my VecVirtual) {
		VPADDD(a, b, a)
		VPADDD(mx, a, a)
		VPXORD(d, a, d)
		VPRORD(Imm(16), d, d)
		VPADDD(c, d, c)
		VPXORD(b, c, b)
		VPRORD(Imm(12), b, b)
		VPADDD(a, b, a)
		VPADDD(my, a, a)
		VPXORD(d, a, d)
		VPRORD(Imm(8), d, d)
		VPADDD(c, d, c)
		VPXORD(b, c, b)
		VPRORD(Imm(7), b, b)
	}

	for i := 0; i < 7; i++ {
		Comment(fmt.Sprintf("Round %v", i+1))
		g(vs[0], vs[4], vs[8], vs[12], mv[0], mv[1])
		g(vs[1], vs[5], vs[9], vs[13], mv[2], mv[3])
		g(vs[2], vs[6], vs[10], vs[14], mv[4], mv[5])
		g(vs[3], vs[7], vs[11], vs[15], mv[6], mv[7])
		g(vs[0], vs[5], vs[10], vs[15], mv[8], mv[9])
		g(vs[1], vs[6], vs[11], vs[12], mv[10], mv[11])
		g(vs[2], vs[7], vs[8], vs[13], mv[12], mv[13])
		g(vs[3], vs[4], vs[9], vs[14], mv[14], mv[15])

		// permute
		mv = [16]VecVirtual{
			mv[2], mv[6], mv[3], mv[10],
			mv[7], mv[0], mv[4], mv[13],
			mv[1], mv[11], mv[12], mv[5],
			mv[9], mv[14], mv[15], mv[8],
		}
	}
}


