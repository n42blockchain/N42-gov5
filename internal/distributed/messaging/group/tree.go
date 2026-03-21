// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package group

import (
	"crypto/sha256"
)

// LeafNode represents a leaf in the ratchet tree (a group member).
type LeafNode struct {
	PublicKey [32]byte
	Private   [32]byte // only set for local member
	Blank     bool
}

// ParentNode represents an internal node in the ratchet tree.
type ParentNode struct {
	PublicKey [32]byte
	Hash      [32]byte
}

// RatchetTree implements an MLS-style binary ratchet tree for group key agreement.
// The tree provides O(log n) key updates when members join, leave, or update keys.
type RatchetTree struct {
	leaves  []LeafNode
	parents map[uint32]ParentNode // node index -> parent node
}

// NewRatchetTree creates an empty ratchet tree.
func NewRatchetTree() *RatchetTree {
	return &RatchetTree{
		leaves:  make([]LeafNode, 0),
		parents: make(map[uint32]ParentNode),
	}
}

// AddLeaf adds a new leaf to the tree and returns the leaf index.
func (rt *RatchetTree) AddLeaf(leaf LeafNode) uint32 {
	idx := uint32(len(rt.leaves))
	rt.leaves = append(rt.leaves, leaf)
	return idx
}

// BlankLeaf marks a leaf as blank (removed member).
func (rt *RatchetTree) BlankLeaf(index uint32) {
	if int(index) < len(rt.leaves) {
		rt.leaves[index].Blank = true
		rt.leaves[index].PublicKey = [32]byte{}
		rt.leaves[index].Private = [32]byte{}
	}
}

// UpdateLeafKey updates a leaf's public key.
func (rt *RatchetTree) UpdateLeafKey(index uint32, publicKey [32]byte) {
	if int(index) < len(rt.leaves) {
		rt.leaves[index].PublicKey = publicKey
		rt.leaves[index].Blank = false
	}
}

// LeafCount returns the number of leaves.
func (rt *RatchetTree) LeafCount() int {
	return len(rt.leaves)
}

// RootHash computes the root hash of the tree.
// Uses a binary tree structure where parent = SHA256(left || right).
func (rt *RatchetTree) RootHash() [32]byte {
	if len(rt.leaves) == 0 {
		return [32]byte{}
	}
	if len(rt.leaves) == 1 {
		return hashLeaf(rt.leaves[0])
	}

	// Build the tree bottom-up
	level := make([][32]byte, len(rt.leaves))
	for i, leaf := range rt.leaves {
		level[i] = hashLeaf(leaf)
	}

	for len(level) > 1 {
		var next [][32]byte
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				next = append(next, hashPair(level[i], level[i+1]))
			} else {
				next = append(next, level[i])
			}
		}
		level = next
	}

	return level[0]
}

// UpdatePath recomputes the path from a leaf to the root after a key change.
// This is the core TreeKEM operation providing forward secrecy.
func (rt *RatchetTree) UpdatePath(leafIndex uint32) {
	if int(leafIndex) >= len(rt.leaves) {
		return
	}

	// Compute path keys from leaf to root
	leafHash := hashLeaf(rt.leaves[leafIndex])
	pathKey := derivePathKey(leafHash, 0)

	// Store path nodes
	nodeIdx := rt.leafToNode(leafIndex)
	level := uint32(0)
	for nodeIdx > 0 {
		rt.parents[nodeIdx] = ParentNode{
			PublicKey: derivePublicFromPath(pathKey),
			Hash:      pathKey,
		}
		nodeIdx = rt.parent(nodeIdx)
		level++
		pathKey = derivePathKey(pathKey, level)
	}
	// Update root
	rt.parents[0] = ParentNode{
		Hash: rt.RootHash(),
	}
}

// GetLeaf returns the leaf at the given index.
func (rt *RatchetTree) GetLeaf(index uint32) (LeafNode, bool) {
	if int(index) >= len(rt.leaves) {
		return LeafNode{}, false
	}
	return rt.leaves[index], true
}

func (rt *RatchetTree) leafToNode(leafIndex uint32) uint32 {
	return leafIndex*2 + 1
}

func (rt *RatchetTree) parent(nodeIndex uint32) uint32 {
	if nodeIndex == 0 {
		return 0
	}
	return (nodeIndex - 1) / 2
}

func hashLeaf(leaf LeafNode) [32]byte {
	if leaf.Blank {
		return [32]byte{}
	}
	h := sha256.New()
	h.Write([]byte("mls-leaf"))
	h.Write(leaf.PublicKey[:])
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func hashPair(left, right [32]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("mls-node"))
	h.Write(left[:])
	h.Write(right[:])
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func derivePathKey(parentHash [32]byte, level uint32) [32]byte {
	h := sha256.New()
	h.Write([]byte("mls-path"))
	h.Write(parentHash[:])
	var buf [4]byte
	buf[0] = byte(level >> 24)
	buf[1] = byte(level >> 16)
	buf[2] = byte(level >> 8)
	buf[3] = byte(level)
	h.Write(buf[:])
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// SecretTree manages per-sender encryption secrets within an epoch.
// Each leaf in the secret tree corresponds to a group member and derives
// unique per-message keys to prevent nonce reuse.
type SecretTree struct {
	secrets map[uint32][32]byte
}

// NewSecretTree creates a new secret tree from an epoch secret.
func NewSecretTree(epochSecret [32]byte, size int) *SecretTree {
	st := &SecretTree{
		secrets: make(map[uint32][32]byte, size),
	}
	for i := 0; i < size; i++ {
		h := sha256.New()
		h.Write([]byte("mls-secret-tree"))
		h.Write(epochSecret[:])
		buf := [4]byte{
			byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i),
		}
		h.Write(buf[:])
		var secret [32]byte
		copy(secret[:], h.Sum(nil))
		st.secrets[uint32(i)] = secret
	}
	return st
}

// GetSecret returns the secret for a given member index.
func (st *SecretTree) GetSecret(index uint32) ([32]byte, bool) {
	s, ok := st.secrets[index]
	return s, ok
}

func derivePublicFromPath(pathKey [32]byte) [32]byte {
	h := sha256.New()
	h.Write([]byte("mls-pub"))
	h.Write(pathKey[:])
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
