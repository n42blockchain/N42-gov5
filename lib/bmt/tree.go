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
// Content-addressed Binary Merkle Tree core. Tree threads a NodeStore with
// a dirty map of newly created nodes and a current root hash, giving free
// versioning through structural sharing: only the path from a modified
// leaf back to the root produces new nodes. Internal nodes serialise as
// tag + left + right (65 bytes) and leaves as tag + keyHash + value, both
// keyed by their Blake3 hash. New / NewFromRoot / Root / DirtyLen / Get
// form the public entry points; note the tree is NOT goroutine safe.

package bmt

import "bytes"

// Tree is a content-addressed Binary Merkle Tree backed by a NodeStore.
//
// Nodes are stored by their Blake3 hash, not by tree position. This gives
// free versioning through structural sharing: when a leaf changes, only the
// path from leaf to root gets new nodes; unchanged subtrees are reused.
// Old roots can still traverse their original nodes — no explicit version
// numbers needed.
//
// Two node types:
//   - Internal: internalTag(1) + leftHash(32) + rightHash(32) = 65 bytes
//     DB key = Blake3(leftHash || rightHash)
//   - Leaf:     leafTag(1) + keyHash(32) + value(N) = 33+ bytes
//     DB key = HashLeaf(leafData)
//
// NOT THREAD SAFE.
type Tree struct {
	dirty map[Hash]NodeValue // newly created nodes (hash → data)
	store NodeStore          // persistent content-addressed store
	root  Hash               // current root hash
}

// New creates a new empty BMT.
func New(s NodeStore) *Tree {
	return &Tree{dirty: make(map[Hash]NodeValue), store: s, root: EmptyHash}
}

// NewFromRoot creates a BMT rooted at an existing hash.
func NewFromRoot(s NodeStore, root Hash) *Tree {
	return &Tree{dirty: make(map[Hash]NodeValue), store: s, root: root}
}

// Root returns the current root hash. EmptyHash for an empty tree.
func (t *Tree) Root() Hash { return t.root }

// DirtyLen returns the number of unflushed nodes in the dirty map.
func (t *Tree) DirtyLen() int { return len(t.dirty) }

// Get retrieves the value for a key hash. Returns ErrNotFound if absent.
func (t *Tree) Get(keyHash Hash) ([]byte, error) {
	path := FromKeyHash(keyHash)
	return t.get(t.root, path, keyHash, 0)
}

func (t *Tree) get(nodeHash Hash, keyPath Path, keyHash Hash, depth int) ([]byte, error) {
	if nodeHash == EmptyHash {
		return nil, ErrNotFound
	}
	data, err := t.getNode(nodeHash)
	if err != nil {
		return nil, ErrNotFound
	}
	if isLeaf(data) {
		storedKey, ok := extractLeafKeyHash(data)
		if !ok || storedKey != keyHash {
			return nil, ErrNotFound
		}
		return decodeLeafValue(data), nil
	}
	left, right := decodeInternalNode(data)
	if keyPath.Bit(depth) == 0 {
		return t.get(left, keyPath, keyHash, depth+1)
	}
	return t.get(right, keyPath, keyHash, depth+1)
}

// Put inserts or updates a key-value pair.
func (t *Tree) Put(keyHash Hash, value []byte) error {
	encoded := encodeLeafValue(keyHash, value)
	leafHash := toHash(encoded)
	t.setNode(leafHash, encoded)

	keyPath := FromKeyHash(keyHash)
	t.root = t.insert(t.root, keyPath, keyHash, 0, leafHash)
	return nil
}

func (t *Tree) insert(nodeHash Hash, keyPath Path, keyHash Hash, depth int, leafHash Hash) Hash {
	if nodeHash == EmptyHash {
		return leafHash
	}
	data, err := t.getNode(nodeHash)
	if err != nil {
		return leafHash
	}
	if isLeaf(data) {
		existingKey, ok := extractLeafKeyHash(data)
		if !ok {
			return leafHash
		}
		if existingKey == keyHash {
			return leafHash // same key: replace
		}
		// Different key: split
		existingPath := FromKeyHash(existingKey)
		return t.splitLeaves(nodeHash, existingPath, leafHash, keyPath, depth)
	}
	// Internal: descend
	left, right := decodeInternalNode(data)
	if keyPath.Bit(depth) == 0 {
		newLeft := t.insert(left, keyPath, keyHash, depth+1, leafHash)
		return t.makeInternal(newLeft, right)
	}
	newRight := t.insert(right, keyPath, keyHash, depth+1, leafHash)
	return t.makeInternal(left, newRight)
}

func (t *Tree) splitLeaves(existingHash Hash, existingPath Path, newHash Hash, newPath Path, depth int) Hash {
	if depth >= MaxDepth {
		return newHash
	}
	eBit := existingPath.Bit(depth)
	nBit := newPath.Bit(depth)
	if eBit != nBit {
		if nBit == 0 {
			return t.makeInternal(newHash, existingHash)
		}
		return t.makeInternal(existingHash, newHash)
	}
	child := t.splitLeaves(existingHash, existingPath, newHash, newPath, depth+1)
	if eBit == 0 {
		return t.makeInternal(child, EmptyHash)
	}
	return t.makeInternal(EmptyHash, child)
}

// Delete removes a key. Returns ErrNotFound if absent.
func (t *Tree) Delete(keyHash Hash) error {
	keyPath := FromKeyHash(keyHash)
	newRoot, err := t.remove(t.root, keyPath, keyHash, 0)
	if err != nil {
		return err
	}
	t.root = newRoot
	return nil
}

func (t *Tree) remove(nodeHash Hash, keyPath Path, keyHash Hash, depth int) (Hash, error) {
	if nodeHash == EmptyHash {
		return EmptyHash, ErrNotFound
	}
	data, err := t.getNode(nodeHash)
	if err != nil {
		return EmptyHash, ErrNotFound
	}
	if isLeaf(data) {
		storedKey, _ := extractLeafKeyHash(data)
		if storedKey != keyHash {
			return nodeHash, ErrNotFound
		}
		return EmptyHash, nil
	}
	left, right := decodeInternalNode(data)
	var newLeft, newRight Hash
	if keyPath.Bit(depth) == 0 {
		nl, err := t.remove(left, keyPath, keyHash, depth+1)
		if err != nil {
			return nodeHash, err
		}
		newLeft, newRight = nl, right
	} else {
		nr, err := t.remove(right, keyPath, keyHash, depth+1)
		if err != nil {
			return nodeHash, err
		}
		newLeft, newRight = left, nr
	}
	// Collapse: both empty → empty
	if newLeft == EmptyHash && newRight == EmptyHash {
		return EmptyHash, nil
	}
	// Collapse: one side empty, other is leaf → promote leaf
	if newLeft == EmptyHash {
		if rd, e := t.getNode(newRight); e == nil && isLeaf(rd) {
			return newRight, nil
		}
	}
	if newRight == EmptyHash {
		if ld, e := t.getNode(newLeft); e == nil && isLeaf(ld) {
			return newLeft, nil
		}
	}
	return t.makeInternal(newLeft, newRight), nil
}

// FlushTo writes all dirty nodes to the target store and clears the dirty map.
func (t *Tree) FlushTo(target NodeStore) error {
	for hash, data := range t.dirty {
		if err := target.Put(hash, data); err != nil {
			return err
		}
	}
	t.dirty = make(map[Hash]NodeValue)
	return nil
}

// PruneDirty removes unreachable nodes from the dirty map, keeping only nodes
// reachable from the current root. This bounds memory when tree nodes are NOT
// flushed to persistent storage (replay mode: compute root in-memory only).
func (t *Tree) PruneDirty() {
	if len(t.dirty) == 0 || t.root == EmptyHash {
		return
	}
	reachable := make(map[Hash]struct{}, len(t.dirty)/2)
	t.walkReachable(t.root, reachable)
	// Skip deletion if <25% garbage (walk cost not worth it).
	if len(reachable) >= len(t.dirty)*3/4 {
		return
	}
	for h := range t.dirty {
		if _, ok := reachable[h]; !ok {
			delete(t.dirty, h)
		}
	}
}

func (t *Tree) walkReachable(h Hash, reachable map[Hash]struct{}) {
	if h == EmptyHash {
		return
	}
	if _, ok := reachable[h]; ok {
		return // already visited
	}
	data, ok := t.dirty[h]
	if !ok {
		return // in store, not in dirty
	}
	reachable[h] = struct{}{}
	if isInternal(data) {
		left, right := decodeInternalNode(data)
		t.walkReachable(left, reachable)
		t.walkReachable(right, reachable)
	}
}

// ETLCollector is the subset of etl.Collector needed by FlushToCollector.
// This avoids importing the etl package from lib/bmt.
type ETLCollector interface {
	Collect(k, v []byte) error
}

// FlushToCollector writes all dirty nodes to an ETL collector for sorted
// sequential MDBX loading. Much faster than random Put() calls for large batches.
func (t *Tree) FlushToCollector(collector ETLCollector) error {
	for hash, data := range t.dirty {
		if err := collector.Collect(hash[:], data); err != nil {
			return err
		}
	}
	t.dirty = make(map[Hash]NodeValue)
	return nil
}

// --- internal helpers ---

func (t *Tree) getNode(hash Hash) (NodeValue, error) {
	if hash == EmptyHash {
		return nil, ErrNotFound
	}
	if data, ok := t.dirty[hash]; ok {
		return data, nil
	}
	return t.store.Get(hash)
}

func (t *Tree) setNode(hash Hash, data NodeValue) {
	t.dirty[hash] = data
}

func (t *Tree) makeInternal(left, right Hash) Hash {
	data := encodeInternalNode(left, right)
	hash := internalNodeHash(left, right)
	t.setNode(hash, data)
	return hash
}

// --- node encoding ---

func encodeInternalNode(left, right Hash) NodeValue {
	v := make(NodeValue, 1+2*HashSize)
	v[0] = internalTag
	copy(v[1:1+HashSize], left[:])
	copy(v[1+HashSize:], right[:])
	return v
}

func decodeInternalNode(v NodeValue) (left, right Hash) {
	copy(left[:], v[1:1+HashSize])
	copy(right[:], v[1+HashSize:])
	return
}

func internalNodeHash(left, right Hash) Hash {
	return HashNode(left[:], right[:])
}

func encodeLeafValue(keyHash Hash, value []byte) NodeValue {
	v := make(NodeValue, 1+HashSize+len(value))
	v[0] = leafTag
	copy(v[1:1+HashSize], keyHash[:])
	copy(v[1+HashSize:], value)
	return v
}

func decodeLeafValue(v NodeValue) []byte {
	if len(v) < 1+HashSize || v[0] != leafTag {
		return nil
	}
	payload := v[1+HashSize:]
	out := make([]byte, len(payload))
	copy(out, payload)
	return out
}

func extractLeafKeyHash(v NodeValue) (Hash, bool) {
	if len(v) < 1+HashSize || v[0] != leafTag {
		return Hash{}, false
	}
	var h Hash
	copy(h[:], v[1:1+HashSize])
	return h, true
}

// BatchEntry is a key-value pair for batch insertion.
type BatchEntry struct {
	Key   Hash
	Value []byte
}

// PutBatch inserts multiple key-value pairs in a single top-down traversal.
// Each internal node is created only ONCE from the final state of both children,
// eliminating the intermediate root garbage that individual Put() calls produce.
// For N entries at depth D: O(N×D) nodes instead of O(N²×D_near_root).
func (t *Tree) PutBatch(entries []BatchEntry) error {
	if len(entries) == 0 {
		return nil
	}
	// Prepare: encode leaves, compute hashes, build sorted work items.
	items := make([]batchItem, len(entries))
	for i, e := range entries {
		encoded := encodeLeafValue(e.Key, e.Value)
		leafHash := toHash(encoded)
		t.setNode(leafHash, encoded)
		items[i] = batchItem{path: FromKeyHash(e.Key), keyHash: e.Key, leafHash: leafHash}
	}
	// Sort by key hash for bit-partitioning.
	sortBatchItems(items)
	// Deduplicate: last write wins for same key.
	deduped := items[:0]
	for i, it := range items {
		if i > 0 && it.keyHash == items[i-1].keyHash {
			deduped[len(deduped)-1] = it
		} else {
			deduped = append(deduped, it)
		}
	}
	t.root = t.insertBatch(t.root, deduped, 0)
	return nil
}

type batchItem struct {
	path     Path
	keyHash  Hash
	leafHash Hash
}

func sortBatchItems(items []batchItem) {
	// Simple insertion sort for small N (typical: 10-50 items per block).
	for i := 1; i < len(items); i++ {
		key := items[i]
		j := i - 1
		for j >= 0 && comparePath(items[j].keyHash, key.keyHash) > 0 {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = key
	}
}

func comparePath(a, b Hash) int {
	return bytes.Compare(a[:], b[:])
}

// insertBatch recursively inserts sorted items into the subtree rooted at nodeHash.
// Items are partitioned by bit at current depth: left (bit=0) and right (bit=1).
// Each internal node is created exactly once from the final children.
func (t *Tree) insertBatch(nodeHash Hash, items []batchItem, depth int) Hash {
	if len(items) == 0 {
		return nodeHash
	}
	if len(items) == 1 {
		// Single item: use regular insert (handles leaf splitting etc.)
		return t.insert(nodeHash, items[0].path, items[0].keyHash, depth, items[0].leafHash)
	}

	// Partition items by bit at depth.
	pivot := 0
	for pivot < len(items) && items[pivot].path.Bit(depth) == 0 {
		pivot++
	}
	leftItems := items[:pivot]
	rightItems := items[pivot:]

	if nodeHash == EmptyHash {
		// Empty subtree: build from scratch.
		left := t.insertBatch(EmptyHash, leftItems, depth+1)
		right := t.insertBatch(EmptyHash, rightItems, depth+1)
		if left == EmptyHash {
			return right
		}
		if right == EmptyHash {
			return left
		}
		return t.makeInternal(left, right)
	}

	data, err := t.getNode(nodeHash)
	if err != nil {
		return t.insertBatch(EmptyHash, items, depth)
	}

	if isLeaf(data) {
		// Existing leaf: add it to items and rebuild.
		existingKey, ok := extractLeafKeyHash(data)
		if !ok {
			return t.insertBatch(EmptyHash, items, depth)
		}
		// Check if any item replaces this leaf.
		replaced := false
		for _, it := range items {
			if it.keyHash == existingKey {
				replaced = true
				break
			}
		}
		if !replaced {
			// Keep existing leaf alongside new items.
			combined := make([]batchItem, len(items)+1)
			combined[0] = batchItem{path: FromKeyHash(existingKey), keyHash: existingKey, leafHash: nodeHash}
			copy(combined[1:], items)
			sortBatchItems(combined)
			return t.insertBatch(EmptyHash, combined, depth)
		}
		return t.insertBatch(EmptyHash, items, depth)
	}

	// Internal node: recurse into both children.
	left, right := decodeInternalNode(data)
	newLeft := t.insertBatch(left, leftItems, depth+1)
	newRight := t.insertBatch(right, rightItems, depth+1)
	return t.makeInternal(newLeft, newRight)
}
