//go:build amd64 && !purego

package keccakf1600

import "github.com/n42blockchain/N42/crypto/sha3"

const simdAVX512 = true

func permuteSIMDx8(state []uint64) { f1600x8AVX512(&state[0], &sha3.RC) }

func permuteSIMDx4VL(state []uint64) { f1600x4AVX512VL(&state[0], &sha3.RC) }

func oneBlockX8(out *[32]uint64, ptrs, full, tail *[8]uint64) {
	keccak256x8OneBlockAVX512(out, &sha3.RC, ptrs, full, tail)
}
