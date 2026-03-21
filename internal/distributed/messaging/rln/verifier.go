// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rln

import (
	"fmt"
	"math/big"
	"sync"
)

// Verifier validates RLN proofs and detects spam.
type Verifier struct {
	tree *MembershipTree
}

// NewVerifier creates a new RLN proof verifier.
func NewVerifier(tree *MembershipTree) *Verifier {
	return &Verifier{tree: tree}
}

// VerifyProof validates an RLN proof.
func (v *Verifier) VerifyProof(proof *RLNProof) error {
	if proof == nil || proof.MerkleProof == nil {
		return fmt.Errorf("nil proof")
	}

	// Verify Merkle root matches current tree
	currentRoot := v.tree.Root()
	if proof.MerkleRoot != currentRoot {
		return fmt.Errorf("merkle root mismatch")
	}

	// Verify the Merkle proof for the member's commitment
	idx := proof.MerkleProof.Index
	if int(idx) >= v.tree.Size() {
		return fmt.Errorf("member index out of range")
	}

	// Get the leaf commitment
	v.tree.mu.RLock()
	leaf := v.tree.leaves[idx]
	v.tree.mu.RUnlock()

	if !proof.MerkleProof.Verify(proof.MerkleRoot, leaf) {
		return fmt.Errorf("invalid merkle proof")
	}

	return nil
}

// DetectSpam checks if two proofs from the same epoch+nullifier reveal the identity secret.
// Returns true if spam is detected, along with the recovered identity secret.
func DetectSpam(proof1, proof2 *RLNProof) (bool, [32]byte, error) {
	if proof1.Epoch != proof2.Epoch {
		return false, [32]byte{}, fmt.Errorf("proofs from different epochs")
	}
	if proof1.Nullifier != proof2.Nullifier {
		return false, [32]byte{}, fmt.Errorf("different nullifiers (different identities)")
	}
	if proof1.ShareX == proof2.ShareX {
		return false, [32]byte{}, fmt.Errorf("identical shares (same message)")
	}

	// Shamir secret recovery: given two points (x1, y1) and (x2, y2)
	// on the line y = secret + hash * x
	// secret = (y1 * x2 - y2 * x1) / (x2 - x1)
	x1 := new(big.Int).SetBytes(proof1.ShareX[:])
	y1 := new(big.Int).SetBytes(proof1.ShareY[:])
	x2 := new(big.Int).SetBytes(proof2.ShareX[:])
	y2 := new(big.Int).SetBytes(proof2.ShareY[:])

	// numerator = y1*x2 - y2*x1
	num := new(big.Int).Mul(y1, x2)
	num.Sub(num, new(big.Int).Mul(y2, x1))
	num.Mod(num, fieldPrime)

	// denominator = x2 - x1
	den := new(big.Int).Sub(x2, x1)
	den.Mod(den, fieldPrime)
	if den.Sign() < 0 {
		den.Add(den, fieldPrime)
	}

	// secret = num * den^(-1) mod p
	denInv := new(big.Int).ModInverse(den, fieldPrime)
	if denInv == nil {
		return false, [32]byte{}, fmt.Errorf("cannot compute modular inverse")
	}

	secret := new(big.Int).Mul(num, denInv)
	secret.Mod(secret, fieldPrime)

	var recovered [32]byte
	secretBytes := secret.Bytes()
	copy(recovered[32-len(secretBytes):], secretBytes)

	return true, recovered, nil
}

// NullifierRegistry tracks seen nullifiers per epoch to detect spam.
type NullifierRegistry struct {
	mu       sync.Mutex
	records  map[uint64]map[[32]byte]*RLNProof // epoch -> nullifier -> first proof
	maxEpoch uint64
}

// NewNullifierRegistry creates a new nullifier registry.
func NewNullifierRegistry() *NullifierRegistry {
	return &NullifierRegistry{
		records: make(map[uint64]map[[32]byte]*RLNProof),
	}
}

// Check checks a proof against the registry. Returns the duplicate proof if spam is detected.
func (nr *NullifierRegistry) Check(proof *RLNProof) (*RLNProof, bool) {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	epochMap, ok := nr.records[proof.Epoch]
	if !ok {
		epochMap = make(map[[32]byte]*RLNProof)
		nr.records[proof.Epoch] = epochMap
	}

	existing, duplicate := epochMap[proof.Nullifier]
	if duplicate {
		return existing, true
	}

	epochMap[proof.Nullifier] = proof
	if proof.Epoch > nr.maxEpoch {
		nr.maxEpoch = proof.Epoch
	}

	return nil, false
}

// Prune removes records older than the given epoch.
func (nr *NullifierRegistry) Prune(minEpoch uint64) int {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	pruned := 0
	for epoch := range nr.records {
		if epoch < minEpoch {
			pruned += len(nr.records[epoch])
			delete(nr.records, epoch)
		}
	}
	return pruned
}
