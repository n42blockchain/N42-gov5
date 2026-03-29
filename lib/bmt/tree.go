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

package bmt

// Tree is a Binary Merkle Tree backed by a NodeStore.
//
// The tree is a binary trie keyed by Blake3 hashes (256 bits).
// Two node types keep the structure compact:
//   - Internal node: stores Blake3(left || right) as its value (exactly 32 bytes)
//   - Leaf node: stores the actual data (inline if <32B, tagged if ==32B)
//
// Nodes are addressed by their bit-path from the root, not by content hash.
// The dirty map buffers in-memory mutations before flushing to the store.
//
// NOT THREAD SAFE: Tree methods must not be called concurrently.
// The caller is responsible for external synchronization if the tree
// is shared across goroutines.
type Tree struct {
	dirty   map[string]NodeValue // path.String() -> value (nil means deleted)
	store   NodeStore
	version uint64

	// leafKeys tracks which paths are leaves (storing the full key hash).
	// This is needed for leaf splitting: when inserting a key that collides
	// at a path prefix with an existing leaf, we must know the existing
	// leaf's full key hash to determine where the two diverge.
	leafKeys map[string]Hash // path.String() -> full key hash
}

// New creates a new empty BMT with the given store.
func New(s NodeStore) *Tree {
	return &Tree{
		dirty:    make(map[string]NodeValue),
		store:    s,
		version:  0,
		leafKeys: make(map[string]Hash),
	}
}

// NewWithVersion creates a BMT at a specific version for historical reads.
func NewWithVersion(s NodeStore, version uint64) *Tree {
	return &Tree{
		dirty:    make(map[string]NodeValue),
		store:    s,
		version:  version,
		leafKeys: make(map[string]Hash),
	}
}

// Root returns the current root hash of the tree.
// Returns EmptyHash for an empty tree.
func (t *Tree) Root() Hash {
	root := EmptyPath()
	val, err := t.getNode(root)
	if err != nil || val == nil {
		return EmptyHash
	}
	return toHash(val)
}

// Get retrieves the value associated with the given key hash.
// Returns ErrNotFound if the key is not in the tree.
func (t *Tree) Get(keyHash Hash) ([]byte, error) {
	path := FromKeyHash(keyHash)
	current := EmptyPath()

	for depth := 0; depth < MaxDepth; depth++ {
		val, err := t.getNode(current)
		if err != nil {
			return nil, ErrNotFound
		}
		if val == nil {
			return nil, ErrNotFound
		}

		// Check if this is a leaf (not a hash pointer = not an internal node)
		if !val.IsHashPointer() {
			// It's a leaf. Verify this is actually the key we're looking for.
			storedKey, ok := t.leafKeys[current.String()]
			if !ok {
				return nil, ErrNotFound
			}
			if storedKey != keyHash {
				return nil, ErrNotFound
			}
			return decodeLeafValue(val), nil
		}

		// Internal node: descend based on the bit at this depth
		bit := path.Bit(depth)
		current = current.Append(bit)
	}

	return nil, ErrNotFound
}

// Put inserts or updates a key-value pair in the tree.
func (t *Tree) Put(keyHash Hash, value []byte) error {
	path := FromKeyHash(keyHash)
	encoded := encodeLeafValue(value)
	current := EmptyPath()

	for depth := 0; depth < MaxDepth; depth++ {
		val, err := t.getNode(current)
		if err != nil || val == nil {
			// Empty slot: place the leaf here
			t.setNode(current, encoded)
			t.leafKeys[current.String()] = keyHash
			t.updateAncestors(current)
			return nil
		}

		// Check if this is a leaf
		if !val.IsHashPointer() {
			// Existing leaf at this path. Check if it's the same key.
			existingKey, ok := t.leafKeys[current.String()]
			if !ok {
				// Shouldn't happen, but treat as empty
				t.setNode(current, encoded)
				t.leafKeys[current.String()] = keyHash
				t.updateAncestors(current)
				return nil
			}

			if existingKey == keyHash {
				// Same key: update value in place
				t.setNode(current, encoded)
				t.updateAncestors(current)
				return nil
			}

			// Different key: need to split. Remove existing leaf and
			// create internal nodes until the two keys diverge.
			existingPath := FromKeyHash(existingKey)
			existingValue := val

			// Remove the existing leaf from this position
			t.deleteNode(current)
			delete(t.leafKeys, current.String())

			// Find the divergence point and create internal nodes
			splitAt := current
			for d := depth; d < MaxDepth; d++ {
				existingBit := existingPath.Bit(d)
				newBit := path.Bit(d)

				if existingBit != newBit {
					// They diverge here: place both leaves
					existingChild := splitAt.Append(existingBit)
					newChild := splitAt.Append(newBit)

					t.setNode(existingChild, existingValue)
					t.leafKeys[existingChild.String()] = existingKey

					t.setNode(newChild, encoded)
					t.leafKeys[newChild.String()] = keyHash

					// Create internal node at splitAt
					t.computeInternalNode(splitAt)

					// Update all ancestors from splitAt up to root
					t.updateAncestors(splitAt)
					return nil
				}

				// Same bit: create an internal node and go deeper
				splitAt = splitAt.Append(existingBit)
			}

			// Keys are identical (should never happen since we checked above)
			return nil
		}

		// Internal node: descend
		bit := path.Bit(depth)
		current = current.Append(bit)
	}

	return nil
}

// Delete removes a key from the tree.
func (t *Tree) Delete(keyHash Hash) error {
	path := FromKeyHash(keyHash)
	current := EmptyPath()

	// Find the leaf
	for depth := 0; depth < MaxDepth; depth++ {
		val, err := t.getNode(current)
		if err != nil || val == nil {
			return ErrNotFound
		}

		if !val.IsHashPointer() {
			// Leaf node
			storedKey, ok := t.leafKeys[current.String()]
			if !ok || storedKey != keyHash {
				return ErrNotFound
			}

			// Remove the leaf
			t.deleteNode(current)
			delete(t.leafKeys, current.String())

			// Collapse single-child ancestors
			t.collapseAncestors(current)
			return nil
		}

		// Internal node: descend
		bit := path.Bit(depth)
		current = current.Append(bit)
	}

	return ErrNotFound
}

// FlushTo writes all dirty entries to the target store and clears the dirty map.
func (t *Tree) FlushTo(target NodeStore) error {
	for pathStr, val := range t.dirty {
		p := decodePathString(pathStr)
		if val == nil {
			if err := target.Delete(t.version, p); err != nil {
				return err
			}
		} else {
			if err := target.Put(t.version, p, val); err != nil {
				return err
			}
		}
	}
	t.dirty = make(map[string]NodeValue)
	return nil
}

// Version returns the current version of the tree.
func (t *Tree) Version() uint64 {
	return t.version
}

// SetVersion sets the tree version (typically block height).
func (t *Tree) SetVersion(v uint64) {
	t.version = v
}

// getNode reads a node, checking dirty map first, then store.
func (t *Tree) getNode(p Path) (NodeValue, error) {
	key := p.String()
	if val, ok := t.dirty[key]; ok {
		return val, nil // may be nil (tombstone)
	}
	return t.store.Get(t.version, p)
}

// setNode writes a node to the dirty map.
func (t *Tree) setNode(p Path, val NodeValue) {
	t.dirty[p.String()] = val
}

// deleteNode marks a node as deleted in the dirty map.
func (t *Tree) deleteNode(p Path) {
	t.dirty[p.String()] = nil
}

// computeInternalNode computes the hash of an internal node from its children.
func (t *Tree) computeInternalNode(p Path) {
	leftChild := p.Append(0)
	rightChild := p.Append(1)

	leftVal, leftErr := t.getNode(leftChild)
	rightVal, rightErr := t.getNode(rightChild)

	var leftBytes, rightBytes []byte
	if leftErr == nil && leftVal != nil {
		leftBytes = leftVal
	} else {
		leftBytes = EmptyHash[:]
	}
	if rightErr == nil && rightVal != nil {
		rightBytes = rightVal
	} else {
		rightBytes = EmptyHash[:]
	}

	hash := HashNode(leftBytes, rightBytes)
	t.setNode(p, NodeValue(hash[:]))
}

// updateAncestors recomputes internal node hashes from a node up to the root.
func (t *Tree) updateAncestors(p Path) {
	// Walk from p's parent up to the root
	for depth := p.BitLen - 1; depth >= 0; depth-- {
		parent := p.Prefix(depth)
		t.computeInternalNode(parent)
		p = parent
	}
}

// collapseAncestors handles deletion cleanup: after removing a leaf,
// if its sibling is the only child, promote the sibling upward.
func (t *Tree) collapseAncestors(deletedPath Path) {
	current := deletedPath
	for current.BitLen > 0 {
		parent := current.Prefix(current.BitLen - 1)
		sibling := current.Sibling()

		sibVal, sibErr := t.getNode(sibling)
		if sibErr != nil || sibVal == nil {
			// No sibling: remove parent too, keep collapsing
			t.deleteNode(parent)
			delete(t.leafKeys, parent.String())
			current = parent
			continue
		}

		// Sibling exists. If it's a leaf, promote it to parent.
		if !sibVal.IsHashPointer() {
			sibKey, ok := t.leafKeys[sibling.String()]
			if ok {
				// Move sibling leaf up to parent
				t.setNode(parent, sibVal)
				t.leafKeys[parent.String()] = sibKey

				// Remove sibling from old position
				t.deleteNode(sibling)
				delete(t.leafKeys, sibling.String())

				// Continue collapsing upward
				current = parent
				continue
			}
		}

		// Sibling is an internal node or we can't collapse further.
		// Recompute parent and all ancestors.
		t.updateAncestors(current)
		return
	}

	// We collapsed all the way to root — update root if needed
	if current.BitLen == 0 {
		// Check if root still has a value
		rootVal, err := t.getNode(EmptyPath())
		if err != nil || rootVal == nil {
			// Tree is empty
			return
		}
	}
}

// encodeLeafValue encodes leaf data for storage.
// Values exactly 32 bytes get a 0x01 tag appended to distinguish from internal nodes.
func encodeLeafValue(value []byte) NodeValue {
	if len(value) == HashSize {
		result := make(NodeValue, HashSize+1)
		copy(result, value)
		result[HashSize] = 0x01 // tag byte
		return result
	}
	result := make(NodeValue, len(value))
	copy(result, value)
	return result
}

// decodeLeafValue decodes a stored leaf back to the original value.
func decodeLeafValue(v NodeValue) []byte {
	if len(v) == HashSize+1 && v[HashSize] == 0x01 {
		// Tagged 32-byte value
		result := make([]byte, HashSize)
		copy(result, v[:HashSize])
		return result
	}
	result := make([]byte, len(v))
	copy(result, v)
	return result
}

// decodePathString reconstructs a Path from its String() representation.
func decodePathString(s string) Path {
	if len(s) == 0 {
		return EmptyPath()
	}
	b := []byte(s)
	bitLen := int(b[0])
	pathBytes := make([]byte, len(b)-1)
	copy(pathBytes, b[1:])
	return Path{Bytes: pathBytes, BitLen: bitLen}
}
