// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Chunk merkle tree for storage proofs. Data is split into fixed-size
// chunks; the manifest commits to a binary keccak256 merkle root over the
// chunk hashes. A storage proof is the chunk bytes plus the merkle path to
// the root: a provider that does not physically hold chunk i cannot produce
// a path that hashes to the committed root, which is the whole point of the
// random-spot-check challenge (design doc §5).

package deal

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// leafHash domain-separates a chunk leaf from an interior node so a proof
// cannot reinterpret one as the other (second-preimage hardening).
func leafHash(chunk []byte) types.Hash {
	return crypto.Keccak256Hash([]byte{0x00}, chunk)
}

func nodeHash(l, r types.Hash) types.Hash {
	return crypto.Keccak256Hash([]byte{0x01}, l[:], r[:])
}

// MerkleProof is the path from a chunk leaf to the root.
type MerkleProof struct {
	Index    int          `json:"index"`    // chunk index proven
	Siblings []types.Hash `json:"siblings"` // bottom-up sibling hashes
	// Right[k] is true if the sibling at level k is the RIGHT node (i.e. the
	// proven node is the left child at that level).
	Right []bool `json:"right"`
}

// merkleTree holds the full tree so proofs can be generated. Levels[0] is the
// leaf level; Levels[len-1] is the single-node root level.
type merkleTree struct {
	levels [][]types.Hash
}

// buildMerkle builds a tree over the chunk hashes. Odd levels duplicate the
// last node (standard promotion) so the tree is well-defined for any count.
func buildMerkle(leaves []types.Hash) *merkleTree {
	if len(leaves) == 0 {
		return &merkleTree{levels: [][]types.Hash{{types.Hash{}}}}
	}
	levels := [][]types.Hash{leaves}
	cur := leaves
	for len(cur) > 1 {
		next := make([]types.Hash, 0, (len(cur)+1)/2)
		for i := 0; i < len(cur); i += 2 {
			left := cur[i]
			right := left
			if i+1 < len(cur) {
				right = cur[i+1]
			}
			next = append(next, nodeHash(left, right))
		}
		levels = append(levels, next)
		cur = next
	}
	return &merkleTree{levels: levels}
}

func (t *merkleTree) root() types.Hash {
	top := t.levels[len(t.levels)-1]
	return top[0]
}

// proof generates a merkle proof for chunk index i.
func (t *merkleTree) proof(i int) (MerkleProof, error) {
	if i < 0 || i >= len(t.levels[0]) {
		return MerkleProof{}, fmt.Errorf("deal merkle: chunk index %d out of range", i)
	}
	var sib []types.Hash
	var right []bool
	idx := i
	for level := 0; level < len(t.levels)-1; level++ {
		nodes := t.levels[level]
		if idx%2 == 0 {
			// proven node is left child; sibling is right (or itself if odd tail)
			sibIdx := idx + 1
			if sibIdx >= len(nodes) {
				sibIdx = idx // duplicated last node
			}
			sib = append(sib, nodes[sibIdx])
			right = append(right, true)
		} else {
			sib = append(sib, nodes[idx-1])
			right = append(right, false)
		}
		idx /= 2
	}
	return MerkleProof{Index: i, Siblings: sib, Right: right}, nil
}

// VerifyChunk checks that chunk is the data at proof.Index under root.
func VerifyChunk(root types.Hash, chunk []byte, proof MerkleProof) bool {
	if len(proof.Siblings) != len(proof.Right) {
		return false
	}
	h := leafHash(chunk)
	for k, sib := range proof.Siblings {
		if proof.Right[k] {
			h = nodeHash(h, sib) // proven node on the left
		} else {
			h = nodeHash(sib, h)
		}
	}
	return h == root
}

// chunkData splits data into fixed-size chunks (last chunk may be short).
func chunkData(data []byte, chunkSize int) [][]byte {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if len(data) == 0 {
		return [][]byte{{}}
	}
	var chunks [][]byte
	for off := 0; off < len(data); off += chunkSize {
		end := off + chunkSize
		if end > len(data) {
			end = len(data)
		}
		cp := make([]byte, end-off)
		copy(cp, data[off:end])
		chunks = append(chunks, cp)
	}
	return chunks
}

// chunkHashes returns the leaf hashes for a chunk set.
func chunkHashes(chunks [][]byte) []types.Hash {
	hs := make([]types.Hash, len(chunks))
	for i, c := range chunks {
		hs[i] = leafHash(c)
	}
	return hs
}
