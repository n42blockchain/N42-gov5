package jmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNibblePathEqual(t *testing.T) {
	r := require.New(t)
	a := NewNibblePathFromSlice([]byte{1, 2, 3})
	b := NewNibblePathFromSlice([]byte{1, 2, 3})
	c := NewNibblePathFromSlice([]byte{1, 2, 4})
	d := NewNibblePathFromSlice([]byte{1, 2})

	r.True(a.Equal(b))
	r.False(a.Equal(c))
	r.False(a.Equal(d))
}

func TestNibblePathSuffix(t *testing.T) {
	r := require.New(t)
	p := NewNibblePathFromSlice([]byte{1, 2, 3, 4, 5})
	suf := p.Suffix(2)
	r.Equal(3, suf.Len())
	r.Equal([]byte{3, 4, 5}, suf.Nibbles())

	full := p.Suffix(0)
	r.True(full.Equal(p))

	empty := p.Suffix(5)
	r.Equal(0, empty.Len())
}

func TestNibblePathAppend(t *testing.T) {
	r := require.New(t)
	p := NewNibblePathFromSlice([]byte{1, 2})
	appended := p.Append(0xF)
	r.Equal(3, appended.Len())
	r.Equal([]byte{1, 2, 0xF}, appended.Nibbles())
	// Appending masks to low nibble.
	appended2 := p.Append(0xFF)
	r.Equal(byte(0xF), appended2.At(2))

	// Original path is unmodified.
	r.Equal(2, p.Len())
}

func TestHashTwo(t *testing.T) {
	r := require.New(t)
	h := Blake3Hasher{}
	var a, b Hash
	a[0] = 1
	b[0] = 2
	h1 := h.HashTwo(a, b)
	h2 := h.HashTwo(a, b)
	r.Equal(h1, h2)

	h3 := h.HashTwo(b, a)
	r.NotEqual(h1, h3, "HashTwo must be order-sensitive")
}

func TestNodeHash(t *testing.T) {
	r := require.New(t)
	h := DefaultHasher()
	var keyHash Hash
	keyHash[0] = 9
	leaf := NewLeafNode(keyHash, []byte("value"), h)
	nh := NodeHash(leaf, h)
	r.NotEqual(EmptyHash, nh)

	// NodeHash must be deterministic for identical encoded content.
	nh2 := NodeHash(leaf, h)
	r.Equal(nh, nh2)

	// Different content -> different hash.
	leaf2 := NewLeafNode(keyHash, []byte("value2"), h)
	r.NotEqual(nh, NodeHash(leaf2, h))
}

func TestMemStoreHasAndForEach(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	var h1, h2 Hash
	h1[0] = 1
	h2[0] = 2

	ok, err := store.Has(h1)
	r.NoError(err)
	r.False(ok)

	r.NoError(store.Put(h1, []byte("a")))
	r.NoError(store.Put(h2, []byte("b")))

	ok, err = store.Has(h1)
	r.NoError(err)
	r.True(ok)

	seen := map[Hash][]byte{}
	store.ForEach(func(hash Hash, data []byte) {
		cp := make([]byte, len(data))
		copy(cp, data)
		seen[hash] = cp
	})
	r.Len(seen, 2)
	r.Equal([]byte("a"), seen[h1])
	r.Equal([]byte("b"), seen[h2])

	r.NoError(store.Delete(h1))
	ok, err = store.Has(h1)
	r.NoError(err)
	r.False(ok)
}
