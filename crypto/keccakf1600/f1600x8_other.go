//go:build !amd64 || purego

package keccakf1600

const simdAVX512 = false

func permuteSIMDx8(state []uint64) { permuteScalarX8(state) }

func permuteSIMDx4VL(state []uint64) { permuteScalarX4(state, false) }

func oneBlockX8(out *[32]uint64, ptrs, full, tail *[8]uint64) { panic("keccakf1600: no AVX-512") }
