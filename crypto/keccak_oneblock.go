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

// keccak256OneBlock hashes data of fewer than keccak256Rate bytes with a
// single Keccak-f[1600] permutation on a stack state: the message and its
// legacy Keccak padding (0x01 ... 0x80) fill exactly one rate block, so the
// sponge reduces to absorb, permute once, squeeze four lanes. No pooled
// hasher, no interface calls, no buffer copies. The caller guarantees
// len(data) < keccak256Rate.
func keccak256OneBlock(data []byte) (h types.Hash) {
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
	return h
}

// Keccak256HashOne is Keccak256Hash of a single input slice, taking the
// one-permutation path when the input fits in one rate block (transfer
// signing payloads, 64-byte public keys, transfer encodings) and the flag
// is on. Bit-identical to Keccak256Hash(data).
func Keccak256HashOne(data []byte) types.Hash {
	if len(data) < keccak256Rate && KeccakOneBlockEnabled {
		return keccak256OneBlock(data)
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
// ~40% of a lone call. Inputs of 136 bytes or more fall back to
// Keccak256Hash for that lane only (its lane is then permuted on garbage and
// discarded). Bit-identical to four Keccak256Hash calls.
func Keccak256x4OneBlock(in *[4][]byte, out *[4]types.Hash) {
	var st keccakf1600.StateX4
	a := st.Initialize(false)
	var blk [keccak256Rate]byte
	for i := 0; i < 4; i++ {
		d := in[i]
		if len(d) >= keccak256Rate {
			continue
		}
		blk = [keccak256Rate]byte{}
		n := copy(blk[:], d)
		blk[n] ^= 0x01
		blk[keccak256Rate-1] ^= 0x80
		for j := 0; j < keccak256Rate/8; j++ {
			a[4*j+i] = binary.LittleEndian.Uint64(blk[j*8:])
		}
	}
	st.Permute()
	for i := 0; i < 4; i++ {
		if len(in[i]) >= keccak256Rate {
			out[i] = Keccak256Hash(in[i])
			continue
		}
		binary.LittleEndian.PutUint64(out[i][0:], a[i])
		binary.LittleEndian.PutUint64(out[i][8:], a[4+i])
		binary.LittleEndian.PutUint64(out[i][16:], a[8+i])
		binary.LittleEndian.PutUint64(out[i][24:], a[12+i])
	}
}
