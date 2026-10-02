package jmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seedTree(t *testing.T, n int) *Tree {
	t.Helper()
	tree := New(NewMemStore())
	for i := 0; i < n; i++ {
		var k Hash
		k[0] = byte(i)
		k[31] = byte(i) ^ 0x5A
		require.NoError(t, tree.Put(k, []byte{byte(i), byte(i + 1)}))
	}
	return tree
}

func TestNewWithHasher(t *testing.T) {
	r := require.New(t)
	h := Blake3Hasher{}
	tree := NewWithHasher(NewMemStore(), h)
	r.Equal(EmptyHash, tree.Root())

	var k Hash
	k[0] = 1
	r.NoError(tree.Put(k, []byte("v")))
	v, err := tree.Get(k)
	r.NoError(err)
	r.Equal([]byte("v"), v)
}

func TestNewFromRootReadOnly(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	var k Hash
	k[0] = 1
	r.NoError(tree.Put(k, []byte("v")))
	r.NoError(tree.Flush())

	ro := NewFromRootReadOnly(store, tree.Root())
	v, err := ro.Get(k)
	r.NoError(err)
	r.Equal([]byte("v"), v)

	// Mutating the read-only-constructed tree still works mechanically
	// (the type doesn't enforce immutability, only sizes its cache small).
	var k2 Hash
	k2[0] = 2
	r.NoError(ro.Put(k2, []byte("v2")))
}

func TestVersionGetSet(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	r.Equal(uint64(0), tree.Version())
	tree.SetVersion(42)
	r.Equal(uint64(42), tree.Version())
}

func TestCanonicalRootMatchesIncrementalRoot(t *testing.T) {
	r := require.New(t)
	tree := seedTree(t, 10)
	incrementalRoot := tree.Root()

	canonical, err := tree.CanonicalRoot()
	r.NoError(err)
	r.Equal(incrementalRoot, canonical)
}

func TestCanonicalRootEmptyTree(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	root, err := tree.CanonicalRoot()
	r.NoError(err)
	r.Equal(EmptyHash, root)
}

func TestLeafChecksumOrderIndependent(t *testing.T) {
	r := require.New(t)
	treeA := New(NewMemStore())
	treeB := New(NewMemStore())

	keys := make([]Hash, 5)
	for i := 0; i < 5; i++ {
		var k Hash
		k[0] = byte(i)
		k[31] = byte(i) ^ 0x33
		keys[i] = k
	}
	// Insert in forward order into A, reverse order into B.
	for i := 0; i < 5; i++ {
		r.NoError(treeA.Put(keys[i], []byte{byte(i)}))
	}
	for i := 4; i >= 0; i-- {
		r.NoError(treeB.Put(keys[i], []byte{byte(i)}))
	}

	sumA, countA, err := treeA.LeafChecksum()
	r.NoError(err)
	sumB, countB, err := treeB.LeafChecksum()
	r.NoError(err)

	r.Equal(countA, countB)
	r.Equal(5, countA)
	r.Equal(sumA, sumB)
}

func TestLeafCount(t *testing.T) {
	r := require.New(t)
	tree := seedTree(t, 7)
	n, err := tree.LeafCount()
	r.NoError(err)
	r.Equal(7, n)

	empty := New(NewMemStore())
	n, err = empty.LeafCount()
	r.NoError(err)
	r.Equal(0, n)
}

func TestSnapshotClearDirtyCountBytes(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	r.Equal(0, tree.DirtyCount())
	r.Equal(int64(0), tree.DirtyBytes())
	r.Empty(tree.SnapshotDirty())

	var k Hash
	k[0] = 1
	r.NoError(tree.Put(k, []byte("value")))
	r.True(tree.DirtyCount() > 0)
	r.True(tree.DirtyBytes() > 0)

	snap := tree.SnapshotDirty()
	r.Equal(tree.DirtyCount(), len(snap))

	tree.ClearDirty()
	r.Equal(0, tree.DirtyCount())
	r.Equal(int64(0), tree.DirtyBytes())
	// Snapshot taken earlier is unaffected by ClearDirty (deep copy).
	r.NotEmpty(snap)
}

func TestTreeReset(t *testing.T) {
	r := require.New(t)
	tree := seedTree(t, 5)
	r.NotEqual(EmptyHash, tree.Root())
	r.True(tree.DirtyCount() > 0)

	tree.Reset()
	r.Equal(EmptyHash, tree.Root())
	r.Equal(0, tree.DirtyCount())
	r.Equal(0, tree.NodeCacheSize())
}

func TestStoreAccessor(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)
	r.Same(store, tree.Store())
}

func TestNodeCacheSizeGrows(t *testing.T) {
	r := require.New(t)
	tree := seedTree(t, 3)
	r.True(tree.NodeCacheSize() > 0)
}

func TestCollectGarbageNoGCIsNoOp(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	n, err := tree.CollectGarbage(NewMemStore())
	r.NoError(err)
	r.Equal(0, n)
	r.Nil(tree.GC())
}
