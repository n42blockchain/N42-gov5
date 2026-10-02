package jmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtensionNodeCreateAndDelete builds a tree whose root is an Extension
// node (two keys sharing a nibble prefix fork into an Internal wrapped by an
// Extension — see putLeaf), then deletes through it to exercise
// deleteExtension's "bubble up bare leaf" branch.
func TestExtensionNodeCreateAndDelete(t *testing.T) {
	r := require.New(t)
	store := NewMemStore()
	tree := New(store)

	// Two keys sharing the first nibble (both start with 0x0?) but diverging
	// at nibble index 1: this forces putLeaf to wrap the fork's internal node
	// in a 1-nibble-long Extension.
	var k1, k2 Hash
	k1[0] = 0x01
	k2[0] = 0x02

	r.NoError(tree.Put(k1, []byte("v1")))
	r.NoError(tree.Put(k2, []byte("v2")))
	r.NoError(tree.Flush())

	root, err := store.Get(tree.Root())
	r.NoError(err)
	node, err := DecodeNode(root)
	r.NoError(err)
	r.Equal(NodeTypeExtension, node.Type, "root should be an Extension node for this key pair")

	// Deleting k1 forces deleteExtension -> collapseInternal -> bare leaf
	// (k2) bubbles up through the extension.
	r.NoError(tree.Delete(k1))

	v, err := tree.Get(k2)
	r.NoError(err)
	r.Equal([]byte("v2"), v)

	_, err = tree.Get(k1)
	r.ErrorIs(err, ErrNotFound)

	// After bubbling a bare leaf all the way up, the root must now be a
	// Leaf, not an Extension (invariant: extensions never point at leaves).
	r.NoError(tree.Flush())
	rootData, err := store.Get(tree.Root())
	r.NoError(err)
	rootNode, err := DecodeNode(rootData)
	r.NoError(err)
	r.Equal(NodeTypeLeaf, rootNode.Type)
}

// TestExtensionDeleteDivergentKeyNotFound exercises deleteExtension's "path
// diverges within the extension prefix" early-return (found=false).
func TestExtensionDeleteDivergentKeyNotFound(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	var k1, k2, other Hash
	k1[0] = 0x01
	k2[0] = 0x02
	other[0] = 0xF0 // diverges from the shared nibble prefix immediately

	r.NoError(tree.Put(k1, []byte("v1")))
	r.NoError(tree.Put(k2, []byte("v2")))

	err := tree.Delete(other)
	r.ErrorIs(err, ErrNotFound)

	// Both original keys remain intact.
	v, err := tree.Get(k1)
	r.NoError(err)
	r.Equal([]byte("v1"), v)
	v, err = tree.Get(k2)
	r.NoError(err)
	r.Equal([]byte("v2"), v)
}
