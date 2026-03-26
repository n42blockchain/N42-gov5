package bridge

import (
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/jmt"
)

// StateInclusionProof proves that a specific key-value pair exists in the
// N42 JMT state trie at a given state root. The proof is a compact Merkle
// path that can be verified on-chain without re-executing the block.
//
// Trust chain: verified StateRoot (from HeaderChainProof) → JMT Merkle proof
type StateInclusionProof struct {
	StateRoot types.Hash  // The state root this proof is anchored to
	Key       []byte      // Account address (20B) or address||slot (52B)
	Value     []byte      // Account data or storage value
	JMTProof  *jmt.Proof  // Compact Merkle path from leaf to root
}

// ProveStateInclusion generates a JMT inclusion proof for the given key
// at the specified state root. The proof can be verified by any party
// with access to the state root (e.g., from a HeaderChainProof).
func ProveStateInclusion(
	tree *jmt.Tree,
	stateRoot types.Hash,
	key []byte,
) (*StateInclusionProof, error) {
	if tree == nil {
		return nil, fmt.Errorf("JMT tree is nil")
	}

	// Compute key hash (JMT uses hashed keys)
	keyHash := jmt.HashKey(key)

	proof, err := tree.GetProof(keyHash)
	if err != nil {
		return nil, fmt.Errorf("get JMT proof: %w", err)
	}

	stateProofsGenerated.Inc()

	return &StateInclusionProof{
		StateRoot: stateRoot,
		Key:       key,
		Value:     proof.Value,
		JMTProof:  proof,
	}, nil
}

// VerifyStateInclusion verifies a state inclusion proof against a known
// state root. Returns the value if valid, error otherwise.
func VerifyStateInclusion(
	stateRoot types.Hash,
	proof *StateInclusionProof,
) ([]byte, error) {
	if proof == nil || proof.JMTProof == nil {
		return nil, fmt.Errorf("nil proof")
	}

	// Verify the state root matches
	var root jmt.Hash
	copy(root[:], stateRoot[:])

	value, err := jmt.VerifyProof(root, proof.JMTProof, jmt.DefaultHasher())
	if err != nil {
		return nil, fmt.Errorf("JMT proof verification failed: %w", err)
	}

	return value, nil
}

// EncodeStateProof serializes a StateInclusionProof for cross-chain transmission.
// Format: stateRoot(32) + keyLen(4) + key + valueLen(4) + value + proofEntries
func EncodeStateProof(proof *StateInclusionProof) ([]byte, error) {
	if proof == nil {
		return nil, fmt.Errorf("nil proof")
	}

	// Calculate total size
	size := 32 + 4 + len(proof.Key) + 4 + len(proof.Value)
	for _, entry := range proof.JMTProof.Path {
		size += 4 + len(entry.NodeData) + 1 // nodeLen + data + nibble
	}
	size += 4 // path count

	buf := make([]byte, 0, size)

	// State root
	buf = append(buf, proof.StateRoot[:]...)

	// Key
	buf = appendUint32(buf, uint32(len(proof.Key)))
	buf = append(buf, proof.Key...)

	// Value
	buf = appendUint32(buf, uint32(len(proof.Value)))
	buf = append(buf, proof.Value...)

	// Proof path
	buf = appendUint32(buf, uint32(len(proof.JMTProof.Path)))
	for _, entry := range proof.JMTProof.Path {
		buf = appendUint32(buf, uint32(len(entry.NodeData)))
		buf = append(buf, entry.NodeData...)
		buf = append(buf, byte(entry.Nibble))
	}

	return buf, nil
}

func appendUint32(buf []byte, v uint32) []byte {
	return append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}
