//go:build amd64 && !purego

package keccakf1600

//go:generate go run internal/genx8/main.go

// f1600x8AVX512 applies the 24-round Keccak-f[1600] to eight interleaved
// states (lane i of state j at state[8*i+j]). Requires AVX-512F.
//
//go:noescape
func f1600x8AVX512(state *uint64, rc *[24]uint64)

// f1600x4AVX512VL applies the 24-round Keccak-f[1600] to four interleaved
// states (lane i of state j at state[4*i+j]) on YMM registers with the
// AVX-512 ternary-logic and rotate instructions. Requires AVX-512F and VL.
//
//go:noescape
func f1600x4AVX512VL(state *uint64, rc *[24]uint64)

// keccak256x8OneBlockAVX512 is the eight-way single-block Keccak-256
// sponge; see OneBlockX8. Requires AVX-512F.
//
//go:noescape
func keccak256x8OneBlockAVX512(out *[32]uint64, rc *[24]uint64, ptrs *[8]uint64, full *[8]uint64, tail *[8]uint64)
