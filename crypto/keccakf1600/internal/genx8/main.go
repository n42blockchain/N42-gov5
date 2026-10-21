//go:build ignore

// Command genx8 generates f1600x8_amd64.s: fully unrolled 24-round
// Keccak-f[1600] permutations on AVX-512 registers, eight-way on ZMM
// (AVX-512F) and four-way on YMM (AVX-512F + AVX-512VL).
//
// Every lane of the 25-lane state lives in its own vector register for the
// whole permutation (lane i of instance j is at state[width*i+j]), leaving
// seven scratch registers. Theta parities and the theta application use
// VPTERNLOGQ three-way XORs, chi is one VPTERNLOGQ per lane, rho is VPROLQ.
// Pi costs nothing: it is a renaming of registers tracked at generation
// time, as is the register rotation chi needs for its two copies per row.
//
// Usage (from crypto/keccakf1600): go run internal/genx8/main.go
package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type gen struct {
	buf   bytes.Buffer
	reg   string // "Z" or "Y"
	lane  [25]int
	free  []int
	c     [5]int // next round's column parities when haveC
	haveC bool
}

func (g *gen) p(format string, args ...any) {
	fmt.Fprintf(&g.buf, "\t"+format+"\n", args...)
}

func (g *gen) r(n int) string { return fmt.Sprintf("%s%d", g.reg, n) }

func (g *gen) alloc() int {
	n := g.free[0]
	g.free = g.free[1:]
	return n
}

func (g *gen) release(n int) { g.free = append(g.free, n) }

// rho rotation offsets and the pi destination of each lane index x+5y.
func rhoPi() (rot [25]int, dst [25]int) {
	x, y := 1, 0
	for t := 0; t < 24; t++ {
		rot[x+5*y] = ((t + 1) * (t + 2) / 2) % 64
		x, y = y, (2*x+3*y)%5
	}
	for x := 0; x < 5; x++ {
		for y := 0; y < 5; y++ {
			dst[x+5*y] = y + 5*((2*x+3*y)%5)
		}
	}
	return
}

func (g *gen) round(i int) {
	rot, dst := rhoPi()
	fmt.Fprintf(&g.buf, "\n\t// round %d\n", i)
	// Theta: column parities, unless the previous round's chi already
	// computed them.
	c := g.c
	if !g.haveC {
		for x := 0; x < 5; x++ {
			c[x] = g.alloc()
			g.p("VMOVDQA64 %s, %s", g.r(g.lane[x]), g.r(c[x]))
			g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(g.lane[x+10]), g.r(g.lane[x+5]), g.r(c[x]))
			g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(g.lane[x+20]), g.r(g.lane[x+15]), g.r(c[x]))
		}
	}
	g.haveC = false
	// Theta: A[x,y] ^= C[x-1] ^ rol(C[x+1], 1), then rho in place
	// column by column; pi is a renaming.
	t := g.alloc()
	var b [25]int
	for x := 0; x < 5; x++ {
		if thetaShift {
			// D = C[x-1] ^ (C[x+1] << 1) ^ (C[x+1] >> 63) on the shift
			// pipes, keeping the rotate pipes for rho.
			u := g.alloc()
			g.p("VPSLLQ $1, %s, %s", g.r(c[(x+1)%5]), g.r(t))
			g.p("VPSRLQ $63, %s, %s", g.r(c[(x+1)%5]), g.r(u))
			g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(c[(x+4)%5]), g.r(u), g.r(t))
			g.release(u)
			for y := 0; y < 5; y++ {
				l := g.r(g.lane[x+5*y])
				g.p("VPXORQ %s, %s, %s", g.r(t), l, l)
			}
		} else {
			g.p("VPROLQ $1, %s, %s", g.r(c[(x+1)%5]), g.r(t))
			for y := 0; y < 5; y++ {
				l := g.r(g.lane[x+5*y])
				g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(t), g.r(c[(x+4)%5]), l)
			}
		}
		if colOrder {
			for y := 0; y < 5; y++ {
				g.rho(x+5*y, rot[x+5*y])
			}
		}
	}
	g.release(t)
	for x := 0; x < 5; x++ {
		g.release(c[x])
	}
	if !colOrder {
		for k := 0; k < 25; k++ {
			g.rho(k, rot[k])
		}
	}
	for k := 0; k < 25; k++ {
		b[dst[k]] = g.lane[k]
	}
	// Chi: A[x,y] = B[x,y] ^ (^B[x+1,y] & B[x+2,y]), imm 0xd2.
	for y := 0; y < 5; y++ {
		r := b[5*y : 5*y+5]
		f, h := g.alloc(), g.alloc()
		g.p("VMOVDQA64 %s, %s", g.r(r[0]), g.r(f))
		g.p("VPTERNLOGQ $0xd2, %s, %s, %s", g.r(r[2]), g.r(r[1]), g.r(f))
		g.p("VMOVDQA64 %s, %s", g.r(r[1]), g.r(h))
		g.p("VPTERNLOGQ $0xd2, %s, %s, %s", g.r(r[3]), g.r(r[2]), g.r(h))
		g.p("VPTERNLOGQ $0xd2, %s, %s, %s", g.r(r[4]), g.r(r[3]), g.r(r[2]))
		g.p("VPTERNLOGQ $0xd2, %s, %s, %s", g.r(r[0]), g.r(r[4]), g.r(r[3]))
		g.p("VPTERNLOGQ $0xd2, %s, %s, %s", g.r(r[1]), g.r(r[0]), g.r(r[4]))
		g.release(r[0])
		g.release(r[1])
		g.lane[5*y+0] = f
		g.lane[5*y+1] = h
		g.lane[5*y+2] = r[2]
		g.lane[5*y+3] = r[3]
		g.lane[5*y+4] = r[4]
		if y == 0 {
			// Iota.
			g.p("VPXORQ.BCST %d(CX), %s, %s", 8*i, g.r(g.lane[0]), g.r(g.lane[0]))
		}
		if !fuseParity || i == 23 {
			continue
		}
		// The next round's column parities, as soon as their rows exist.
		switch y {
		case 2:
			for x := 0; x < 5; x++ {
				g.c[x] = g.alloc()
				g.p("VMOVDQA64 %s, %s", g.r(g.lane[x]), g.r(g.c[x]))
				g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(g.lane[x+10]), g.r(g.lane[x+5]), g.r(g.c[x]))
			}
		case 4:
			for x := 0; x < 5; x++ {
				g.p("VPTERNLOGQ $0x96, %s, %s, %s", g.r(g.lane[x+20]), g.r(g.lane[x+15]), g.r(g.c[x]))
			}
			g.haveC = true
		}
	}
}

// Scheduling knobs, kept for re-tuning on other cores; the defaults were
// fastest on Zen 5 (rotates issue on two pipes, ternary logic on four,
// two-cycle latency for both):
//
//	THETA=shift   theta's rotate by one as two shifts (slower)
//	ORDER=lane    rho after the whole theta step instead of per column (slower)
//	PAR=late      next-round parities after chi instead of inside it
//	RHOSHIFT="k…" rho of lanes k… as two shifts and a XOR
var (
	thetaShift = os.Getenv("THETA") == "shift"
	colOrder   = os.Getenv("ORDER") != "lane"
	fuseParity = os.Getenv("PAR") != "late"
	rhoShift   = map[int]bool{}
)

// rho rotates lane k left by r in place.
func (g *gen) rho(k, r int) {
	if r == 0 {
		return
	}
	l := g.r(g.lane[k])
	if rhoShift[k] {
		u := g.alloc()
		g.p("VPSLLQ $%d, %s, %s", r, l, g.r(u))
		g.p("VPSRLQ $%d, %s, %s", 64-r, l, l)
		g.p("VPXORQ %s, %s, %s", g.r(u), l, l)
		g.release(u)
		return
	}
	g.p("VPROLQ $%d, %s, %s", r, l, l)
}

func (g *gen) reset(reg string) {
	g.reg = reg
	g.free = nil
	g.haveC = false
	for k := 0; k < 25; k++ {
		g.lane[k] = k
	}
	for k := 25; k < 32; k++ {
		g.free = append(g.free, k)
	}
}

func (g *gen) rounds() {
	for i := 0; i < 24; i++ {
		g.round(i)
	}
	g.buf.WriteString("\n")
}

// fn emits a permutation of the interleaved state in place.
func (g *gen) fn(name, reg string, width int, req string) {
	g.reset(reg)
	stride := 8 * width
	fmt.Fprintf(&g.buf, "\n// func %s(state *uint64, rc *[24]uint64)\n// Requires: %s\n", name, req)
	fmt.Fprintf(&g.buf, "TEXT ·%s(SB), NOSPLIT, $0-16\n", name)
	g.p("MOVQ state+0(FP), AX")
	g.p("MOVQ rc+8(FP), CX")
	for k := 0; k < 25; k++ {
		g.p("VMOVDQU64 %d(AX), %s", stride*k, g.r(k))
	}
	g.rounds()
	for k := 0; k < 25; k++ {
		g.p("VMOVDQU64 %s, %d(AX)", g.r(g.lane[k]), stride*k)
	}
	g.p("VZEROUPPER")
	g.p("RET")
}

// oneBlock emits the eight-way single-block Keccak-256 sponge: it gathers
// each input's full words straight from memory (masked VPGATHERQQ, so no
// byte past a full word is read), merges the caller's padded tail words,
// adds the final 0x80 pad bit, permutes, and stores the four digest lanes
// interleaved to out (out[8*k+i] is lane k of input i).
func (g *gen) oneBlock() {
	g.reset("Z")
	const name = "keccak256x8OneBlockAVX512"
	fmt.Fprintf(&g.buf, "\n// func %s(out *[32]uint64, rc *[24]uint64, ptrs *[8]uint64, full *[8]uint64, tail *[8]uint64)\n", name)
	g.buf.WriteString("// Requires: AVX512F\n")
	fmt.Fprintf(&g.buf, "TEXT ·%s(SB), NOSPLIT, $0-40\n", name)
	g.p("MOVQ out+0(FP), DI")
	g.p("MOVQ rc+8(FP), CX")
	g.p("MOVQ ptrs+16(FP), SI")
	g.p("MOVQ full+24(FP), DX")
	g.p("MOVQ tail+32(FP), BX")
	g.p("VMOVDQU64 (SI), Z25")  // gather indices: the input addresses
	g.p("VMOVDQU64 (DX), Z26")  // full words per input
	g.p("VMOVDQU64 (BX), Z27")  // padded tail word per input, at index full
	g.p("VPXORQ Z28, Z28, Z28") // word index j in every element
	g.p("MOVQ $1, R8")
	g.p("VPBROADCASTQ R8, Z29")
	g.p("XORQ R9, R9")
	for j := 0; j < 17; j++ {
		z := g.r(j)
		g.p("VPCMPUQ $6, Z28, Z26, K1") // full > j: word j is a full word
		g.p("VPCMPEQQ Z28, Z26, K2")    // full == j: word j is the tail
		g.p("VPXORQ %s, %s, %s", z, z, z)
		g.p("VPGATHERQQ %d(R9)(Z25*1), K1, %s", 8*j, z)
		g.p("VMOVDQA64 Z27, K2, %s", z)
		if j < 16 {
			g.p("VPADDQ Z29, Z28, Z28")
		}
	}
	g.p("MOVQ $0x8000000000000000, R8")
	g.p("VPBROADCASTQ R8, Z30")
	g.p("VPXORQ Z30, Z16, Z16")
	for j := 17; j < 25; j++ {
		z := g.r(j)
		g.p("VPXORQ %s, %s, %s", z, z, z)
	}
	g.rounds()
	for k := 0; k < 4; k++ {
		g.p("VMOVDQU64 %s, %d(DI)", g.r(g.lane[k]), 64*k)
	}
	g.p("VZEROUPPER")
	g.p("RET")
}

func main() {
	for _, f := range strings.Fields(os.Getenv("RHOSHIFT")) {
		k, _ := strconv.Atoi(f)
		rhoShift[k] = true
	}
	g := &gen{}
	g.buf.WriteString("// Code generated by command: go run internal/genx8/main.go. DO NOT EDIT.\n\n")
	g.buf.WriteString("//go:build amd64 && !purego\n\n#include \"textflag.h\"\n")
	g.fn("f1600x8AVX512", "Z", 8, "AVX512F")
	g.fn("f1600x4AVX512VL", "Y", 4, "AVX512F, AVX512VL")
	g.oneBlock()
	if err := os.WriteFile("f1600x8_amd64.s", g.buf.Bytes(), 0o644); err != nil {
		panic(err)
	}
}
