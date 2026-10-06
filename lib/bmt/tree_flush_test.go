package bmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemStoreTotalBytes(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	r.Equal(int64(0), store.TotalBytes())

	tree := New(store)
	var k1 Hash
	k1[0] = 1
	r.NoError(tree.Put(k1, []byte("value-one")))
	r.NoError(tree.FlushTo(store))

	r.True(store.TotalBytes() > 0)
	wantTotal := int64(store.Len() * 32)
	for h := range store.nodes {
		v, err := store.Get(h)
		r.NoError(err)
		wantTotal += int64(len(v))
	}
	r.Equal(wantTotal, store.TotalBytes())
}

func TestTreeDirtyLen(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	r.Equal(0, tree.DirtyLen())

	var k Hash
	k[0] = 0xaa
	r.NoError(tree.Put(k, []byte("v")))
	r.True(tree.DirtyLen() > 0)

	r.NoError(tree.FlushTo(store))
	r.Equal(0, tree.DirtyLen())
}

func TestPruneDirtyRemovesUnreachable(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)

	// Insert several keys without ever flushing, then overwrite one key so
	// the old internal path nodes become unreachable garbage in `dirty`.
	keys := make([]Hash, 0, 8)
	for i := 0; i < 8; i++ {
		var k Hash
		k[0] = byte(i)
		keys = append(keys, k)
		r.NoError(tree.Put(k, []byte{byte(i)}))
	}
	beforePrune := tree.DirtyLen()
	r.True(beforePrune > 0)

	// Overwrite the first key many times to generate garbage internal nodes
	// along its path (structural sharing means old path nodes are orphaned).
	for i := 0; i < 20; i++ {
		r.NoError(tree.Put(keys[0], []byte{byte(i), byte(i + 1), byte(i + 2)}))
	}

	afterOverwrites := tree.DirtyLen()
	tree.PruneDirty()
	afterPrune := tree.DirtyLen()
	r.True(afterPrune <= afterOverwrites)

	// All original keys must still resolve correctly after pruning garbage.
	for i := 1; i < 8; i++ {
		v, err := tree.Get(keys[i])
		r.NoError(err)
		r.Equal([]byte{byte(i)}, v)
	}

	// PruneDirty on an empty tree / empty dirty map must be a safe no-op.
	empty := New(store)
	empty.PruneDirty()
	r.Equal(0, empty.DirtyLen())
}

func TestPruneDirtySkipsWhenMostlyLive(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	var k Hash
	k[0] = 1
	r.NoError(tree.Put(k, []byte("v")))
	before := tree.DirtyLen()
	// Nearly everything is reachable -> PruneDirty should decide the walk
	// isn't worth it and leave the dirty map untouched.
	tree.PruneDirty()
	r.Equal(before, tree.DirtyLen())
}

// fakeCollector implements ETLCollector for FlushToCollector tests without
// pulling in the real etl package.
type fakeCollector struct {
	keys   [][]byte
	values [][]byte
}

func (f *fakeCollector) Collect(k, v []byte) error {
	kc := append([]byte(nil), k...)
	vc := append([]byte(nil), v...)
	f.keys = append(f.keys, kc)
	f.values = append(f.values, vc)
	return nil
}

func TestFlushToCollector(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	var k Hash
	k[0] = 7
	r.NoError(tree.Put(k, []byte("payload")))
	dirtyBefore := tree.DirtyLen()
	r.True(dirtyBefore > 0)

	collector := &fakeCollector{}
	r.NoError(tree.FlushToCollector(collector))
	r.Equal(dirtyBefore, len(collector.keys))
	r.Equal(0, tree.DirtyLen())

	// Values collected are exactly what was in dirty (Hash -> NodeValue) and
	// are readable back from a fresh store seeded from the collector.
	fresh := NewMemStore()
	for i, k := range collector.keys {
		var h Hash
		copy(h[:], k)
		r.NoError(fresh.Put(h, collector.values[i]))
	}
	freshTree := NewFromRoot(fresh, tree.Root())
	v, err := freshTree.Get(k)
	r.NoError(err)
	r.Equal([]byte("payload"), v)
}

type erroringCollector struct{}

func (erroringCollector) Collect(k, v []byte) error { return ErrNotFound }

func TestFlushToCollectorPropagatesError(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	var k Hash
	k[0] = 9
	r.NoError(tree.Put(k, []byte("x")))
	err := tree.FlushToCollector(erroringCollector{})
	r.ErrorIs(err, ErrNotFound)
}
