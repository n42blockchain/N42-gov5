package keccakf1600

import (
	"unsafe"

	"golang.org/x/sys/cpu"

	"github.com/n42blockchain/N42/crypto/sha3"
)

// StateX8 contains state for the eight-way 24-round permutation: eight
// interleaved [25]uint64 buffers. Call Initialize() before use to get the
// interleaved buffer.
type StateX8 struct {
	// 8 x 25 uint64s plus seven uint64s of headroom to start the buffer on
	// a 64-byte boundary (one ZMM register per lane, no split loads).
	a [207]uint64

	// Offset into a that is 64 byte aligned.
	offset int
}

// IsEnabledX8 returns true if the eight-way permutation runs as one
// AVX-512 pass. Otherwise Permute is correct but runs the eight states one
// after another.
func IsEnabledX8() bool { return enabledX8 }

// IsEnabledX4VL returns true if the four-way permutation uses the AVX-512VL
// ternary-logic/rotate code path on YMM registers (24 rounds only).
func IsEnabledX4VL() bool { return enabledX4VL }

var (
	hasAVX512VL = cpu.X86.HasAVX512F && cpu.X86.HasAVX512VL
	enabledX8   = hasAVX512VL && simdAVX512
	enabledX4VL = hasAVX512VL && simdAVX512
)

// DisableAVX512 turns off both AVX-512 code paths: StateX8 then runs its
// eight states one after another and StateX4 uses the AVX2 permutation.
// Call it during start-up, before any permutation runs (it is not
// synchronised).
func DisableAVX512() {
	enabledX8 = false
	enabledX4VL = false
}

// Initialize returns the buffer on which the eight
// permutations act: a uint64 slice of length 200. The first permutation acts
// on {a[0], a[8], ..., a[192]}, the second on {a[1], a[9], ..., a[193]}, etc.
// The buffer is zero on a fresh StateX8.
func (s *StateX8) Initialize() []uint64 {
	rem := int(uintptr(unsafe.Pointer(&s.a[0]))&63) >> 3
	s.offset = 0
	if rem != 0 {
		s.offset = 8 - rem
	}
	return s.a[s.offset : s.offset+200]
}

// Permute performs the eight parallel Keccak-f[1600]s interleaved on the
// slice returned from Initialize().
func (s *StateX8) Permute() {
	if enabledX8 {
		permuteSIMDx8(s.a[s.offset:])
	} else {
		permuteScalarX8(s.a[s.offset:])
	}
}

func permuteScalarX8(a []uint64) {
	var buf [25]uint64
	for i := 0; i < 8; i++ {
		for j := 0; j < 25; j++ {
			buf[j] = a[8*j+i]
		}
		sha3.KeccakF1600(&buf, false)
		for j := 0; j < 25; j++ {
			a[8*j+i] = buf[j]
		}
	}
}

// OneBlockX8 runs eight single-block Keccak sponges (rate 136 bytes, 24
// rounds) in one AVX-512 pass and reports true, or does nothing and reports
// false without AVX-512 (IsEnabledX8). Input i is the ptrs[i]-addressed
// message whose first full[i] (<= 16) 64-bit little-endian words are read
// from memory; word full[i] is tail[i] (the remaining message bytes with
// the caller's pad byte already applied); later words are zero, except that
// the final 0x80 pad bit is XORed into word 16. Exactly 8*full[i] bytes are
// read at ptrs[i], which must stay alive during the call. out[8*k+i] is
// lane k (k < 4) of output i.
func OneBlockX8(out *[32]uint64, ptrs, full, tail *[8]uint64) bool {
	if !enabledX8 {
		return false
	}
	oneBlockX8(out, ptrs, full, tail)
	return true
}
