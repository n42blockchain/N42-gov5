package etl

import (
	"bytes"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/stretchr/testify/require"
)

// TestAppendSortableBufferMethods covers the Size/SizeLimit/Swap/Prealloc/Get
// paths of appendSortableBuffer that the higher-level Collector tests never
// exercise directly.
func TestAppendSortableBufferMethods(t *testing.T) {
	r := require.New(t)
	b := NewAppendBuffer(1 * datasize.MB)
	r.Equal(1*1024*1024, b.SizeLimit())
	r.Equal(0, b.Size())

	b.Put([]byte("b"), []byte("2"))
	b.Put([]byte("a"), []byte("1"))
	r.Equal(2, b.Len())
	r.True(b.Size() > 0)
	r.False(b.CheckFlushSize())

	b.Sort()
	r.Len(b.sortedBuf, 2)
	// After sort, "a" must come before "b".
	r.Equal([]byte("a"), b.sortedBuf[0].key)

	// Exercise Swap directly: swap entry 0 and 1, then swap back.
	first := b.sortedBuf[0]
	second := b.sortedBuf[1]
	b.Swap(0, 1)
	r.Equal(second, b.sortedBuf[0])
	r.Equal(first, b.sortedBuf[1])
	b.Swap(0, 1)

	keyBuf, valBuf := b.Get(0, nil, nil)
	r.Equal([]byte("a"), keyBuf)
	r.Equal([]byte("1"), valBuf)

	b.Prealloc(4, 16)
	r.NotNil(b.entries)
	r.Equal(0, b.Len())

	b.Reset()
	r.Equal(0, b.Size())
	r.Equal(0, b.Len())
}

func TestOldestEntrySortableBufferMethods(t *testing.T) {
	r := require.New(t)
	b := NewOldestEntryBuffer(1 * datasize.MB)
	r.Equal(1*1024*1024, b.SizeLimit())

	b.Put([]byte("x"), []byte("first"))
	// Second put with same key must be ignored (oldest wins).
	b.Put([]byte("x"), []byte("second"))
	r.Equal(1, b.Len())

	b.Put([]byte("y"), []byte("y-val"))
	r.Equal(2, b.Len())

	b.Sort()
	r.Len(b.sortedBuf, 2)
	r.Equal([]byte("x"), b.sortedBuf[0].key)
	r.Equal([]byte("first"), b.sortedBuf[0].value)

	first := b.sortedBuf[0]
	second := b.sortedBuf[1]
	b.Swap(0, 1)
	r.Equal(second, b.sortedBuf[0])
	r.Equal(first, b.sortedBuf[1])

	keyBuf, valBuf := b.Get(0, nil, nil)
	r.Equal(second.key, keyBuf)
	r.Equal(second.value, valBuf)

	b.Prealloc(2, 8)
	r.Equal(0, b.Len())

	var buf bytes.Buffer
	b.Put([]byte("z"), []byte("zz"))
	b.Sort()
	r.NoError(b.Write(&buf))
	r.True(buf.Len() > 0)

	b.Reset()
	r.Equal(0, b.Size())
}

func TestOldestMergedEntrySortableBufferMethods(t *testing.T) {
	r := require.New(t)
	merge := func(v1, v2 []byte) []byte { return append(append([]byte{}, v1...), v2...) }
	b := NewLatestMergedEntryMergedBuffer(1*datasize.MB, merge)
	r.Equal(1*1024*1024, b.SizeLimit())

	b.Put([]byte("k"), []byte("a"))
	b.Put([]byte("k"), []byte("b")) // merged -> "ab"
	r.Equal(1, b.Len())
	r.Equal([]byte("ab"), b.entries["k"])

	b.Put([]byte("m"), []byte("solo"))
	r.Equal(2, b.Len())

	b.Sort()
	r.Len(b.sortedBuf, 2)
	r.Equal([]byte("k"), b.sortedBuf[0].key)

	first := b.sortedBuf[0]
	second := b.sortedBuf[1]
	b.Swap(0, 1)
	r.Equal(second, b.sortedBuf[0])
	r.Equal(first, b.sortedBuf[1])
	b.Swap(0, 1)

	keyBuf, valBuf := b.Get(0, nil, nil)
	r.Equal([]byte("k"), keyBuf)
	r.Equal([]byte("ab"), valBuf)

	var buf bytes.Buffer
	r.NoError(b.Write(&buf))
	r.True(buf.Len() > 0)

	b.Prealloc(4, 16)
	r.Equal(0, b.Len())

	b.Reset()
	r.Equal(0, b.Size())

	r.Panics(func() {
		NewLatestMergedEntryMergedBuffer(1*datasize.MB, nil)
	})
}

func TestGetBufferByTypeAndGetTypeByBuffer(t *testing.T) {
	r := require.New(t)

	sortable := getBufferByType(SortableSliceBuffer, 1024, nil)
	r.IsType(&sortableBuffer{}, sortable)
	r.Equal(SortableSliceBuffer, getTypeByBuffer(sortable))

	appendBuf := getBufferByType(SortableAppendBuffer, 1024, nil)
	r.IsType(&appendSortableBuffer{}, appendBuf)
	r.Equal(SortableAppendBuffer, getTypeByBuffer(appendBuf))

	oldest := getBufferByType(SortableOldestAppearedBuffer, 1024, nil)
	r.IsType(&oldestEntrySortableBuffer{}, oldest)
	r.Equal(SortableOldestAppearedBuffer, getTypeByBuffer(oldest))

	merge := func(v1, v2 []byte) []byte { return v2 }
	prevMerged := NewLatestMergedEntryMergedBuffer(1024, merge)
	merged := getBufferByType(SortableMergeBuffer, 1024, prevMerged)
	r.IsType(&oldestMergedEntrySortableBuffer{}, merged)
	r.Equal(SortableMergeBuffer, getTypeByBuffer(merged))

	r.Panics(func() {
		getBufferByType(999, 1024, nil)
	})
	r.Panics(func() {
		getTypeByBuffer(nil)
	})
}
