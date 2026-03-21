// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package ed2k implements eDonkey2000 hash computation and ed2k link handling.
//
// The ed2k hash uses MD4 on 9500 KiB (9728000 byte) chunks. Each chunk is
// independently hashed with MD4, then the chunk hashes are concatenated and
// hashed again with MD4 to produce the final file hash. For files that fit
// in a single chunk, the hash of that single chunk is the file hash.
package ed2k

import (
	"encoding/binary"
	"hash"
	"math/bits"
)

// md4digest implements hash.Hash for the MD4 algorithm.
// MD4 is cryptographically broken but required by the ed2k protocol.
type md4digest struct {
	s   [4]uint32
	x   [64]byte
	nx  int
	len uint64
}

const md4BlockSize = 64
const md4Size = 16

// NewMD4 returns a new MD4 hash.
func NewMD4() hash.Hash {
	d := new(md4digest)
	d.Reset()
	return d
}

func (d *md4digest) Reset() {
	d.s[0] = 0x67452301
	d.s[1] = 0xEFCDAB89
	d.s[2] = 0x98BADCFE
	d.s[3] = 0x10325476
	d.nx = 0
	d.len = 0
}

func (d *md4digest) Size() int      { return md4Size }
func (d *md4digest) BlockSize() int { return md4BlockSize }

func (d *md4digest) Write(p []byte) (nn int, err error) {
	nn = len(p)
	d.len += uint64(nn)

	if d.nx > 0 {
		n := copy(d.x[d.nx:], p)
		d.nx += n
		if d.nx == md4BlockSize {
			md4Block(d, d.x[:])
			d.nx = 0
		}
		p = p[n:]
	}
	for len(p) >= md4BlockSize {
		md4Block(d, p[:md4BlockSize])
		p = p[md4BlockSize:]
	}
	if len(p) > 0 {
		d.nx = copy(d.x[:], p)
	}
	return
}

func (d *md4digest) Sum(in []byte) []byte {
	d0 := *d
	hash := d0.checkSum()
	return append(in, hash[:]...)
}

func (d *md4digest) checkSum() [md4Size]byte {
	// Padding: append 1 bit, then zeros, then length in bits as 64-bit LE.
	length := d.len
	var tmp [64]byte
	tmp[0] = 0x80
	if length%64 < 56 {
		d.Write(tmp[0 : 56-length%64])
	} else {
		d.Write(tmp[0 : 64+56-length%64])
	}

	// Length in bits.
	length <<= 3
	binary.LittleEndian.PutUint64(tmp[:8], length)
	d.Write(tmp[:8])

	if d.nx != 0 {
		panic("md4: internal error")
	}

	var digest [md4Size]byte
	binary.LittleEndian.PutUint32(digest[0:], d.s[0])
	binary.LittleEndian.PutUint32(digest[4:], d.s[1])
	binary.LittleEndian.PutUint32(digest[8:], d.s[2])
	binary.LittleEndian.PutUint32(digest[12:], d.s[3])
	return digest
}

func md4Block(d *md4digest, p []byte) {
	var x [16]uint32
	for i := 0; i < 16; i++ {
		x[i] = binary.LittleEndian.Uint32(p[4*i:])
	}

	a, b, c, dd := d.s[0], d.s[1], d.s[2], d.s[3]

	// Round 1
	f := func(x, y, z uint32) uint32 { return (x & y) | (^x & z) }
	r1 := [16]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	s1 := [4]uint{3, 7, 11, 19}
	for i := 0; i < 16; i++ {
		val := a + f(b, c, dd) + x[r1[i]]
		a, b, c, dd = dd, bits.RotateLeft32(val, int(s1[i%4])), b, c
	}

	// Round 2
	g := func(x, y, z uint32) uint32 { return (x & y) | (x & z) | (y & z) }
	r2 := [16]int{0, 4, 8, 12, 1, 5, 9, 13, 2, 6, 10, 14, 3, 7, 11, 15}
	s2 := [4]uint{3, 5, 9, 13}
	for i := 0; i < 16; i++ {
		val := a + g(b, c, dd) + x[r2[i]] + 0x5A827999
		a, b, c, dd = dd, bits.RotateLeft32(val, int(s2[i%4])), b, c
	}

	// Round 3
	h := func(x, y, z uint32) uint32 { return x ^ y ^ z }
	r3 := [16]int{0, 8, 4, 12, 2, 10, 6, 14, 1, 9, 5, 13, 3, 11, 7, 15}
	s3 := [4]uint{3, 9, 11, 15}
	for i := 0; i < 16; i++ {
		val := a + h(b, c, dd) + x[r3[i]] + 0x6ED9EBA1
		a, b, c, dd = dd, bits.RotateLeft32(val, int(s3[i%4])), b, c
	}

	d.s[0] += a
	d.s[1] += b
	d.s[2] += c
	d.s[3] += dd
}
