// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The permutation in keccakf.go is golang.org/x/crypto/sha3's legacy Keccak-f
// (BSD license, see LICENSE-x-crypto). The sponge here is deliberately a
// concrete value type: calling Keccak-256 through hash.Hash makes the
// compiler assume Write/Sum retain their arguments, so every caller's input
// and output buffers escape to the heap. Erigon measured the same change at
// 1→0 allocations and −23% time for 32-byte inputs (#22489).

// Package keccak implements Keccak-256 (the pre-standard padding Ethereum
// uses) as a concrete sponge with no interfaces on the hot path.
package keccak

import (
	"encoding/binary"

	fastkeccak "github.com/erigontech/fastkeccak"
)

const (
	// Rate is the sponge rate of Keccak-256 in bytes.
	Rate = 136
	// Size is the digest size in bytes.
	Size   = 32
	dsbyte = 0x01 // legacy Keccak domain separation (SHA-3 uses 0x06)
)

// State is a Keccak-256 sponge. The zero value is ready to use. It satisfies
// hash.Hash and the Read-based KeccakState interface used across the code
// base, but callers on hot paths should hold it by value or pointer and call
// the methods directly.
type State struct {
	a         [25]uint64 // the permutation state
	buf       [Rate]byte // pending absorb bytes, or squeezed output
	n         int        // bytes in buf (absorbing) or consumed from buf (squeezing)
	squeezing bool
}

// New returns a fresh Keccak-256 state.
func New() *State { return new(State) }

// Reset clears the state for a new message.
func (s *State) Reset() {
	s.a = [25]uint64{}
	s.n = 0
	s.squeezing = false
}

// Size returns the digest size.
func (s *State) Size() int { return Size }

// BlockSize returns the sponge rate.
func (s *State) BlockSize() int { return Rate }

// absorbBlock XORs a full rate block into the state and permutes.
func (s *State) absorbBlock(b []byte) {
	for i := 0; i < Rate/8; i++ {
		s.a[i] ^= binary.LittleEndian.Uint64(b[8*i:])
	}
	keccakF1600(&s.a)
}

// Write absorbs p. Writing after a Read starts a new message, matching
// hash.Hash's Reset-less reuse expectations as little as x/crypto does:
// callers must Reset between messages.
func (s *State) Write(p []byte) (int, error) {
	if s.squeezing {
		panic("keccak: Write after Read")
	}
	n := len(p)
	if s.n > 0 {
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
		if s.n < Rate {
			return n, nil
		}
		s.absorbBlock(s.buf[:])
		s.n = 0
	}
	for len(p) >= Rate {
		s.absorbBlock(p[:Rate])
		p = p[Rate:]
	}
	s.n = copy(s.buf[:], p)
	return n, nil
}

// pad finishes the message and produces the first output block into buf.
func (s *State) pad() {
	for i := s.n; i < Rate; i++ {
		s.buf[i] = 0
	}
	s.buf[s.n] ^= dsbyte
	s.buf[Rate-1] ^= 0x80
	s.absorbBlock(s.buf[:])
	s.squeezing = true
	s.n = 0
	s.fillOutput()
}

func (s *State) fillOutput() {
	for i := 0; i < Rate/8; i++ {
		binary.LittleEndian.PutUint64(s.buf[8*i:], s.a[i])
	}
}

// Read squeezes len(out) bytes of digest. The first call pads the message.
func (s *State) Read(out []byte) (int, error) {
	if !s.squeezing {
		s.pad()
	}
	n := len(out)
	for len(out) > 0 {
		if s.n == Rate {
			keccakF1600(&s.a)
			s.fillOutput()
			s.n = 0
		}
		c := copy(out, s.buf[s.n:])
		s.n += c
		out = out[c:]
	}
	return n, nil
}

// Sum appends the digest of the message so far to in without disturbing the
// state (hash.Hash semantics).
func (s *State) Sum(in []byte) []byte {
	dup := *s
	var h [Size]byte
	dup.Read(h[:])
	return append(in, h[:]...)
}

// Sum256 returns the Keccak-256 digest of data. One-shot digests go through
// erigontech/fastkeccak, whose assembly permutation is ~23% faster than the
// portable one on this code base's targets; State keeps the portable sponge
// for streaming callers that squeeze more than one block.
func Sum256(data []byte) [Size]byte {
	return fastkeccak.Sum256(data)
}

// Sum256Into writes the Keccak-256 digest of the concatenation of the given
// slices into h.
func Sum256Into(h *[Size]byte, data ...[]byte) {
	if len(data) == 1 {
		*h = fastkeccak.Sum256(data[0])
		return
	}
	var d fastkeccak.Hasher
	for _, chunk := range data {
		d.Write(chunk)
	}
	*h = d.Sum256()
}
