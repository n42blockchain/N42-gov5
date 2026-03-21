// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package rln implements Rate-Limiting Nullifier (RLN) for spam prevention.
// Based on Waku RLN v2: Shamir secret sharing + Poseidon hashing + Merkle membership proofs.
// Members register identity commitments; each epoch allows a limited number of messages.
// Exceeding the limit exposes the identity secret via Shamir reconstruction.
package rln

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"golang.org/x/crypto/sha3"
)

// Field prime for Poseidon (BN254 scalar field).
var fieldPrime, _ = new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)

// DefaultMerkleDepth is the default depth of the membership Merkle tree.
const DefaultMerkleDepth = 20

// Identity represents an RLN member's secret identity.
type Identity struct {
	Secret     [32]byte // identity secret (random)
	Commitment [32]byte // identity commitment = PoseidonHash(secret)
}

// GenerateIdentity creates a new random RLN identity.
func GenerateIdentity() (*Identity, error) {
	id := &Identity{}
	if _, err := rand.Read(id.Secret[:]); err != nil {
		return nil, fmt.Errorf("generate identity secret: %w", err)
	}
	id.Commitment = PoseidonHash(id.Secret[:])
	return id, nil
}

// PoseidonHash is a simplified Poseidon-like hash using Keccak256 with domain separation.
// In production, this would use a proper Poseidon implementation over BN254.
// This provides the same interface and collision resistance properties.
func PoseidonHash(data ...[]byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("n42-rln-poseidon-v1"))
	for _, d := range data {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(d)))
		h.Write(lenBuf[:])
		h.Write(d)
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// MembershipTree is a Merkle tree of identity commitments.
type MembershipTree struct {
	mu          sync.RWMutex
	depth       int
	leaves      [][32]byte
	nodes       map[uint64][32]byte // level<<32 | index -> hash
	root        [32]byte
	size        int
	emptyHashes [][32]byte // precomputed empty hashes per level
}

// NewMembershipTree creates a new Merkle membership tree.
func NewMembershipTree(depth int) *MembershipTree {
	if depth <= 0 {
		depth = DefaultMerkleDepth
	}
	mt := &MembershipTree{
		depth:  depth,
		leaves: make([][32]byte, 0),
		nodes:  make(map[uint64][32]byte),
	}
	// Precompute empty hashes for each level
	mt.emptyHashes = make([][32]byte, depth+1)
	// mt.emptyHashes[0] is the zero value [32]byte{}
	for i := 1; i <= depth; i++ {
		child := mt.emptyHashes[i-1]
		mt.emptyHashes[i] = PoseidonHash(child[:], child[:])
	}
	mt.root = mt.emptyHashes[depth]
	return mt
}

// Register adds an identity commitment to the tree and returns the membership index.
func (mt *MembershipTree) Register(commitment [32]byte) (uint32, error) {
	mt.mu.Lock()
	defer mt.mu.Unlock()

	maxLeaves := 1 << mt.depth
	if mt.size >= maxLeaves {
		return 0, errors.New("membership tree full")
	}

	idx := uint32(mt.size)
	mt.leaves = append(mt.leaves, commitment)
	mt.size++

	// Update path from leaf to root
	mt.updatePath(int(idx))

	return idx, nil
}

// Root returns the current Merkle root.
func (mt *MembershipTree) Root() [32]byte {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.root
}

// Size returns the number of registered members.
func (mt *MembershipTree) Size() int {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.size
}

// GenerateMerkleProof generates a Merkle proof for the member at the given index.
func (mt *MembershipTree) GenerateMerkleProof(index uint32) (*MerkleProof, error) {
	mt.mu.RLock()
	defer mt.mu.RUnlock()

	if int(index) >= mt.size {
		return nil, fmt.Errorf("index %d out of range (size=%d)", index, mt.size)
	}

	proof := &MerkleProof{
		Index:    index,
		Siblings: make([][32]byte, mt.depth),
	}

	idx := int(index)
	for level := 0; level < mt.depth; level++ {
		siblingIdx := idx ^ 1
		sibling := mt.getNode(level, siblingIdx)
		proof.Siblings[level] = sibling
		idx >>= 1
	}

	return proof, nil
}

// MerkleProof contains a Merkle inclusion proof.
type MerkleProof struct {
	Index    uint32
	Siblings [][32]byte
}

// Verify verifies the Merkle proof against the given root and leaf.
func (p *MerkleProof) Verify(root [32]byte, leaf [32]byte) bool {
	current := leaf
	idx := int(p.Index)
	for _, sibling := range p.Siblings {
		if idx&1 == 0 {
			current = PoseidonHash(current[:], sibling[:])
		} else {
			current = PoseidonHash(sibling[:], current[:])
		}
		idx >>= 1
	}
	return current == root
}

// RLNProof is a proof that a message was sent by a registered member within rate limits.
type RLNProof struct {
	MerkleProof *MerkleProof
	Epoch       uint64   // time epoch
	Nullifier   [32]byte // unique per identity+epoch pair
	ShareX      [32]byte // Shamir share x-coordinate
	ShareY      [32]byte // Shamir share y-coordinate
	MerkleRoot  [32]byte // root at time of proof generation
}

// GenerateProof generates an RLN proof for sending a message.
func GenerateProof(identity *Identity, memberIndex uint32, tree *MembershipTree, epoch uint64, messageHash [32]byte) (*RLNProof, error) {
	merkleProof, err := tree.GenerateMerkleProof(memberIndex)
	if err != nil {
		return nil, fmt.Errorf("generate merkle proof: %w", err)
	}

	// Compute nullifier = PoseidonHash(secret, epoch)
	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], epoch)
	nullifier := PoseidonHash(identity.Secret[:], epochBuf[:])

	// Compute Shamir share: y = secret + messageHash * x (mod p)
	// where x = PoseidonHash(epoch, messageHash)
	shareX := PoseidonHash(epochBuf[:], messageHash[:])
	shareY := shamirEvaluate(identity.Secret, messageHash, shareX)

	return &RLNProof{
		MerkleProof: merkleProof,
		Epoch:       epoch,
		Nullifier:   nullifier,
		ShareX:      shareX,
		ShareY:      shareY,
		MerkleRoot:  tree.Root(),
	}, nil
}

// shamirEvaluate computes y = secret + hash * x (simplified Shamir evaluation).
func shamirEvaluate(secret [32]byte, hash [32]byte, x [32]byte) [32]byte {
	s := new(big.Int).SetBytes(secret[:])
	h := new(big.Int).SetBytes(hash[:])
	xInt := new(big.Int).SetBytes(x[:])

	// y = s + h * x mod p
	y := new(big.Int).Mul(h, xInt)
	y.Add(y, s)
	y.Mod(y, fieldPrime)

	var result [32]byte
	yBytes := y.Bytes()
	copy(result[32-len(yBytes):], yBytes)
	return result
}

func (mt *MembershipTree) getNode(level, index int) [32]byte {
	key := uint64(level)<<32 | uint64(index)
	if h, ok := mt.nodes[key]; ok {
		return h
	}
	if level == 0 {
		if index < len(mt.leaves) {
			return mt.leaves[index]
		}
		return mt.emptyHash(0)
	}
	return mt.emptyHash(level)
}

func (mt *MembershipTree) setNode(level, index int, hash [32]byte) {
	key := uint64(level)<<32 | uint64(index)
	mt.nodes[key] = hash
}

func (mt *MembershipTree) updatePath(leafIndex int) {
	mt.setNode(0, leafIndex, mt.leaves[leafIndex])

	idx := leafIndex
	for level := 0; level < mt.depth; level++ {
		left := mt.getNode(level, idx&^1)
		right := mt.getNode(level, idx|1)
		parent := PoseidonHash(left[:], right[:])
		idx >>= 1
		mt.setNode(level+1, idx, parent)
	}
	mt.root = mt.getNode(mt.depth, 0)
}

func (mt *MembershipTree) emptyHash(level int) [32]byte {
	return mt.emptyHashes[level]
}
