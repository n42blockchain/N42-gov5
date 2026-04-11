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
// Merkle inclusion and exclusion proofs for the JMT. ProofEntry records
// one serialized node and the nibble taken on descent (-1 marks the
// terminal leaf entry). Proof groups the queried key hash, an optional
// value and the full Path from root down. Tree.GetProof walks from
// t.root through NibblePath positions, serializing each visited node via
// EncodeNode, so a caller receives enough data to independently re-hash
// the branch and verify inclusion or the point of divergence.

package jmt

// ProofEntry is one step along a Merkle proof path.
type ProofEntry struct {
	// NodeData is the serialized node at this level.
	NodeData []byte
	// Nibble is the child index taken at this internal/extension node.
	// -1 for the terminal (leaf) entry.
	Nibble int8
}

// Proof is a Merkle inclusion/exclusion proof for a single key.
type Proof struct {
	// Key is the queried key hash.
	Key Hash
	// Value is the value if found (nil for exclusion proofs).
	Value []byte
	// Path contains serialized nodes from the root down to the leaf (or
	// the point of divergence for exclusion proofs).
	Path []ProofEntry
}

// GetProof generates a Merkle proof for the given key.
// If the key exists, an inclusion proof is returned with Value set.
// If not, an exclusion proof is returned with Value == nil.
func (t *Tree) GetProof(keyHash Hash) (*Proof, error) {
	proof := &Proof{Key: keyHash}
	if t.root == EmptyHash {
		return proof, nil
	}
	path := NewNibblePath(keyHash)
	err := t.buildProof(t.root, path, 0, proof)
	if err != nil {
		return nil, err
	}
	return proof, nil
}

func (t *Tree) buildProof(nodeHash Hash, path NibblePath, depth int, proof *Proof) error {
	node, err := t.loadNode(nodeHash)
	if err != nil {
		return err
	}
	encoded := EncodeNode(node)

	switch node.Type {
	case NodeTypeLeaf:
		proof.Path = append(proof.Path, ProofEntry{NodeData: encoded, Nibble: -1})
		if node.Leaf.KeyHash == path.data {
			proof.Value = node.Leaf.Value
		}
		return nil

	case NodeTypeInternal:
		nibble := path.At(depth)
		proof.Path = append(proof.Path, ProofEntry{NodeData: encoded, Nibble: int8(nibble)})
		child := node.Internal.Children[nibble]
		if !child.Valid {
			return nil // exclusion: path ends at empty slot
		}
		return t.buildProof(child.Hash, path, depth+1, proof)

	case NodeTypeExtension:
		ext := node.Extension
		extLen := ext.Path.Len()
		proof.Path = append(proof.Path, ProofEntry{NodeData: encoded, Nibble: 0})
		for i := 0; i < extLen; i++ {
			if depth+i >= path.Len() || path.At(depth+i) != ext.Path.At(i) {
				return nil // exclusion: diverges within extension
			}
		}
		return t.buildProof(ext.Child, path, depth+extLen, proof)

	default:
		return ErrInvalidNode
	}
}

// ExtractProofNodes returns all intermediate nodes from the proof path
// as (hash -> encoded_node) pairs. These can be used to seed a partial
// JMT tree for post-state root computation in stateless execution.
func ExtractProofNodes(proof *Proof, h Hasher) map[Hash][]byte {
	nodes := make(map[Hash][]byte, len(proof.Path))
	for _, entry := range proof.Path {
		nodeHash := h.Hash(entry.NodeData)
		nodes[nodeHash] = entry.NodeData
	}
	return nodes
}

// VerifyProof verifies a Merkle proof against a known root hash.
// Returns the value if the proof is a valid inclusion proof, or nil + nil
// if it is a valid exclusion proof. Returns an error if the proof is invalid.
func VerifyProof(root Hash, proof *Proof, h Hasher) ([]byte, error) {
	if len(proof.Path) == 0 {
		if root == EmptyHash {
			return nil, nil // valid exclusion on empty tree
		}
		return nil, ErrInvalidProof
	}

	// Recompute the root by walking the proof path bottom-up.
	// First, verify the chain: each node's hash must match what its parent references.
	computedHashes := make([]Hash, len(proof.Path))
	for i := range proof.Path {
		computedHashes[i] = h.Hash(proof.Path[i].NodeData)
	}

	// Verify parent → child hash linkage.
	for i := 0; i < len(proof.Path)-1; i++ {
		entry := proof.Path[i]
		node, err := DecodeNode(entry.NodeData)
		if err != nil {
			return nil, ErrInvalidProof
		}
		expectedChild := computedHashes[i+1]
		switch node.Type {
		case NodeTypeInternal:
			nibble := entry.Nibble
			if nibble < 0 || nibble >= int8(ChildCount) {
				return nil, ErrInvalidProof
			}
			child := node.Internal.Children[nibble]
			if !child.Valid || child.Hash != expectedChild {
				return nil, ErrInvalidProof
			}
		case NodeTypeExtension:
			if node.Extension.Child != expectedChild {
				return nil, ErrInvalidProof
			}
		default:
			return nil, ErrInvalidProof
		}
	}

	// Root must match.
	if computedHashes[0] != root {
		return nil, ErrInvalidProof
	}

	keyPath := NewNibblePath(proof.Key)
	depth := 0

	for i, entry := range proof.Path {
		node, err := DecodeNode(entry.NodeData)
		if err != nil {
			return nil, ErrInvalidProof
		}

		isLast := i == len(proof.Path)-1
		switch node.Type {
		case NodeTypeInternal:
			if depth >= keyPath.Len() {
				return nil, ErrInvalidProof
			}

			expectedNibble := keyPath.At(depth)
			if entry.Nibble != int8(expectedNibble) {
				return nil, ErrInvalidProof
			}

			child := node.Internal.Children[expectedNibble]
			if !child.Valid {
				if !isLast {
					return nil, ErrInvalidProof
				}
				return nil, nil
			}
			if isLast {
				return nil, ErrInvalidProof
			}
			depth++

		case NodeTypeExtension:
			ext := node.Extension
			extLen := ext.Path.Len()
			match := true
			for j := 0; j < extLen; j++ {
				if depth+j >= keyPath.Len() || keyPath.At(depth+j) != ext.Path.At(j) {
					match = false
					break
				}
			}
			if !match {
				if !isLast {
					return nil, ErrInvalidProof
				}
				return nil, nil
			}
			if isLast {
				return nil, ErrInvalidProof
			}
			depth += extLen

		case NodeTypeLeaf:
			if !isLast {
				return nil, ErrInvalidProof
			}
			if node.Leaf.KeyHash == proof.Key {
				return node.Leaf.Value, nil
			}
			return nil, nil

		default:
			return nil, ErrInvalidProof
		}
	}

	return nil, ErrInvalidProof
}
