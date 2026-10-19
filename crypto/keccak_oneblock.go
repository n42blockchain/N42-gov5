package crypto

import (
	"encoding/binary"
	"os"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/keccakf1600"
	"github.com/n42blockchain/N42/crypto/sha3"
)

// keccak256Rate is the Keccak-256 sponge rate in bytes (1600-2*256 bits).
const keccak256Rate = 136

// KeccakOneBlockEnabled gates the single-block Keccak-256 fast path
// (N42_KECCAK_ONEBLOCK, default on; "0" restores the pooled sponge for
// every input). Read once at start-up.
var KeccakOneBlockEnabled = os.Getenv("N42_KECCAK_ONEBLOCK") != "0"

// Keccak256OneBlock returns Keccak256Hash(data) computed with a single
// Keccak-f[1600] permutation on a stack state, and true, when data is
// shorter than the 136-byte rate: the message and its legacy Keccak padding
// (0x01 ... 0x80) then fill exactly one rate block, so the sponge reduces to
// absorb, permute once, squeeze four lanes. No pooled hasher, no interface
// calls, and data does not escape (callers may pass a stack buffer). For a
// longer input it returns false and computes nothing. It ignores
// N42_KECCAK_ONEBLOCK; callers check KeccakOneBlockEnabled.
func Keccak256OneBlock(data []byte) (h types.Hash, ok bool) {
	if len(data) >= keccak256Rate {
		return h, false
	}
	var blk [keccak256Rate]byte
	n := copy(blk[:], data)
	blk[n] ^= 0x01
	blk[keccak256Rate-1] ^= 0x80
	var a [25]uint64
	for i := 0; i < keccak256Rate/8; i++ {
		a[i] = binary.LittleEndian.Uint64(blk[i*8:])
	}
	sha3.KeccakF1600(&a, false)
	binary.LittleEndian.PutUint64(h[0:], a[0])
	binary.LittleEndian.PutUint64(h[8:], a[1])
	binary.LittleEndian.PutUint64(h[16:], a[2])
	binary.LittleEndian.PutUint64(h[24:], a[3])
	return h, true
}

// Keccak256HashOne is Keccak256Hash of a single input slice, taking the
// one-permutation path when the input fits in one rate block (transfer
// signing payloads, 64-byte public keys, transfer encodings) and the flag
// is on. Bit-identical to Keccak256Hash(data).
func Keccak256HashOne(data []byte) types.Hash {
	if KeccakOneBlockEnabled {
		if h, ok := Keccak256OneBlock(data); ok {
			return h
		}
	}
	return Keccak256Hash(data)
}

// KeccakX4Enabled reports whether Keccak256x4OneBlock runs the four
// permutations in one AVX2 pass (otherwise it runs them one after another,
// which is still correct but no faster than four single calls).
var KeccakX4Enabled = keccakf1600.IsEnabledX4()

// Keccak256x4OneBlock computes out[i] = Keccak256Hash(in[i]) for four inputs
// that each fit in one rate block (len < 136), interleaving the four
// single-block states into one four-way Keccak-f[1600] permutation. With AVX2
// that permutation costs about 1.6x a single scalar one, so each hash costs
// ~40% of a lone call. If any input is 136 bytes or longer it returns false
// and computes nothing. The inputs do not escape. It ignores
// N42_KECCAK_ONEBLOCK; callers check KeccakOneBlockEnabled.
func Keccak256x4OneBlock(in *[4][]byte, out *[4]types.Hash) bool {
	for i := 0; i < 4; i++ {
		if len(in[i]) >= keccak256Rate {
			return false
		}
	}
	var st keccakf1600.StateX4
	a := st.Initialize(false)
	var blk [keccak256Rate]byte
	for i := 0; i < 4; i++ {
		blk = [keccak256Rate]byte{}
		n := copy(blk[:], in[i])
		blk[n] ^= 0x01
		blk[keccak256Rate-1] ^= 0x80
		for j := 0; j < keccak256Rate/8; j++ {
			a[4*j+i] = binary.LittleEndian.Uint64(blk[j*8:])
		}
	}
	st.Permute()
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint64(out[i][0:], a[i])
		binary.LittleEndian.PutUint64(out[i][8:], a[4+i])
		binary.LittleEndian.PutUint64(out[i][16:], a[8+i])
		binary.LittleEndian.PutUint64(out[i][24:], a[12+i])
	}
	return true
}
