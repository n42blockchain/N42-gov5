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

// GetProof generates a Merkle inclusion proof for the given key hash.
// Returns the proof with sibling hashes at each level from the leaf to the root.
func (t *Tree) GetProof(keyHash Hash) (*Proof, error) {
	path := FromKeyHash(keyHash)
	current := EmptyPath()

	var siblings []Hash

	for depth := 0; depth < MaxDepth; depth++ {
		val, err := t.getNode(current)
		if err != nil || val == nil {
			// Key not found: return absence proof with siblings collected so far
			return &Proof{
				Key:      keyHash,
				Value:    nil,
				Siblings: siblings,
				Depth:    depth,
			}, ErrNotFound
		}

		// Check if this is a leaf
		if !val.IsHashPointer() {
			storedKey, ok := t.leafKeys[current.String()]
			if !ok || storedKey != keyHash {
				return nil, ErrNotFound
			}
			return &Proof{
				Key:      keyHash,
				Value:    decodeLeafValue(val),
				Siblings: siblings,
				Depth:    depth,
			}, nil
		}

		// Internal node: collect sibling hash and descend
		bit := path.Bit(depth)
		var siblingPath Path
		if bit == 0 {
			siblingPath = current.Append(1)
		} else {
			siblingPath = current.Append(0)
		}

		sibVal, sibErr := t.getNode(siblingPath)
		if sibErr != nil || sibVal == nil {
			siblings = append(siblings, EmptyHash)
		} else {
			siblings = append(siblings, toHash(sibVal))
		}

		current = current.Append(bit)
	}

	return nil, ErrNotFound
}

// VerifyProof verifies a Merkle inclusion proof against a known root hash.
func VerifyProof(root Hash, proof *Proof) bool {
	if proof == nil || proof.Value == nil {
		return false
	}

	path := FromKeyHash(proof.Key)

	// Start with the leaf hash
	encoded := encodeLeafValue(proof.Value)
	current := toHash(encoded)

	// Walk from leaf back to root using siblings
	for i := len(proof.Siblings) - 1; i >= 0; i-- {
		depth := i
		bit := path.Bit(depth)
		if bit == 0 {
			current = HashNode(current[:], proof.Siblings[i][:])
		} else {
			current = HashNode(proof.Siblings[i][:], current[:])
		}
	}

	return current == root
}
