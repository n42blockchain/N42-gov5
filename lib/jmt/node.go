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
// Node type definitions for the Jellyfish Merkle Tree. NodeType tags one
// of three shapes: NodeTypeInternal, NodeTypeLeaf or NodeTypeExtension.
// InternalNode holds 16 ChildEntry slots for the nibble branches; LeafNode
// binds a full KeyHash to a ValueHash plus inline Value bytes; and
// ExtensionNode compresses a run of single-child internals into a shared
// NibblePath + child hash. Node is the tagged union used by encoding,
// traversal and hashing code throughout the jmt package.

package jmt

import "encoding/binary"

// NodeType discriminates the three kinds of JMT nodes.
type NodeType byte

const (
	NodeTypeInternal  NodeType = 0x01
	NodeTypeLeaf      NodeType = 0x02
	NodeTypeExtension NodeType = 0x03
)

// Children count for the 16-ary internal node.
const ChildCount = 16

// ChildEntry stores the hash and (optionally cached) pointer for one child.
type ChildEntry struct {
	Hash  Hash
	Valid bool // true if this child slot is occupied
}

// InternalNode is a 16-ary branch in the JMT.
// Each child slot maps to a nibble [0..15].
type InternalNode struct {
	Children [ChildCount]ChildEntry
}

// LeafNode stores a key-value pair at the bottom of the tree.
// KeyHash is the full Blake3 hash of the original key.
// ValueHash is the Blake3 hash of the stored value.
// Value is the raw value bytes (stored inline for small values).
type LeafNode struct {
	KeyHash   Hash
	ValueHash Hash
	Value     []byte
}

// ExtensionNode compresses a chain of single-child internal nodes.
// Path is the shared nibble prefix; Child is the hash of the next node.
type ExtensionNode struct {
	Path  NibblePath
	Child Hash
}

// Node is the union type for all JMT node kinds.
type Node struct {
	Type      NodeType
	Internal  *InternalNode
	Leaf      *LeafNode
	Extension *ExtensionNode
}

// NewInternalNode creates an empty internal node.
func NewInternalNode() *Node {
	return &Node{
		Type:     NodeTypeInternal,
		Internal: &InternalNode{},
	}
}

// NewLeafNode creates a leaf node with the given key hash and value.
func NewLeafNode(keyHash Hash, value []byte, h Hasher) *Node {
	// Defensive copy so the caller cannot mutate JMT state.
	valCopy := make([]byte, len(value))
	copy(valCopy, value)
	return &Node{
		Type: NodeTypeLeaf,
		Leaf: &LeafNode{
			KeyHash:   keyHash,
			ValueHash: h.Hash(valCopy),
			Value:     valCopy,
		},
	}
}

// NewExtensionNode creates an extension node with a shared nibble prefix.
func NewExtensionNode(path NibblePath, child Hash) *Node {
	return &Node{
		Type:      NodeTypeExtension,
		Extension: &ExtensionNode{Path: path, Child: child},
	}
}

// NodeHash computes the content-addressed hash of this node.
func NodeHash(n *Node, h Hasher) Hash {
	return h.Hash(EncodeNode(n))
}

// --- Serialization ---
// Format:
//   Internal:  [0x01][bitmap_u16][child_hash_0]...[child_hash_n]
//   Leaf:      [0x02][key_hash_32][value_len_u32][value...]
//   Extension: [0x03][path_len_u8][path_nibbles...][child_hash_32]

// EncodeNode serializes a node to bytes.
func EncodeNode(n *Node) []byte {
	switch n.Type {
	case NodeTypeInternal:
		return encodeInternal(n.Internal)
	case NodeTypeLeaf:
		return encodeLeaf(n.Leaf)
	case NodeTypeExtension:
		return encodeExtension(n.Extension)
	default:
		panic("unknown node type")
	}
}

func encodeInternal(n *InternalNode) []byte {
	var bitmap uint16
	for i := 0; i < ChildCount; i++ {
		if n.Children[i].Valid {
			bitmap |= 1 << uint(i)
		}
	}
	// 1 byte type + 2 bytes bitmap + 32 bytes per valid child
	popcount := popcount16(bitmap)
	buf := make([]byte, 1+2+popcount*HashSize)
	buf[0] = byte(NodeTypeInternal)
	binary.BigEndian.PutUint16(buf[1:3], bitmap)
	offset := 3
	for i := 0; i < ChildCount; i++ {
		if n.Children[i].Valid {
			copy(buf[offset:offset+HashSize], n.Children[i].Hash[:])
			offset += HashSize
		}
	}
	return buf
}

func encodeLeaf(n *LeafNode) []byte {
	// 1 + 32 (keyHash) + 4 (valueLen) + len(value)
	buf := make([]byte, 1+HashSize+4+len(n.Value))
	buf[0] = byte(NodeTypeLeaf)
	copy(buf[1:1+HashSize], n.KeyHash[:])
	binary.BigEndian.PutUint32(buf[1+HashSize:1+HashSize+4], uint32(len(n.Value)))
	copy(buf[1+HashSize+4:], n.Value)
	return buf
}

func encodeExtension(n *ExtensionNode) []byte {
	nibbles := n.Path.Nibbles()
	// 1 + 1 (pathLen) + len(nibbles) + 32 (childHash)
	buf := make([]byte, 1+1+len(nibbles)+HashSize)
	buf[0] = byte(NodeTypeExtension)
	buf[1] = byte(len(nibbles))
	copy(buf[2:2+len(nibbles)], nibbles)
	copy(buf[2+len(nibbles):], n.Child[:])
	return buf
}

// DecodeNode deserializes a node from bytes.
func DecodeNode(data []byte) (*Node, error) {
	if len(data) == 0 {
		return nil, ErrInvalidNode
	}
	switch NodeType(data[0]) {
	case NodeTypeInternal:
		return decodeInternal(data)
	case NodeTypeLeaf:
		return decodeLeaf(data)
	case NodeTypeExtension:
		return decodeExtension(data)
	default:
		return nil, ErrInvalidNode
	}
}

func decodeInternal(data []byte) (*Node, error) {
	if len(data) < 3 {
		return nil, ErrInvalidNode
	}
	bitmap := binary.BigEndian.Uint16(data[1:3])
	popcount := popcount16(bitmap)
	expected := 3 + popcount*HashSize
	if len(data) < expected {
		return nil, ErrInvalidNode
	}
	n := &InternalNode{}
	offset := 3
	for i := 0; i < ChildCount; i++ {
		if bitmap&(1<<uint(i)) != 0 {
			copy(n.Children[i].Hash[:], data[offset:offset+HashSize])
			n.Children[i].Valid = true
			offset += HashSize
		}
	}
	return &Node{Type: NodeTypeInternal, Internal: n}, nil
}

func decodeLeaf(data []byte) (*Node, error) {
	minLen := 1 + HashSize + 4
	if len(data) < minLen {
		return nil, ErrInvalidNode
	}
	var keyHash Hash
	copy(keyHash[:], data[1:1+HashSize])
	valueLen := binary.BigEndian.Uint32(data[1+HashSize : 1+HashSize+4])
	// Guard against uint32 overflow: minLen + valueLen must not wrap.
	if valueLen > uint32(len(data)) || int(valueLen) > len(data)-minLen {
		return nil, ErrInvalidNode
	}
	value := make([]byte, valueLen)
	copy(value, data[minLen:minLen+int(valueLen)])
	return &Node{
		Type: NodeTypeLeaf,
		Leaf: &LeafNode{
			KeyHash:   keyHash,
			ValueHash: Blake3Hasher{}.Hash(value),
			Value:     value,
		},
	}, nil
}

func decodeExtension(data []byte) (*Node, error) {
	if len(data) < 2 {
		return nil, ErrInvalidNode
	}
	pathLen := int(data[1])
	if len(data) < 2+pathLen+HashSize {
		return nil, ErrInvalidNode
	}
	nibbles := make([]byte, pathLen)
	copy(nibbles, data[2:2+pathLen])
	var childHash Hash
	copy(childHash[:], data[2+pathLen:2+pathLen+HashSize])
	return &Node{
		Type: NodeTypeExtension,
		Extension: &ExtensionNode{
			Path:  NewNibblePathFromSlice(nibbles),
			Child: childHash,
		},
	}, nil
}

// childCount returns the number of occupied children in an internal node.
func (n *InternalNode) childCount() int {
	c := 0
	for i := 0; i < ChildCount; i++ {
		if n.Children[i].Valid {
			c++
		}
	}
	return c
}

// singleChild returns the index and hash of the only child, or -1 if != 1 child.
func (n *InternalNode) singleChild() (int, Hash) {
	idx := -1
	for i := 0; i < ChildCount; i++ {
		if n.Children[i].Valid {
			if idx != -1 {
				return -1, Hash{}
			}
			idx = i
		}
	}
	if idx == -1 {
		return -1, Hash{}
	}
	return idx, n.Children[idx].Hash
}

func popcount16(x uint16) int {
	count := 0
	for x != 0 {
		count++
		x &= x - 1
	}
	return count
}
