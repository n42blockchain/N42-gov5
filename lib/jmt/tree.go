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
//
// Core Tree type for the Jellyfish Merkle Tree. Tree owns a root hash,
// a NodeStore, a Hasher, a monotonically increasing version counter, a
// dirty map of unflushed nodes and an LRU nodeCache (container/list
// based, capped at DefaultNodeCacheSize = 131072 entries) that absorbs
// the role previously played by CachedStore. An optional NodeGC tracks
// reference counts so mutations can queue replaced nodes for deletion.
// The tree is NOT goroutine-safe; callers synchronise externally, in
// line with IntraBlockState and other per-block data structures.

package jmt

import "container/list"

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
// Set to 128K to absorb the workload previously handled by CachedStore.
const DefaultNodeCacheSize = 131072

type Tree struct {
	root   Hash
	store  NodeStore
	hasher Hasher

	// version is the monotonically increasing version number (typically block
	// height) of the tree. It is bumped by the caller after each batch of
	// mutations to enable historical state queries and migration tracking.
	version uint64

	// dirty tracks nodes written during the current mutation batch
	// that have not yet been flushed to the store.
	dirty map[Hash][]byte

	// nodeCache is an LRU cache of decoded *Node objects keyed by content hash.
	// Eviction is O(1) via a doubly-linked list (container/list).
	nodeCache     map[Hash]*list.Element
	nodeCacheLRU  *list.List
	nodeCacheCap  int

	// gc tracks node reference counts for garbage collection.
	// When enabled, old nodes replaced during tree mutations are queued
	// for deletion, preventing unbounded store growth.
	gc *NodeGC
}

type nodeCacheEntry struct {
	hash Hash
	node *Node
}

// New creates a new empty JMT with the given store and default Blake3 hasher.
func New(s NodeStore) *Tree {
	return &Tree{
		root:         EmptyHash,
		store:        s,
		hasher:       DefaultHasher(),
		dirty:        make(map[Hash][]byte),
		nodeCache:    make(map[Hash]*list.Element, DefaultNodeCacheSize),
		nodeCacheLRU: list.New(),
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
		nodeCache:    make(map[Hash]*list.Element, DefaultNodeCacheSize),
		nodeCacheLRU: list.New(),
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
		nodeCache:    make(map[Hash]*list.Element, DefaultNodeCacheSize),
		nodeCacheLRU: list.New(),
		nodeCacheCap: DefaultNodeCacheSize,
	}
}

// Root returns the current root hash. EmptyHash for an empty tree.
func (t *Tree) Root() Hash {
	return t.root
}

// NewFromRootReadOnly opens a JMT from a known root with a small cache,
// optimized for read-only proof queries. Uses 1/128th the cache of a full
// tree since proof paths are at most ~64 nodes deep.
func NewFromRootReadOnly(s NodeStore, root Hash) *Tree {
	const readOnlyCacheSize = 1024
	return &Tree{
		root:         root,
		store:        s,
		hasher:       DefaultHasher(),
		dirty:        make(map[Hash][]byte),
		nodeCache:    make(map[Hash]*list.Element, readOnlyCacheSize),
		nodeCacheLRU: list.New(),
		nodeCacheCap: readOnlyCacheSize,
	}
}

// Version returns the current tree version (typically the last processed block height).
func (t *Tree) Version() uint64 {
	return t.version
}

// SetVersion sets the tree version. Call after processing a block to associate
// the current root with a block height. This enables historical state queries
// and migration progress tracking.
func (t *Tree) SetVersion(v uint64) {
	t.version = v
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

	// Full match — recurse into the child (always an internal node by the
	// canonical invariant), then re-wrap with the same extension path. We do
	// NOT inline single-nibble extensions into a single-child internal: that
	// produces a second representation for the same logical structure and makes
	// the tree shape depend on insertion/deletion history rather than the live
	// key set. Extensions point only to internals; this stays canonical.
	if commonLen == extLen {
		newChild, err := t.put(ext.Child, path, depth+extLen, leaf)
		if err != nil {
			return EmptyHash, err
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

	// Collapse to the canonical shape if the node dropped below 2 children.
	h, err := t.collapseInternal(newInternal)
	if err != nil {
		return EmptyHash, false, err
	}
	return h, true, nil
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

	childNode, err := t.loadNode(newChild)
	if err != nil {
		return EmptyHash, false, err
	}
	switch childNode.Type {
	case NodeTypeLeaf:
		// An extension must never point to a leaf (the leaf already carries its
		// full key hash, so the path prefix is redundant). Bubble the bare leaf
		// up so an ancestor places it as a direct child — matching the shape a
		// delete-free Put history would produce.
		return newChild, true, nil
	case NodeTypeExtension:
		// Merge consecutive extensions into one.
		merged := ext.Path.Concat(childNode.Extension.Path)
		return t.storeNode(NewExtensionNode(merged, childNode.Extension.Child)), true, nil
	default: // Internal
		return t.storeNode(NewExtensionNode(ext.Path, newChild)), true, nil
	}
}

// collapseInternal returns the canonical representation of an internal node that
// may have dropped below two children after a deletion. It enforces the strict
// JMT invariants so the tree shape is a pure function of the live key set
// (independent of insertion/deletion history):
//
//   - internal nodes have >= 2 children,
//   - extension nodes point only to internal nodes (never a leaf or extension),
//   - leaves are bare (no extension wrapper).
//
// A single remaining child is therefore lifted: a leaf bubbles up as-is; an
// extension absorbs the branch nibble into its path; an internal is wrapped in a
// single-nibble extension. Returns EmptyHash when no children remain.
func (t *Tree) collapseInternal(node *Node) (Hash, error) {
	cc := node.Internal.childCount()
	if cc == 0 {
		return EmptyHash, nil
	}
	if cc >= 2 {
		return t.storeNode(node), nil
	}

	idx, childHash := node.Internal.singleChild()
	childNode, err := t.loadNode(childHash)
	if err != nil {
		return EmptyHash, err
	}
	nibble := byte(idx)
	switch childNode.Type {
	case NodeTypeLeaf:
		// Bubble the bare leaf up — no extension wrapper.
		return childHash, nil
	case NodeTypeExtension:
		// Prepend the branch nibble to the child extension's path.
		merged := NewNibblePathFromSlice([]byte{nibble}).Concat(childNode.Extension.Path)
		return t.storeNode(NewExtensionNode(merged, childNode.Extension.Child)), nil
	default: // Internal
		return t.storeNode(NewExtensionNode(NewNibblePathFromSlice([]byte{nibble}), childHash)), nil
	}
}

// CanonicalRoot rebuilds the tree's current live leaf set into a fresh tree and
// returns that root. For a correctly maintained tree this always equals Root();
// any difference reveals non-canonical internal structure left behind by an
// incremental mutation. Intended for diagnostics and tests, not the hot path.
func (t *Tree) CanonicalRoot() (Hash, error) {
	var entries []BatchEntry
	if err := t.collectLeavesForRebuild(t.root, &entries); err != nil {
		return EmptyHash, err
	}
	if len(entries) == 0 {
		return EmptyHash, nil
	}
	return New(NewMemStore()).BatchUpdate(entries)
}

// LeafChecksum returns an order-independent XOR checksum over hash(keyHash||value)
// of every leaf. Two trees with the same {key→value} set share a checksum.
func (t *Tree) LeafChecksum() (Hash, int, error) {
	var entries []BatchEntry
	if err := t.collectLeavesForRebuild(t.root, &entries); err != nil {
		return EmptyHash, 0, err
	}
	var x Hash
	for _, e := range entries {
		buf := make([]byte, 0, HashSize+len(e.Value))
		buf = append(buf, e.KeyHash[:]...)
		buf = append(buf, e.Value...)
		h := t.hasher.Hash(buf)
		for i := range x {
			x[i] ^= h[i]
		}
	}
	return x, len(entries), nil
}

// LeafCount returns the number of leaves reachable from the current root.
func (t *Tree) LeafCount() (int, error) {
	var entries []BatchEntry
	if err := t.collectLeavesForRebuild(t.root, &entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (t *Tree) collectLeavesForRebuild(h Hash, out *[]BatchEntry) error {
	if h == EmptyHash {
		return nil
	}
	n, err := t.loadNode(h)
	if err != nil {
		return err
	}
	switch n.Type {
	case NodeTypeLeaf:
		v := make([]byte, len(n.Leaf.Value))
		copy(v, n.Leaf.Value)
		*out = append(*out, BatchEntry{KeyHash: n.Leaf.KeyHash, Value: v})
	case NodeTypeExtension:
		return t.collectLeavesForRebuild(n.Extension.Child, out)
	case NodeTypeInternal:
		for i := range n.Internal.Children {
			if n.Internal.Children[i].Valid {
				if err := t.collectLeavesForRebuild(n.Internal.Children[i].Hash, out); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Flush writes all dirty nodes to the underlying store and clears the buffer.
func (t *Tree) Flush() error {
	return t.FlushTo(t.store)
}

// FlushTo writes all dirty nodes to the given external store and clears the
// buffer. This is used when the tree's primary store is in-memory but nodes
// must be persisted to a different backend (e.g., an MDBX transaction).
//
// If target implements BatchNodeStore, PutBatch is used for efficiency.
func (t *Tree) FlushTo(target NodeStore) error {
	if batch, ok := target.(BatchNodeStore); ok {
		if err := batch.PutBatch(t.dirty); err != nil {
			return err
		}
	} else {
		for h, data := range t.dirty {
			if err := target.Put(h, data); err != nil {
				return err
			}
		}
	}

	// Promote dirty nodes to the parsed cache so they remain accessible
	// across subsequent payloads without hitting the store.
	if t.nodeCache != nil {
		for h, data := range t.dirty {
			if _, cached := t.nodeCache[h]; !cached {
				if node, decErr := DecodeNode(data); decErr == nil {
					t.cacheNode(h, node)
				}
			}
		}
	}

	t.dirty = make(map[Hash][]byte)
	return nil
}

// SnapshotDirty returns a deep copy of the dirty node map. Used by the deep
// pipeline's commitment stage to capture mutations without flushing.
func (t *Tree) SnapshotDirty() map[Hash][]byte {
	snapshot := make(map[Hash][]byte, len(t.dirty))
	for h, data := range t.dirty {
		cp := make([]byte, len(data))
		copy(cp, data)
		snapshot[h] = cp
	}
	return snapshot
}

// ClearDirty discards all unflushed mutations.
func (t *Tree) ClearDirty() {
	t.dirty = make(map[Hash][]byte)
}

// DirtyCount returns the number of unflushed nodes.
func (t *Tree) DirtyCount() int {
	return len(t.dirty)
}

// DirtyBytes returns the total serialized size of unflushed nodes (key+value).
func (t *Tree) DirtyBytes() int64 {
	var total int64
	for h, data := range t.dirty {
		total += int64(len(h)) + int64(len(data))
	}
	return total
}

// Reset clears the tree to an empty state, discarding all dirty nodes.
func (t *Tree) Reset() {
	t.root = EmptyHash
	t.dirty = make(map[Hash][]byte)
	t.nodeCache = make(map[Hash]*list.Element, t.nodeCacheCap)
	t.nodeCacheLRU.Init()
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

// Store returns the backing node store.
func (t *Tree) Store() NodeStore { return t.store }

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

// loadNode retrieves a node by hash, checking the parsed node cache first,
// then the dirty buffer (uncommitted writes), then the backing store.
func (t *Tree) loadNode(h Hash) (*Node, error) {
	// 1. Check parsed node cache (avoids re-decode for both dirty and stored nodes)
	if t.nodeCache != nil {
		if elem, ok := t.nodeCache[h]; ok {
			t.nodeCacheLRU.MoveToFront(elem)
			return elem.Value.(*nodeCacheEntry).node, nil
		}
	}
	// 2. Check dirty buffer (uncommitted writes)
	if data, ok := t.dirty[h]; ok {
		node, err := DecodeNode(data)
		if err != nil {
			return nil, err
		}
		// Cache the decoded node to avoid re-decoding on subsequent lookups.
		t.cacheNode(h, node)
		return node, nil
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

// cacheNode adds a decoded node to the LRU cache, evicting the least
// recently used entry in O(1) if the cache is at capacity.
func (t *Tree) cacheNode(h Hash, n *Node) {
	if t.nodeCache == nil || t.nodeCacheCap <= 0 {
		return
	}

	// Already cached — promote to front.
	if elem, ok := t.nodeCache[h]; ok {
		t.nodeCacheLRU.MoveToFront(elem)
		elem.Value.(*nodeCacheEntry).node = n
		return
	}

	// Evict LRU entry if at capacity.
	if t.nodeCacheLRU.Len() >= t.nodeCacheCap {
		tail := t.nodeCacheLRU.Back()
		if tail != nil {
			evicted := t.nodeCacheLRU.Remove(tail).(*nodeCacheEntry)
			delete(t.nodeCache, evicted.hash)
		}
	}

	entry := &nodeCacheEntry{hash: h, node: n}
	elem := t.nodeCacheLRU.PushFront(entry)
	t.nodeCache[h] = elem
}

// NodeCacheSize returns the current number of cached decoded nodes.
func (t *Tree) NodeCacheSize() int {
	if t.nodeCache == nil {
		return 0
	}
	return t.nodeCacheLRU.Len()
}
