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

// Proof is a Merkle inclusion proof for a key-value pair.
type Proof struct {
	Key      Hash   // the key hash
	Value    []byte // the leaf value (nil for absence proof)
	Siblings []Hash // sibling hashes from leaf to root
	Depth    int    // number of levels traversed
}

// GetProof generates a Merkle inclusion proof by traversing hash pointers
// from root to leaf, collecting sibling hashes along the way.
func (t *Tree) GetProof(keyHash Hash) (*Proof, error) {
	path := FromKeyHash(keyHash)
	current := t.root
	var siblings []Hash

	for depth := 0; depth < MaxDepth; depth++ {
		if current == EmptyHash {
			return &Proof{Key: keyHash, Siblings: siblings, Depth: depth}, ErrNotFound
		}
		data, err := t.getNode(current)
		if err != nil {
			return &Proof{Key: keyHash, Siblings: siblings, Depth: depth}, ErrNotFound
		}
		if isLeaf(data) {
			storedKey, ok := extractLeafKeyHash(data)
			if !ok || storedKey != keyHash {
				return nil, ErrNotFound
			}
			return &Proof{
				Key:      keyHash,
				Value:    decodeLeafValue(data),
				Siblings: siblings,
				Depth:    depth,
			}, nil
		}
		// Internal: collect sibling, descend
		left, right := decodeInternalNode(data)
		if path.Bit(depth) == 0 {
			siblings = append(siblings, right)
			current = left
		} else {
			siblings = append(siblings, left)
			current = right
		}
	}
	return nil, ErrNotFound
}

// VerifyProof checks a Merkle proof against a known root hash.
func VerifyProof(root Hash, proof *Proof) bool {
	if proof == nil || proof.Value == nil {
		return false
	}
	path := FromKeyHash(proof.Key)

	// Start with the leaf hash
	encoded := encodeLeafValue(proof.Key, proof.Value)
	current := toHash(encoded)

	// Walk from leaf back to root using siblings
	for i := len(proof.Siblings) - 1; i >= 0; i-- {
		if path.Bit(i) == 0 {
			current = HashNode(current[:], proof.Siblings[i][:])
		} else {
			current = HashNode(proof.Siblings[i][:], current[:])
		}
	}
	return current == root
}
