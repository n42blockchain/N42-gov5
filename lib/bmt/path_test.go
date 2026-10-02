package bmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathEmpty(t *testing.T) {
	r := require.New(t)
	p := EmptyPath()
	r.Equal(0, p.BitLen)
	r.Nil(p.Bytes)
}

func TestPathFromKeyHash(t *testing.T) {
	r := require.New(t)
	var h Hash
	for i := range h {
		h[i] = byte(i)
	}
	p := FromKeyHash(h)
	r.Equal(256, p.BitLen)
	r.Equal(h[:], p.Bytes)
}

func TestPathBitSequence(t *testing.T) {
	r := require.New(t)
	p := EmptyPath()
	p = p.Append(1)
	p = p.Append(0)
	p = p.Append(1)
	r.Equal(byte(1), p.Bit(0))
	r.Equal(byte(0), p.Bit(1))
	r.Equal(byte(1), p.Bit(2))
	// Out of range returns 0.
	r.Equal(byte(0), p.Bit(3))
	r.Equal(byte(0), p.Bit(100))
}

func TestPathPrefix(t *testing.T) {
	r := require.New(t)
	var h Hash
	h[0] = 0b10110000
	p := FromKeyHash(h)

	// n >= BitLen returns the same path.
	full := p.Prefix(300)
	r.Equal(p, full)

	pre4 := p.Prefix(4)
	r.Equal(4, pre4.BitLen)
	r.Equal(byte(0b10110000), pre4.Bytes[0])

	pre3 := p.Prefix(3)
	r.Equal(3, pre3.BitLen)
	// trailing bits masked off: only top 3 bits (101) kept.
	r.Equal(byte(0b10100000), pre3.Bytes[0])

	pre0 := p.Prefix(0)
	r.Equal(0, pre0.BitLen)
	r.Len(pre0.Bytes, 0)
}

func TestPathAppend(t *testing.T) {
	r := require.New(t)
	p := EmptyPath()
	for i := 0; i < 10; i++ {
		bit := byte(i % 2)
		p = p.Append(bit)
		r.Equal(i+1, p.BitLen)
		r.Equal(bit, p.Bit(i))
	}
}

func TestPathSibling(t *testing.T) {
	r := require.New(t)
	// Sibling of an empty path is itself (no bits to flip).
	empty := EmptyPath()
	r.Equal(empty, empty.Sibling())

	p := EmptyPath().Append(1).Append(0).Append(1)
	sib := p.Sibling()
	r.Equal(p.BitLen, sib.BitLen)
	r.Equal(byte(0), sib.Bit(2)) // last bit flipped from 1 -> 0
	r.Equal(p.Bit(0), sib.Bit(0))
	r.Equal(p.Bit(1), sib.Bit(1))

	// Flipping twice returns to the original.
	r.Equal(p, sib.Sibling())
}

func TestPathEqual(t *testing.T) {
	r := require.New(t)
	a := EmptyPath().Append(1).Append(0)
	b := EmptyPath().Append(1).Append(0)
	c := EmptyPath().Append(1).Append(1)
	d := EmptyPath().Append(1)

	r.True(a.Equal(b))
	r.False(a.Equal(c))
	r.False(a.Equal(d))
}

func TestPathString(t *testing.T) {
	r := require.New(t)
	p := EmptyPath().Append(1).Append(0).Append(1)
	s := p.String()
	r.Equal(byte(p.BitLen), s[0])
	r.Equal(string(p.Bytes), s[1:])

	// Two distinct paths must stringify to distinct keys (used for map lookups).
	q := EmptyPath().Append(1).Append(1)
	r.NotEqual(p.String(), q.String())
}
