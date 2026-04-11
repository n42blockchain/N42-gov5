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
// Reference-counting garbage collection for content-addressed JMT nodes.
// Because nodes are keyed by blake3(serialized), a mutation that rewrites
// a subtree leaves the old node behind; NodeGC tracks a per-hash ref map
// and a pendingDelete list to reclaim unreachable state. IncRef / DecRef
// are threaded through Tree mutations and CollectGarbage drains the
// pending set against the store — the JMT counterpart to geth PBSS-style
// online trie pruning, adapted for hash-indexed (not path-indexed) storage.

package jmt

// NodeGC implements reference-counting garbage collection for JMT nodes.
//
// Problem: JMT nodes are content-addressed (keyed by blake3(serialized)).
// When a tree mutation replaces a node at a given path, the old node remains
// in the store because its hash-key is different from the new node's key.
// Without cleanup, the node store grows unboundedly.
//
// Solution: Track reference counts. Each node is referenced by its parent's
// ChildEntry.Hash or Extension.Child field. When a mutation replaces a child
// hash, the old hash loses a reference. When the ref count reaches zero, the
// node and its entire subtree can be deleted.
//
// This is the JMT equivalent of geth's "online trie pruning" (PBSS), adapted
// for content-addressed (hash-indexed) storage rather than path-indexed.
//
// Usage:
//
//	gc := NewNodeGC()
//	// During tree mutations, call gc.IncRef/DecRef as nodes are created/replaced.
//	// After FlushTo, call gc.CollectGarbage(store) to delete unreachable nodes.
type NodeGC struct {
	// refs maps node hash → reference count.
	// A newly stored node starts with refcount 1.
	// When a parent replaces a child hash, the old child is DecRef'd.
	refs map[Hash]int32

	// pendingDelete accumulates hashes whose refcount reached zero.
	// These are collected in batch during CollectGarbage.
	pendingDelete []Hash
}

// NewNodeGC creates a new GC tracker.
func NewNodeGC() *NodeGC {
	return &NodeGC{
		refs: make(map[Hash]int32, 1024),
	}
}

// IncRef increments the reference count for a node hash.
// Called when a new node is stored or when a parent references a child.
func (gc *NodeGC) IncRef(h Hash) {
	if h == EmptyHash {
		return
	}
	gc.refs[h]++
}

// DecRef decrements the reference count for a node hash.
// If the count reaches zero, the hash is queued for deletion.
func (gc *NodeGC) DecRef(h Hash) {
	if h == EmptyHash {
		return
	}
	gc.refs[h]--
	if gc.refs[h] <= 0 {
		delete(gc.refs, h)
		gc.pendingDelete = append(gc.pendingDelete, h)
	}
}

// PendingDeleteCount returns the number of nodes queued for deletion.
func (gc *NodeGC) PendingDeleteCount() int {
	return len(gc.pendingDelete)
}

// TrackedNodeCount returns the number of nodes with active references.
func (gc *NodeGC) TrackedNodeCount() int {
	return len(gc.refs)
}

// CollectGarbage deletes all nodes with zero references from the store.
// For each deleted internal/extension node, it recursively decrements
// references to child nodes, potentially triggering cascade deletions.
//
// Returns the number of nodes deleted.
func (gc *NodeGC) CollectGarbage(store NodeStore) (int, error) {
	deleted := 0

	for len(gc.pendingDelete) > 0 {
		// Pop batch from pending list
		batch := gc.pendingDelete
		gc.pendingDelete = nil

		for _, h := range batch {
			// Load the node to find its children (for cascading DecRef)
			data, err := store.Get(h)
			if err != nil {
				// Node already deleted or never persisted — skip
				continue
			}

			// Decode to find child references
			node, err := DecodeNode(data)
			if err == nil {
				gc.decRefChildren(node)
			}

			// Delete the node from store
			if err := store.Delete(h); err != nil {
				return deleted, err
			}
			deleted++
		}
	}

	return deleted, nil
}

// decRefChildren decrements references for all children of a node.
func (gc *NodeGC) decRefChildren(node *Node) {
	switch node.Type {
	case NodeTypeInternal:
		for _, child := range node.Internal.Children {
			if child.Valid {
				gc.DecRef(child.Hash)
			}
		}
	case NodeTypeExtension:
		gc.DecRef(node.Extension.Child)
	case NodeTypeLeaf:
		// Leaves have no children
	}
}

// Reset clears all tracking state.
func (gc *NodeGC) Reset() {
	gc.refs = make(map[Hash]int32, 1024)
	gc.pendingDelete = nil
}
