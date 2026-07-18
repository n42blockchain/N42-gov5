// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// RLN proof Verifier. VerifyProof checks the MembershipTree root,
// confirms the Merkle path for the prover's commitment index, recomputes
// the nullifier and the Shamir share x-coordinate, and range-checks the
// share y-coordinate, so two shares in the same epoch let a higher-level
// component recover the identity secret and slash. Without a ZK circuit
// the share y-coordinate itself is unverifiable (only the secret holder
// can evaluate it), so recovered secrets must be checked against the
// member's commitment before slashing — see GossipSubValidator.

package rln

import (
	"encoding/binary"
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

// VerifyProof validates an RLN proof against the message it claims to
// authorize. Beyond the Merkle membership check it recomputes every
// publicly derivable component: the nullifier must equal
// PoseidonHash(commitment, epoch) — otherwise a spammer mints a fresh
// nullifier per message and the rate limit never trips — and ShareX must
// equal PoseidonHash(epoch, messageHash), binding the share to this
// message. ShareY must be a canonical field element so Shamir recovery
// operates on well-formed points.
func (v *Verifier) VerifyProof(proof *RLNProof, messageHash [32]byte) error {
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
	leaf, err := v.LeafAt(idx)
	if err != nil {
		return err
	}

	if !proof.MerkleProof.Verify(proof.MerkleRoot, leaf) {
		return fmt.Errorf("invalid merkle proof")
	}

	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], proof.Epoch)

	if expected := PoseidonHash(leaf[:], epochBuf[:]); proof.Nullifier != expected {
		return fmt.Errorf("nullifier mismatch")
	}
	if expected := PoseidonHash(epochBuf[:], messageHash[:]); proof.ShareX != expected {
		return fmt.Errorf("share x mismatch")
	}
	if new(big.Int).SetBytes(proof.ShareY[:]).Cmp(fieldPrime) >= 0 {
		return fmt.Errorf("share y out of field range")
	}

	return nil
}

// LeafAt returns the commitment stored at the given member index.
func (v *Verifier) LeafAt(index uint32) ([32]byte, error) {
	v.tree.mu.RLock()
	defer v.tree.mu.RUnlock()
	if int(index) >= len(v.tree.leaves) {
		return [32]byte{}, fmt.Errorf("member index out of range")
	}
	return v.tree.leaves[index], nil
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
	// on the line y = secret + slope * x (the slope is identity+epoch
	// bound, so both shares from one epoch lie on the same line)
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
