// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package group implements MLS-inspired (RFC 9420) group messaging with
// tree-based key agreement, providing O(log n) group operations,
// forward secrecy, and post-compromise security.
package group

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// DefaultMaxGroupSize is the default maximum number of members in a group.
const DefaultMaxGroupSize = 256

// GroupSession manages an MLS group session.
type GroupSession struct {
	mu sync.Mutex

	GroupID     [32]byte
	Epoch      uint64
	Tree       *RatchetTree
	Members    []GroupMember
	SecretTree *SecretTree

	// Local member identity: the commits this session emits are signed with
	// signer and carry selfIndex as the sender.
	signer    ed25519.PrivateKey
	selfIndex uint32

	// Current epoch secrets
	epochSecret   [32]byte
	senderSecret  [32]byte
	encryptionKey [32]byte
}

// GroupMember represents a member of the group.
type GroupMember struct {
	Index      uint32
	PublicKey  [32]byte
	SigningKey [32]byte // Ed25519 public key commits from this member must verify against
	Credential []byte   // opaque identity credential
	Removed    bool
}

// CreateGroup creates a new group session with the creator as the first member.
func CreateGroup(groupID [32]byte, creatorKeyPair *KeyPair) (*GroupSession, error) {
	if creatorKeyPair == nil {
		return nil, errors.New("nil creator key pair")
	}

	tree := NewRatchetTree()
	memberIdx := tree.AddLeaf(LeafNode{
		PublicKey: creatorKeyPair.PublicKey,
		Private:   creatorKeyPair.PrivateKey,
	})

	gs := &GroupSession{
		GroupID: groupID,
		Epoch:   0,
		Tree:    tree,
		Members: []GroupMember{{
			Index:      memberIdx,
			PublicKey:  creatorKeyPair.PublicKey,
			SigningKey: creatorKeyPair.PublicKey,
		}},
		signer:    creatorKeyPair.Signer(),
		selfIndex: memberIdx,
	}

	// Derive initial epoch secrets
	if err := gs.deriveEpochSecrets(); err != nil {
		return nil, fmt.Errorf("derive epoch secrets: %w", err)
	}

	return gs, nil
}

// AddMember adds a new member to the group using their KeyPackage.
// Returns a Welcome message for the new member and a Commit for existing members.
func (gs *GroupSession) AddMember(pkg *KeyPackage) (*Welcome, *Commit, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if len(gs.Members) >= DefaultMaxGroupSize {
		return nil, nil, errors.New("group at maximum capacity")
	}
	// A forged or expired KeyPackage must not enter the tree.
	if err := ValidateKeyPackage(pkg); err != nil {
		return nil, nil, fmt.Errorf("invalid key package: %w", err)
	}

	// Add leaf to ratchet tree
	memberIdx := gs.Tree.AddLeaf(LeafNode{
		PublicKey: pkg.InitKey,
	})

	member := GroupMember{
		Index:      memberIdx,
		PublicKey:  pkg.InitKey,
		SigningKey: pkg.SigningKey,
		Credential: pkg.Credential,
	}
	gs.Members = append(gs.Members, member)

	// Advance epoch
	gs.Epoch++

	// Update path secrets
	gs.Tree.UpdatePath(gs.selfIndex)

	if err := gs.deriveEpochSecrets(); err != nil {
		return nil, nil, fmt.Errorf("derive epoch secrets: %w", err)
	}

	welcome := &Welcome{
		GroupID:     gs.GroupID,
		Epoch:      gs.Epoch,
		MemberIndex: memberIdx,
		EpochSecret: gs.epochSecret,
		TreeHash:    gs.Tree.RootHash(),
	}

	commit := &Commit{
		GroupID:          gs.GroupID,
		Epoch:            gs.Epoch,
		Sender:           gs.selfIndex,
		Type:             CommitAdd,
		MemberPublicKey:  pkg.InitKey,
		MemberSigningKey: pkg.SigningKey,
	}
	commit.Signature = ed25519.Sign(gs.signer, commitSigBytes(commit))

	return welcome, commit, nil
}

// RemoveMember removes a member from the group by index.
func (gs *GroupSession) RemoveMember(memberIndex uint32) (*Commit, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	found := false
	for i, m := range gs.Members {
		if m.Index == memberIndex && !m.Removed {
			gs.Members[i].Removed = true
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("member %d not found or already removed", memberIndex)
	}

	gs.Tree.BlankLeaf(memberIndex)
	gs.Epoch++
	gs.Tree.UpdatePath(gs.selfIndex)

	if err := gs.deriveEpochSecrets(); err != nil {
		return nil, fmt.Errorf("derive epoch secrets: %w", err)
	}

	commit := &Commit{
		GroupID:     gs.GroupID,
		Epoch:      gs.Epoch,
		Sender:     gs.selfIndex,
		Type:       CommitRemove,
		MemberIndex: memberIndex,
	}
	commit.Signature = ed25519.Sign(gs.signer, commitSigBytes(commit))
	return commit, nil
}

// Encrypt encrypts plaintext for the group.
func (gs *GroupSession) Encrypt(plaintext []byte) ([]byte, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	aead, err := chacha20poly1305.NewX(gs.encryptionKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Header: [groupID(32)][epoch(8)][nonce(24)]
	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], gs.Epoch)

	header := make([]byte, 0, 32+8+chacha20poly1305.NonceSizeX)
	header = append(header, gs.GroupID[:]...)
	header = append(header, epochBuf[:]...)
	header = append(header, nonce...)

	ciphertext := aead.Seal(nil, nonce, plaintext, header[:32+8]) // AAD = groupID + epoch
	result := make([]byte, 0, len(header)+len(ciphertext))
	result = append(result, header...)
	result = append(result, ciphertext...)

	return result, nil
}

// Decrypt decrypts a group message.
func (gs *GroupSession) Decrypt(data []byte) ([]byte, error) {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	headerSize := 32 + 8 + chacha20poly1305.NonceSizeX
	if len(data) < headerSize+chacha20poly1305.Overhead {
		return nil, errors.New("ciphertext too short")
	}

	// Parse header
	var groupID [32]byte
	copy(groupID[:], data[:32])
	if groupID != gs.GroupID {
		return nil, errors.New("group ID mismatch")
	}

	epoch := binary.BigEndian.Uint64(data[32:40])
	if epoch != gs.Epoch {
		return nil, fmt.Errorf("epoch mismatch: got %d, expected %d", epoch, gs.Epoch)
	}

	nonce := data[40:headerSize]
	ciphertext := data[headerSize:]

	aead, err := chacha20poly1305.NewX(gs.encryptionKey[:])
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, data[:40])
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

// ProcessCommit processes a Commit message from another member. The commit
// must carry a valid Ed25519 signature from a current, non-removed member —
// group membership changes are exactly the operations an outsider (or an
// evicted member) must not be able to forge.
func (gs *GroupSession) ProcessCommit(commit *Commit) error {
	gs.mu.Lock()
	defer gs.mu.Unlock()

	if commit.GroupID != gs.GroupID {
		return errors.New("group ID mismatch")
	}
	if commit.Epoch != gs.Epoch+1 {
		return fmt.Errorf("epoch mismatch: got %d, expected %d", commit.Epoch, gs.Epoch+1)
	}

	var sender *GroupMember
	for i := range gs.Members {
		if gs.Members[i].Index == commit.Sender {
			sender = &gs.Members[i]
			break
		}
	}
	if sender == nil {
		return fmt.Errorf("commit sender %d is not a group member", commit.Sender)
	}
	if sender.Removed {
		return fmt.Errorf("commit sender %d was removed from the group", commit.Sender)
	}
	if !ed25519.Verify(sender.SigningKey[:], commitSigBytes(commit), commit.Signature) {
		return errors.New("commit signature verification failed")
	}

	switch commit.Type {
	case CommitAdd:
		memberIdx := gs.Tree.AddLeaf(LeafNode{
			PublicKey: commit.MemberPublicKey,
		})
		gs.Members = append(gs.Members, GroupMember{
			Index:      memberIdx,
			PublicKey:  commit.MemberPublicKey,
			SigningKey: commit.MemberSigningKey,
		})
	case CommitRemove:
		for i, m := range gs.Members {
			if m.Index == commit.MemberIndex {
				gs.Members[i].Removed = true
				break
			}
		}
		gs.Tree.BlankLeaf(commit.MemberIndex)
	case CommitUpdate:
		gs.Tree.UpdateLeafKey(commit.Sender, commit.MemberPublicKey)
	default:
		return fmt.Errorf("unknown commit type %d", commit.Type)
	}

	gs.Epoch = commit.Epoch
	gs.Tree.UpdatePath(commit.Sender)

	return gs.deriveEpochSecrets()
}

// ActiveMembers returns the count of non-removed members.
func (gs *GroupSession) ActiveMembers() int {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	count := 0
	for _, m := range gs.Members {
		if !m.Removed {
			count++
		}
	}
	return count
}

// deriveEpochSecrets derives all epoch-specific secrets from the tree root.
func (gs *GroupSession) deriveEpochSecrets() error {
	rootHash := gs.Tree.RootHash()

	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], gs.Epoch)

	hkdfReader := hkdf.New(sha256.New, rootHash[:], gs.GroupID[:], epochBuf[:])

	if _, err := io.ReadFull(hkdfReader, gs.epochSecret[:]); err != nil {
		return fmt.Errorf("derive epoch secret: %w", err)
	}
	if _, err := io.ReadFull(hkdfReader, gs.senderSecret[:]); err != nil {
		return fmt.Errorf("derive sender secret: %w", err)
	}
	if _, err := io.ReadFull(hkdfReader, gs.encryptionKey[:]); err != nil {
		return fmt.Errorf("derive encryption key: %w", err)
	}

	return nil
}

// Welcome is sent to a new member joining a group.
type Welcome struct {
	GroupID     [32]byte
	Epoch      uint64
	MemberIndex uint32
	EpochSecret [32]byte
	TreeHash    [32]byte
}

// Commit represents a group state change, authenticated by the sender's
// Ed25519 signature over commitSigBytes.
type Commit struct {
	GroupID          [32]byte
	Epoch            uint64
	Sender           uint32
	Type             CommitType
	MemberIndex      uint32   // for Remove
	MemberPublicKey  [32]byte // for Add/Update
	MemberSigningKey [32]byte // for Add: the new member's Ed25519 key
	Signature        []byte   // sender's Ed25519 signature over commitSigBytes
}

// commitSigBytes is the canonical signing input: every field except the
// signature itself, fixed-width encoded.
func commitSigBytes(c *Commit) []byte {
	buf := make([]byte, 0, 32+8+4+1+4+32+32)
	buf = append(buf, c.GroupID[:]...)
	var u64 [8]byte
	binary.BigEndian.PutUint64(u64[:], c.Epoch)
	buf = append(buf, u64[:]...)
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], c.Sender)
	buf = append(buf, u32[:]...)
	buf = append(buf, byte(c.Type))
	binary.BigEndian.PutUint32(u32[:], c.MemberIndex)
	buf = append(buf, u32[:]...)
	buf = append(buf, c.MemberPublicKey[:]...)
	buf = append(buf, c.MemberSigningKey[:]...)
	return buf
}

// CommitType identifies the type of group change.
type CommitType uint8

const (
	CommitAdd    CommitType = 1
	CommitRemove CommitType = 2
	CommitUpdate CommitType = 3
)

// KeyPair is an Ed25519 key pair for group operations. PrivateKey is the
// 32-byte Ed25519 seed; PublicKey the corresponding verification key.
type KeyPair struct {
	PublicKey  [32]byte
	PrivateKey [32]byte
}

// Signer expands the seed into the Ed25519 signing key.
func (kp *KeyPair) Signer() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(kp.PrivateKey[:])
}

// GenerateKeyPair generates a random Ed25519 key pair for group membership.
func GenerateKeyPair() (*KeyPair, error) {
	var kp KeyPair
	if _, err := rand.Read(kp.PrivateKey[:]); err != nil {
		return nil, err
	}
	priv := ed25519.NewKeyFromSeed(kp.PrivateKey[:])
	copy(kp.PublicKey[:], priv.Public().(ed25519.PublicKey))
	return &kp, nil
}
