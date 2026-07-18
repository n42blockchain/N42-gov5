// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package rln implements Rate-Limiting Nullifier (RLN) for spam prevention.
// Based on Waku RLN v2: Shamir secret sharing + Poseidon hashing + Merkle membership proofs.
// Members register identity commitments; each epoch allows a limited number of messages.
// Exceeding the limit exposes the identity secret via Shamir reconstruction.
//
// NOT PRODUCTION-WIRED. RLN's guarantees require a ZK circuit proving, without
// revealing the secret, that the nullifier and Shamir share were correctly
// derived from a secret whose commitment is in the tree. This package has no
// such circuit, and no non-test code calls the verifier — the messaging relay
// does not invoke RLN validation. Without ZK there is no secure nullifier
// choice: a secret-derived nullifier is unverifiable (a spammer mints a fresh
// one per message and bypasses the limit), while the commitment-derived
// nullifier used here is verifiable but forgeable (any third party can craft a
// passing proof with an arbitrary in-range ShareY, pre-register a victim's
// nullifier, and censor them). Do not wire RLN into any production path until a
// vetted BN254 ZK circuit lands.
package rln

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sync"
)

// Field prime for Poseidon (BN254 scalar field).
var fieldPrime, _ = new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)

// DefaultMerkleDepth is the default depth of the membership Merkle tree.
const DefaultMerkleDepth = 20

// Identity represents an RLN member's secret identity.
type Identity struct {
	Secret     [32]byte // identity secret, canonical BN254 scalar (big-endian)
	Commitment [32]byte // identity commitment = PoseidonHash(secret)
}

// GenerateIdentity creates a new random RLN identity. The secret is a
// uniformly random canonical field element so Shamir recovery returns the
// exact secret bytes.
func GenerateIdentity() (*Identity, error) {
	secret, err := rand.Int(rand.Reader, fieldPrime)
	if err != nil {
		return nil, fmt.Errorf("generate identity secret: %w", err)
	}
	id := &Identity{}
	secret.FillBytes(id.Secret[:])
	id.Commitment = PoseidonHash(id.Secret[:])
	return id, nil
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
	return mt.generateMerkleProofLocked(index)
}

// ProofAndRoot returns a member's Merkle proof together with the root it was
// generated against, captured under a single read lock. Callers that need a
// proof consistent with a specific root must use this rather than calling
// GenerateMerkleProof and Root() separately: a concurrent Register between the
// two changes shared-ancestor siblings, yielding a (new root, old siblings)
// pair that fails verification.
func (mt *MembershipTree) ProofAndRoot(index uint32) (*MerkleProof, [32]byte, error) {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	proof, err := mt.generateMerkleProofLocked(index)
	if err != nil {
		return nil, [32]byte{}, err
	}
	return proof, mt.root, nil
}

// generateMerkleProofLocked builds the proof; the caller must hold mt.mu.
func (mt *MembershipTree) generateMerkleProofLocked(index uint32) (*MerkleProof, error) {
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
	// Capture the proof and the root it was built against atomically; see
	// ProofAndRoot for why the separate GenerateMerkleProof + Root() calls
	// raced under concurrent Register.
	merkleProof, root, err := tree.ProofAndRoot(memberIndex)
	if err != nil {
		return nil, fmt.Errorf("generate merkle proof: %w", err)
	}

	// Compute nullifier = PoseidonHash(commitment, epoch). The RLN spec
	// derives the nullifier from the secret, but without a ZK circuit a
	// secret-derived nullifier is unverifiable, letting a spammer mint a
	// fresh nullifier per message and bypass the rate limit entirely.
	// The commitment is already public (the proof carries the member
	// index), so deriving from it costs no anonymity and lets verifiers
	// recompute the expected nullifier. NOTE: this trade is not a net win
	// without ZK — a verifiable commitment-derived nullifier is also
	// forgeable by any third party (arbitrary in-range ShareY passes the
	// non-ZK verifier), so anyone can pre-register a victim's nullifier and
	// censor their per-epoch messages. RLN is therefore NOT wired into any
	// production path (see the package doc) and must not be until a real ZK
	// circuit makes both the nullifier and the share verifiable.
	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], epoch)
	nullifier := PoseidonHash(identity.Commitment[:], epochBuf[:])

	// Compute Shamir share: y = secret + slope * x (mod p) where
	// slope = PoseidonHash(secret, epoch) and x = PoseidonHash(epoch, messageHash).
	// The slope must depend only on identity+epoch (RLN A(x) = a0 + a1*x):
	// two shares from the same epoch then lie on one line and reveal the
	// secret. Using the message hash as the slope put each share on a
	// different line and recovery returned garbage.
	slope := PoseidonHash(identity.Secret[:], epochBuf[:])
	shareX := PoseidonHash(epochBuf[:], messageHash[:])
	shareY := shamirEvaluate(identity.Secret, slope, shareX)

	return &RLNProof{
		MerkleProof: merkleProof,
		Epoch:       epoch,
		Nullifier:   nullifier,
		ShareX:      shareX,
		ShareY:      shareY,
		MerkleRoot:  root,
	}, nil
}

// shamirEvaluate computes y = secret + slope * x (Shamir line evaluation).
func shamirEvaluate(secret [32]byte, slope [32]byte, x [32]byte) [32]byte {
	s := new(big.Int).SetBytes(secret[:])
	h := new(big.Int).SetBytes(slope[:])
	xInt := new(big.Int).SetBytes(x[:])

	// y = s + slope * x mod p
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
