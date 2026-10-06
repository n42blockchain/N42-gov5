/*
   Copyright 2022 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package bitmapdb_test

import (
	"bytes"
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/bitmapdb"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// g36BitmapTable picks a bucket present in the default table config that is
// suitable for arbitrary key/value storage used by the bitmapdb helpers.
const g36BitmapTable = kv.LogAddressIndex

func TestG36PoolRoundtrip(t *testing.T) {
	a := bitmapdb.NewBitmap()
	require.NotNil(t, a)
	a.Add(1)
	a.Add(2)
	bitmapdb.ReturnToPool(a)
	// nil must be a no-op, not a panic
	bitmapdb.ReturnToPool(nil)

	b := bitmapdb.NewBitmap64()
	require.NotNil(t, b)
	b.Add(10)
	bitmapdb.ReturnToPool64(b)
	bitmapdb.ReturnToPool64(nil)
}

func TestG36CutLeft64AndWalkChunks64(t *testing.T) {
	bm := roaring64.New()
	for i := uint64(0); i < 5000; i++ {
		bm.Add(i)
	}

	// small limit forces multiple chunks
	var chunks []*roaring64.Bitmap
	err := bitmapdb.WalkChunks64(bm, 64, func(chunk *roaring64.Bitmap, isLast bool) error {
		c := roaring64.New()
		c.Or(chunk)
		chunks = append(chunks, c)
		return nil
	})
	require.NoError(t, err)
	require.True(t, len(chunks) > 1)

	// reassembled union must equal original range
	union := roaring64.New()
	for _, c := range chunks {
		union.Or(c)
	}
	require.Equal(t, uint64(5000), union.GetCardinality())
	require.True(t, union.Contains(0))
	require.True(t, union.Contains(4999))

	// empty bitmap: no callback invocations
	empty := roaring64.New()
	calls := 0
	err = bitmapdb.WalkChunks64(empty, 64, func(*roaring64.Bitmap, bool) error {
		calls++
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 0, calls)

	// single fits-in-one-chunk bitmap
	small := roaring64.New()
	small.AddRange(0, 10)
	var gotLast bool
	err = bitmapdb.WalkChunks64(small, 1<<20, func(chunk *roaring64.Bitmap, isLast bool) error {
		gotLast = isLast
		require.Equal(t, uint64(10), chunk.GetCardinality())
		return nil
	})
	require.NoError(t, err)
	require.True(t, gotLast)
}

func TestG36WalkChunkWithKeys64(t *testing.T) {
	bm := roaring64.New()
	bm.AddRange(0, 300)

	key := []byte("prefix")
	var keys [][]byte
	err := bitmapdb.WalkChunkWithKeys64(key, bm, 64, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
		kk := make([]byte, len(chunkKey))
		copy(kk, chunkKey)
		keys = append(keys, kk)
		return nil
	})
	require.NoError(t, err)
	require.True(t, len(keys) >= 1)
	for i, k := range keys {
		require.True(t, len(k) >= len(key))
		require.Equal(t, key, k[:len(key)])
		if i == len(keys)-1 {
			// last chunk key is suffixed with max uint64 marker
			continue
		}
	}
}

func TestG36TruncateRangeAndGet64(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	bucket := g36BitmapTable
	key := []byte("acct64")

	bm := roaring64.New()
	bm.AddRange(0, 1000)

	err := bitmapdb.WalkChunkWithKeys64(key, bm, 64, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
		buf, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(bucket, chunkKey, buf)
	})
	require.NoError(t, err)

	got, err := bitmapdb.Get64(tx, bucket, key, 0, bitmapdb.MaxUint32)
	require.NoError(t, err)
	require.Equal(t, uint64(1000), got.GetCardinality())

	require.NoError(t, bitmapdb.TruncateRange64(tx, bucket, key, 500))

	after, err := bitmapdb.Get64(tx, bucket, key, 0, bitmapdb.MaxUint32)
	require.NoError(t, err)
	require.Equal(t, uint64(500), after.GetCardinality())
	require.False(t, after.Contains(500))
	require.True(t, after.Contains(499))
}

func TestG36TruncateRangeAndGet32(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	bucket := g36BitmapTable
	key := []byte("acct32")

	bm := roaring.New()
	bm.AddRange(0, 1000)

	err := bitmapdb.WalkChunkWithKeys(key, bm, 64, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		w := &bytes.Buffer{}
		if _, err := chunk.WriteTo(w); err != nil {
			return err
		}
		return tx.Put(bucket, chunkKey, w.Bytes())
	})
	require.NoError(t, err)

	got, err := bitmapdb.Get(tx, bucket, key, 0, bitmapdb.MaxUint32)
	require.NoError(t, err)
	require.Equal(t, uint64(1000), got.GetCardinality())

	require.NoError(t, bitmapdb.TruncateRange(tx, bucket, key, 200))

	after, err := bitmapdb.Get(tx, bucket, key, 0, bitmapdb.MaxUint32)
	require.NoError(t, err)
	require.Equal(t, uint64(200), after.GetCardinality())
}

func TestG36GetEmptyKey(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	got, err := bitmapdb.Get(tx, g36BitmapTable, []byte("nope"), 0, bitmapdb.MaxUint32)
	require.NoError(t, err)
	require.Equal(t, uint64(0), got.GetCardinality())
}

func TestG36SeekInBitmap64(t *testing.T) {
	bm := roaring64.New()
	bm.Add(5)
	bm.Add(10)
	bm.Add(20)

	found, ok := bitmapdb.SeekInBitmap64(bm, 11)
	require.True(t, ok)
	require.Equal(t, uint64(20), found)

	found, ok = bitmapdb.SeekInBitmap64(bm, 0)
	require.True(t, ok)
	require.Equal(t, uint64(5), found)

	_, ok = bitmapdb.SeekInBitmap64(bm, 21)
	require.False(t, ok)
}

func TestG36SeekInBitmap32(t *testing.T) {
	bm := roaring.New()
	bm.Add(5)
	bm.Add(10)

	found, ok := bitmapdb.SeekInBitmap(bm, 6)
	require.True(t, ok)
	require.Equal(t, uint32(10), found)

	_, ok = bitmapdb.SeekInBitmap(bm, 100)
	require.False(t, ok)
}

func TestG36WalkAndBytesmask(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	bucket := g36BitmapTable

	require.NoError(t, tx.Put(bucket, []byte{0x01, 0x02}, []byte("a")))
	require.NoError(t, tx.Put(bucket, []byte{0x01, 0x03}, []byte("b")))
	require.NoError(t, tx.Put(bucket, []byte{0x02, 0x00}, []byte("c")))

	c, err := tx.Cursor(bucket)
	require.NoError(t, err)
	defer c.Close()

	var seen [][]byte
	err = bitmapdb.Walk(c, []byte{0x01, 0x00}, 8, func(k, v []byte) (bool, error) {
		kk := make([]byte, len(k))
		copy(kk, k)
		seen = append(seen, kk)
		return true, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, len(seen))

	fixedbytes, mask := bitmapdb.Bytesmask(12)
	require.Equal(t, 2, fixedbytes)
	require.Equal(t, byte(0xf0), mask)

	fixedbytes, mask = bitmapdb.Bytesmask(0)
	require.Equal(t, 0, fixedbytes)
	require.Equal(t, byte(0xff), mask)
}

func TestG36ToIter(t *testing.T) {
	bm := roaring64.New()
	bm.Add(1)
	bm.Add(2)
	bm.Add(3)

	it := bitmapdb.ToIter(bm.Iterator())
	var got []uint64
	for it.HasNext() {
		v, err := it.Next()
		require.NoError(t, err)
		got = append(got, v)
	}
	require.Equal(t, []uint64{1, 2, 3}, got)
	require.False(t, it.HasNext())
}
