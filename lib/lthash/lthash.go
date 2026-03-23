// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package lthash implements a lattice-based homomorphic hash function
// inspired by Solana's SIMD-215 Accounts Lattice Hash.
//
// LtHash has the homomorphic property: LtHash(A ∪ B) = LtHash(A) ⊕ LtHash(B).
// This enables O(k) incremental state digest computation per block (k = changed
// accounts/slots), compared to O(k * depth) for Merkle tree traversal.
//
// The digest is 2048 bytes (16384 bits), computed using BLAKE3 in XOF
// (eXtendable Output Function) mode. The 2048-byte output provides
// 128-bit collision security per output block.
//
// The compact summary (for block headers) is BLAKE3(digest[:]) → 32 bytes.
package lthash

import (
	"encoding/binary"
	"unsafe"

	"lukechampine.com/blake3"
)

// DigestSize is the size of the LtHash digest in bytes.
// 2048 bytes = 16384 bits provides 128-bit collision security.
const DigestSize = 2048

// words is the number of uint64 words in a digest (2048 / 8 = 256).
const words = DigestSize / 8

// Digest is a 2048-byte lattice hash accumulator.
// The zero value is a valid empty digest.
type Digest [DigestSize]byte

// New returns a pointer to a zero-initialized digest.
func New() *Digest {
	return &Digest{}
}

// Add XORs the BLAKE3 XOF expansion of data into the digest.
// This is equivalent to inserting an element into the multiset.
func (d *Digest) Add(data []byte) {
	var expanded Digest
	expandBlake3(data, &expanded)
	xorDigest(d, &expanded)
}

// Remove XORs the BLAKE3 XOF expansion of data out of the digest.
// This is equivalent to removing an element from the multiset.
// Since XOR is self-inverse, Remove is identical to Add.
func (d *Digest) Remove(data []byte) {
	d.Add(data) // XOR is self-inverse
}

// Update atomically removes oldData and adds newData in a single pass.
// More efficient than separate Remove + Add calls (one fewer XOF expansion
// and one loop instead of two).
func (d *Digest) Update(oldData, newData []byte) {
	var oldExp, newExp Digest
	expandBlake3(oldData, &oldExp)
	expandBlake3(newData, &newExp)

	// XOR all three in a single pass using uint64 arithmetic.
	dw := (*[words]uint64)(unsafe.Pointer(&d[0]))
	ow := (*[words]uint64)(unsafe.Pointer(&oldExp[0]))
	nw := (*[words]uint64)(unsafe.Pointer(&newExp[0]))
	for i := 0; i < words; i++ {
		dw[i] ^= ow[i] ^ nw[i]
	}
}

// Sum compresses the 2048-byte digest into a 32-byte summary using BLAKE3.
// This compact form is suitable for inclusion in block headers.
func (d *Digest) Sum() [32]byte {
	return blake3.Sum256(d[:])
}

// Clone returns a deep copy of the digest.
func (d *Digest) Clone() *Digest {
	cp := new(Digest)
	copy(cp[:], d[:])
	return cp
}

// IsZero returns true if the digest is all zeros (empty multiset).
func (d *Digest) IsZero() bool {
	dw := (*[words]uint64)(unsafe.Pointer(&d[0]))
	for i := 0; i < words; i++ {
		if dw[i] != 0 {
			return false
		}
	}
	return true
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (d *Digest) MarshalBinary() ([]byte, error) {
	buf := make([]byte, DigestSize)
	copy(buf, d[:])
	return buf, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler.
func (d *Digest) UnmarshalBinary(data []byte) error {
	if len(data) < DigestSize {
		return errShortDigest
	}
	copy(d[:], data[:DigestSize])
	return nil
}

// Len returns the digest size in bytes (always 2048).
func (d *Digest) Len() int {
	return DigestSize
}

// expandBlake3 computes BLAKE3 XOF output of exactly DigestSize bytes.
func expandBlake3(data []byte, out *Digest) {
	h := blake3.New(0, nil)
	h.Write(data)
	xof := h.XOF()
	xof.Read(out[:])
}

// xorDigest XORs src into dst using uint64 arithmetic for performance.
func xorDigest(dst, src *Digest) {
	dw := (*[words]uint64)(unsafe.Pointer(&dst[0]))
	sw := (*[words]uint64)(unsafe.Pointer(&src[0]))
	for i := 0; i < words; i++ {
		dw[i] ^= sw[i]
	}
}

// errShortDigest is returned when UnmarshalBinary receives too few bytes.
var errShortDigest = &digestError{"lthash: data too short for digest"}

type digestError struct{ msg string }

func (e *digestError) Error() string { return e.msg }

// EncodeElement builds a domain-separated encoding for LtHash input.
// Format: [tag:1][keyHash:32][value:n]
// tag 'A' (0x41) for accounts, 'S' (0x53) for storage slots.
func EncodeElement(tag byte, keyHash [32]byte, value []byte) []byte {
	buf := make([]byte, 1+32+len(value))
	buf[0] = tag
	copy(buf[1:33], keyHash[:])
	copy(buf[33:], value)
	return buf
}

// EncodeUint64Element is a convenience for encoding a uint64 value.
func EncodeUint64Element(tag byte, keyHash [32]byte, val uint64) []byte {
	var vbuf [8]byte
	binary.BigEndian.PutUint64(vbuf[:], val)
	return EncodeElement(tag, keyHash, vbuf[:])
}
