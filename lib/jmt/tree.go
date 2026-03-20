// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package jmt

// No external imports needed — NodeStore is defined in store.go within this package.

// Tree is a Jellyfish Merkle Tree backed by a NodeStore.
//
// The tree is a 16-ary trie keyed by Blake3 hashes (64 nibbles).
// Three node types keep the structure compact:
//   - InternalNode: 16-way branch (sparse, bitmap-indexed)
//   - LeafNode: terminal key-value pair
//   - ExtensionNode: path-compressed chain of single-child internals
//
// All nodes are content-addressed: stored and looked up by Blake3(serialized).
//
// NOT THREAD SAFE: Tree methods must not be called concurrently.
// The caller is responsible for external synchronization if the tree
// is shared across goroutines. This is consistent with IntraBlockState
// and other per-block data structures in the N42 codebase.

// DefaultNodeCacheSize is the default number of decoded nodes to cache.
const DefaultNodeCacheSize = 16384

type Tree struct {
	root   Hash
	store  NodeStore
	hasher Hasher

	// dirty tracks nodes written during the current mutation batch
	// that have not yet been flushed to the store.
	dirty map[Hash][]byte

	// nodeCache is an LRU cache of decoded *Node objects keyed by content hash.
	nodeCache    map[Hash]*nodeCacheEntry
	nodeCacheCap int
	nodeCacheSeq uint64 // per-tree sequence counter for LRU eviction

	// gc tracks node reference counts for garbage collection.
	// When enabled, old nodes replaced during tree mutations are queued
	// for deletion, preventing unbounded store growth.
	gc *NodeGC
}

type nodeCacheEntry struct {
	node *Node
	seq  uint64 // access sequence for LRU eviction
}

// New creates a new empty JMT with the given store and default Blake3 hasher.
func New(s NodeStore) *Tree {
	return &Tree{
		root:         EmptyHash,
		store:        s,
		hasher:       DefaultHasher(),
		dirty:        make(map[Hash][]byte),
		nodeCache:    make(map[Hash]*nodeCacheEntry, DefaultNodeCacheSize),
		nodeCacheCap: DefaultNodeCacheSize,
	}
}

// NewWithHasher creates a JMT with a custom hasher.
func NewWithHasher(s NodeStore, h Hasher) *Tree {
	return &Tree{
		root:         EmptyHash,
		store:        s,
		hasher:       h,
		dirty:        make(map[Hash][]byte),
		nodeCache:    make(map[Hash]*nodeCacheEntry, DefaultNodeCacheSize),
		nodeCacheCap: DefaultNodeCacheSize,
	}
}

// NewFromRoot opens an existing JMT from a known root hash.
func NewFromRoot(s NodeStore, root Hash) *Tree {
	return &Tree{
		root:         root,
		store:        s,
		hasher:       DefaultHasher(),
		dirty:        make(map[Hash][]byte),
		nodeCache:    make(map[Hash]*nodeCacheEntry, DefaultNodeCacheSize),
		nodeCacheCap: DefaultNodeCacheSize,
	}
}

// Root returns the current root hash. EmptyHash for an empty tree.
func (t *Tree) Root() Hash {
	return t.root
}

// Get looks up the value associated with the given key hash.
// Returns ErrNotFound if the key is absent.
func (t *Tree) Get(keyHash Hash) ([]byte, error) {
	if t.root == EmptyHash {
		return nil, ErrNotFound
	}
	path := NewNibblePath(keyHash)
	return t.get(t.root, path, 0)
}

func (t *Tree) get(nodeHash Hash, path NibblePath, depth int) ([]byte, error) {
	node, err := t.loadNode(nodeHash)
	if err != nil {
		return nil, err
	}

	switch node.Type {
	case NodeTypeLeaf:
		if node.Leaf.KeyHash == path.data {
			return node.Leaf.Value, nil
		}
		return nil, ErrNotFound

	case NodeTypeInternal:
		nibble := path.At(depth)
		child := node.Internal.Children[nibble]
		if !child.Valid {
			return nil, ErrNotFound
		}
		return t.get(child.Hash, path, depth+1)

	case NodeTypeExtension:
		ext := node.Extension
		extLen := ext.Path.Len()
		if depth+extLen > path.Len() {
			return nil, ErrNotFound
		}
		for i := 0; i < extLen; i++ {
			if path.At(depth+i) != ext.Path.At(i) {
				return nil, ErrNotFound
			}
		}
		return t.get(ext.Child, path, depth+extLen)

	default:
		return nil, ErrInvalidNode
	}
}

// Put inserts or updates a key-value pair. The key must be a Blake3 hash.
// value must not be nil (use Delete to remove keys).
func (t *Tree) Put(keyHash Hash, value []byte) error {
	path := NewNibblePath(keyHash)
	leaf := NewLeafNode(keyHash, value, t.hasher)

	if t.root == EmptyHash {
		h := t.storeNode(leaf)
		t.root = h
		return nil
	}

	oldRoot := t.root
	newRoot, err := t.put(t.root, path, 0, leaf)
	if err != nil {
		return err
	}
	if t.gc != nil && newRoot != oldRoot {
		t.gc.DecRef(oldRoot)
	}
	t.root = newRoot
	return nil
}

func (t *Tree) put(nodeHash Hash, path NibblePath, depth int, leaf *Node) (Hash, error) {
	node, err := t.loadNode(nodeHash)
	if err != nil {
		return EmptyHash, err
	}

	switch node.Type {
	case NodeTypeLeaf:
		return t.putLeaf(node, path, depth, leaf)

	case NodeTypeInternal:
		return t.putInternal(node, path, depth, leaf)

	case NodeTypeExtension:
		return t.putExtension(node, path, depth, leaf)

	default:
		return EmptyHash, ErrInvalidNode
	}
}

// putLeaf handles inserting when we've reached an existing leaf.
func (t *Tree) putLeaf(existing *Node, path NibblePath, depth int, newLeaf *Node) (Hash, error) {
	existingPath := NewNibblePath(existing.Leaf.KeyHash)

	// Same key — update value.
	if existing.Leaf.KeyHash == newLeaf.Leaf.KeyHash {
		return t.storeNode(newLeaf), nil
	}

	// Different keys — split into an internal (or extension) node.
	commonLen := 0
	for d := depth; d < NibbleCount; d++ {
		if existingPath.At(d) == path.At(d) {
			commonLen++
		} else {
			break
		}
	}

	// Create internal node at the fork point.
	internal := NewInternalNode()
	existingNibble := existingPath.At(depth + commonLen)
	newNibble := path.At(depth + commonLen)
	internal.Internal.Children[existingNibble] = ChildEntry{Hash: t.storeNode(existing), Valid: true}
	internal.Internal.Children[newNibble] = ChildEntry{Hash: t.storeNode(newLeaf), Valid: true}
	internalHash := t.storeNode(internal)

	// Wrap with extension if there is a shared prefix.
	if commonLen > 0 {
		extPath := path.Slice(depth, depth+commonLen)
		ext := NewExtensionNode(extPath, internalHash)
		return t.storeNode(ext), nil
	}
	return internalHash, nil
}

// putInternal handles inserting into an internal node.
func (t *Tree) putInternal(node *Node, path NibblePath, depth int, leaf *Node) (Hash, error) {
	nibble := path.At(depth)
	child := node.Internal.Children[nibble]

	var childHash Hash
	if !child.Valid {
		// Empty slot — place the leaf directly.
		childHash = t.storeNode(leaf)
	} else {
		// Recurse into existing child.
		var err error
		childHash, err = t.put(child.Hash, path, depth+1, leaf)
		if err != nil {
			return EmptyHash, err
		}
	}

	// Build a new internal node with the updated child.
	// DecRef the old child if it was replaced.
	if t.gc != nil && child.Valid && child.Hash != childHash {
		t.gc.DecRef(child.Hash)
	}
	newInternal := NewInternalNode()
	*newInternal.Internal = *node.Internal
	newInternal.Internal.Children[nibble] = ChildEntry{Hash: childHash, Valid: true}
	return t.storeNode(newInternal), nil
}

// putExtension handles inserting when we encounter an extension node.
func (t *Tree) putExtension(node *Node, path NibblePath, depth int, leaf *Node) (Hash, error) {
	ext := node.Extension
	extNibbles := ext.Path.Nibbles()
	extLen := len(extNibbles)

	// Find where the new key diverges from the extension path.
	commonLen := 0
	for i := 0; i < extLen && depth+i < NibbleCount; i++ {
		if path.At(depth+i) == extNibbles[i] {
			commonLen++
		} else {
			break
		}
	}

	// Full match — recurse into the child.
	if commonLen == extLen {
		newChild, err := t.put(ext.Child, path, depth+extLen, leaf)
		if err != nil {
			return EmptyHash, err
		}
		if extLen == 1 {
			// Single nibble extension can be inlined into parent.
			return t.wrapSingleExtension(extNibbles[0], newChild), nil
		}
		newExt := NewExtensionNode(ext.Path, newChild)
		return t.storeNode(newExt), nil
	}

	// Partial match — split the extension.
	// Create the fork internal node.
	fork := NewInternalNode()

	// The remaining portion of the old extension.
	oldNibble := extNibbles[commonLen]
	if commonLen+1 < extLen {
		// Old extension still has a suffix.
		suffix := NewNibblePathFromSlice(extNibbles[commonLen+1:])
		suffixExt := NewExtensionNode(suffix, ext.Child)
		fork.Internal.Children[oldNibble] = ChildEntry{Hash: t.storeNode(suffixExt), Valid: true}
	} else {
		// Old extension consumed — child goes directly into fork.
		fork.Internal.Children[oldNibble] = ChildEntry{Hash: ext.Child, Valid: true}
	}

	// Place the new leaf (or recurse if there's more path).
	newNibble := path.At(depth + commonLen)
	fork.Internal.Children[newNibble] = ChildEntry{Hash: t.storeNode(leaf), Valid: true}
	forkHash := t.storeNode(fork)

	// Wrap with extension for the shared prefix.
	if commonLen > 0 {
		prefix := NewNibblePathFromSlice(extNibbles[:commonLen])
		prefixExt := NewExtensionNode(prefix, forkHash)
		return t.storeNode(prefixExt), nil
	}
	return forkHash, nil
}

// wrapSingleExtension builds an internal node with a single child at the given nibble.
func (t *Tree) wrapSingleExtension(nibble byte, childHash Hash) Hash {
	internal := NewInternalNode()
	internal.Internal.Children[nibble] = ChildEntry{Hash: childHash, Valid: true}
	return t.storeNode(internal)
}

// Delete removes a key from the tree. Returns ErrNotFound if the key is absent.
func (t *Tree) Delete(keyHash Hash) error {
	if t.root == EmptyHash {
		return ErrNotFound
	}
	path := NewNibblePath(keyHash)
	newRoot, found, err := t.delete(t.root, path, 0)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if t.gc != nil && newRoot != t.root {
		t.gc.DecRef(t.root)
	}
	t.root = newRoot
	return nil
}

func (t *Tree) delete(nodeHash Hash, path NibblePath, depth int) (Hash, bool, error) {
	node, err := t.loadNode(nodeHash)
	if err != nil {
		return EmptyHash, false, err
	}

	switch node.Type {
	case NodeTypeLeaf:
		if node.Leaf.KeyHash != path.data {
			return EmptyHash, false, nil
		}
		return EmptyHash, true, nil

	case NodeTypeInternal:
		return t.deleteInternal(node, path, depth)

	case NodeTypeExtension:
		return t.deleteExtension(node, path, depth)

	default:
		return EmptyHash, false, ErrInvalidNode
	}
}

func (t *Tree) deleteInternal(node *Node, path NibblePath, depth int) (Hash, bool, error) {
	nibble := path.At(depth)
	child := node.Internal.Children[nibble]
	if !child.Valid {
		return EmptyHash, false, nil
	}

	newChild, found, err := t.delete(child.Hash, path, depth+1)
	if err != nil || !found {
		return EmptyHash, found, err
	}

	// Build updated internal node.
	newInternal := NewInternalNode()
	*newInternal.Internal = *node.Internal
	if newChild == EmptyHash {
		newInternal.Internal.Children[nibble] = ChildEntry{}
	} else {
		newInternal.Internal.Children[nibble] = ChildEntry{Hash: newChild, Valid: true}
	}

	// Collapse if only one child remains.
	return t.collapseInternal(newInternal), true, nil
}

func (t *Tree) deleteExtension(node *Node, path NibblePath, depth int) (Hash, bool, error) {
	ext := node.Extension
	extLen := ext.Path.Len()
	for i := 0; i < extLen; i++ {
		if depth+i >= path.Len() || path.At(depth+i) != ext.Path.At(i) {
			return EmptyHash, false, nil
		}
	}

	newChild, found, err := t.delete(ext.Child, path, depth+extLen)
	if err != nil || !found {
		return EmptyHash, found, err
	}

	if newChild == EmptyHash {
		return EmptyHash, true, nil
	}

	// Check if the child is also an extension — merge paths.
	childNode, err := t.loadNode(newChild)
	if err != nil {
		return EmptyHash, false, err
	}
	if childNode.Type == NodeTypeExtension {
		merged := ext.Path.Concat(childNode.Extension.Path)
		mergedExt := NewExtensionNode(merged, childNode.Extension.Child)
		return t.storeNode(mergedExt), true, nil
	}

	newExt := NewExtensionNode(ext.Path, newChild)
	return t.storeNode(newExt), true, nil
}

// collapseInternal checks if an internal node has only one child and,
// if that child is a leaf or extension, merges them.
func (t *Tree) collapseInternal(node *Node) Hash {
	idx, childHash := node.Internal.singleChild()
	if idx < 0 {
		// 0 or 2+ children — keep as internal.
		if node.Internal.childCount() == 0 {
			return EmptyHash
		}
		return t.storeNode(node)
	}

	// Single child — try to collapse.
	childNode, err := t.loadNode(childHash)
	if err != nil {
		// Cannot load child — this should not happen in a well-formed tree.
		// Keep the internal node as-is to avoid data loss.
		return t.storeNode(node)
	}

	nibble := byte(idx)
	switch childNode.Type {
	case NodeTypeLeaf:
		// A single-child internal pointing to a leaf can be replaced by
		// an extension → leaf (or just the leaf if parent handles it).
		// But since we always key by the full hash, we keep the leaf as-is.
		ext := NewExtensionNode(NewNibblePathFromSlice([]byte{nibble}), childHash)
		return t.storeNode(ext)

	case NodeTypeExtension:
		// Merge: nibble + extension path.
		merged := NewNibblePathFromSlice([]byte{nibble}).Concat(childNode.Extension.Path)
		ext := NewExtensionNode(merged, childNode.Extension.Child)
		return t.storeNode(ext)

	default:
		// Internal child — wrap with single-nibble extension.
		ext := NewExtensionNode(NewNibblePathFromSlice([]byte{nibble}), childHash)
		return t.storeNode(ext)
	}
}

// Flush writes all dirty nodes to the underlying store and clears the buffer.
func (t *Tree) Flush() error {
	return t.FlushTo(t.store)
}

// FlushTo writes all dirty nodes to the given external store and clears the
// buffer. This is used when the tree's primary store is in-memory but nodes
// must be persisted to a different backend (e.g., an MDBX transaction).
func (t *Tree) FlushTo(target NodeStore) error {
	for h, data := range t.dirty {
		if err := target.Put(h, data); err != nil {
			return err
		}
		// Promote dirty nodes to the parsed cache so they remain accessible
		// across subsequent payloads without hitting the store.
		if t.nodeCache != nil {
			if _, cached := t.nodeCache[h]; !cached {
				if node, err := DecodeNode(data); err == nil {
					t.cacheNode(h, node)
				}
			}
		}
	}
	t.dirty = make(map[Hash][]byte)
	return nil
}

// DirtyCount returns the number of unflushed nodes.
func (t *Tree) DirtyCount() int {
	return len(t.dirty)
}

// Reset clears the tree to an empty state, discarding all dirty nodes.
func (t *Tree) Reset() {
	t.root = EmptyHash
	t.dirty = make(map[Hash][]byte)
	t.nodeCache = make(map[Hash]*nodeCacheEntry, t.nodeCacheCap)
}

// EnableGC activates reference-counting garbage collection.
// When enabled, nodes replaced during Put/Delete are tracked and can be
// collected via CollectGarbage(). Call after tree construction.
func (t *Tree) EnableGC() {
	t.gc = NewNodeGC()
	// The current root is the initial live reference.
	if t.root != EmptyHash {
		t.gc.IncRef(t.root)
	}
}

// GC returns the garbage collector, or nil if GC is not enabled.
func (t *Tree) GC() *NodeGC {
	return t.gc
}

// CollectGarbage deletes unreachable nodes from the given store.
// Returns the number of nodes deleted. No-op if GC is not enabled.
func (t *Tree) CollectGarbage(store NodeStore) (int, error) {
	if t.gc == nil {
		return 0, nil
	}
	return t.gc.CollectGarbage(store)
}

// storeNode serializes a node, computes its hash, and buffers it in dirty.
func (t *Tree) storeNode(n *Node) Hash {
	data := EncodeNode(n)
	h := t.hasher.Hash(data)
	t.dirty[h] = data
	if t.gc != nil {
		t.gc.IncRef(h)
	}
	return h
}

// loadNode retrieves a node by hash, checking dirty buffer first, then store.
func (t *Tree) loadNode(h Hash) (*Node, error) {
	// 1. Check dirty buffer (uncommitted writes)
	if data, ok := t.dirty[h]; ok {
		return DecodeNode(data)
	}
	// 2. Check parsed node cache (avoids re-decode + store lookup)
	if t.nodeCache != nil {
		if entry, ok := t.nodeCache[h]; ok {
			t.nodeCacheSeq++
			entry.seq = t.nodeCacheSeq
			return entry.node, nil
		}
	}
	// 3. Load from backing store
	data, err := t.store.Get(h)
	if err != nil {
		return nil, err
	}
	node, err := DecodeNode(data)
	if err != nil {
		return nil, err
	}
	// Populate cache
	t.cacheNode(h, node)
	return node, nil
}

// cacheNode adds a decoded node to the LRU cache, evicting the oldest
// entry if the cache is at capacity.
func (t *Tree) cacheNode(h Hash, n *Node) {
	if t.nodeCache == nil || t.nodeCacheCap <= 0 {
		return
	}
	t.nodeCacheSeq++
	t.nodeCache[h] = &nodeCacheEntry{node: n, seq: t.nodeCacheSeq}

	// Simple eviction: when over capacity, remove the oldest entry
	if len(t.nodeCache) > t.nodeCacheCap {
		var oldestHash Hash
		oldestSeq := t.nodeCacheSeq
		for k, v := range t.nodeCache {
			if v.seq < oldestSeq {
				oldestSeq = v.seq
				oldestHash = k
			}
		}
		delete(t.nodeCache, oldestHash)
	}
}

// NodeCacheSize returns the current number of cached decoded nodes.
func (t *Tree) NodeCacheSize() int {
	if t.nodeCache == nil {
		return 0
	}
	return len(t.nodeCache)
}
