// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Poseidon hashing over the BN254 scalar field, replacing the earlier
// Keccak256-based approximation. Built on the audited gnark-crypto
// Poseidon2 permutation (width 2, 6 full / 50 partial rounds) used as a
// 2-to-1 compression with feed-forward. Inputs are absorbed as a
// length-prefixed stream of 31-byte chunks so the mapping from byte
// tuples to field-element streams is injective: every chunk is below
// 2^248 < r and every input's length is absorbed before its chunks.

package rln

import (
	"encoding/binary"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/poseidon2"
	"golang.org/x/crypto/sha3"
)

// poseidonChunkSize is the number of input bytes absorbed per field element.
// 31 bytes keep every chunk below 2^248 < r, so SetBytesCanonical never fails.
const poseidonChunkSize = 31

// poseidonDomain seeds the initial state so this construction is
// domain-separated from other Poseidon2 users.
const poseidonDomain = "n42-rln-poseidon2-v1"

var (
	poseidonOnce sync.Once
	poseidonPerm *poseidon2.Permutation
	poseidonIV   [32]byte
)

func poseidonSetup() {
	poseidonOnce.Do(func() {
		poseidonPerm = poseidon2.NewDefaultPermutation()
		// Reduce the domain tag digest into the field so the IV is a
		// canonical element with no known preimage structure.
		seed := sha3.Sum256([]byte(poseidonDomain))
		var iv fr.Element
		iv.SetBytes(seed[:])
		copy(poseidonIV[:], iv.Marshal())
	})
}

// PoseidonHash hashes the given byte slices into a canonical BN254 scalar
// field element, encoded as 32 big-endian bytes.
func PoseidonHash(data ...[]byte) [32]byte {
	poseidonSetup()

	state := poseidonIV[:]
	var block [32]byte
	absorb := func() {
		next, err := poseidonPerm.Compress(state, block[:])
		if err != nil {
			// Unreachable: the running state is a canonical field element
			// and every absorbed block is below 2^248.
			panic("rln: poseidon compress: " + err.Error())
		}
		state = next
	}

	for _, d := range data {
		clear(block[:])
		binary.BigEndian.PutUint64(block[24:], uint64(len(d)))
		absorb()
		for off := 0; off < len(d); off += poseidonChunkSize {
			end := min(off+poseidonChunkSize, len(d))
			clear(block[:])
			copy(block[32-(end-off):], d[off:end])
			absorb()
		}
	}

	var out [32]byte
	copy(out[:], state)
	return out
}
