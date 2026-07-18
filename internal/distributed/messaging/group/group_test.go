// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package group

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func TestCreateGroup(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-id-001"))

	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if gs.Epoch != 0 {
		t.Errorf("Epoch = %d, want 0", gs.Epoch)
	}
	if len(gs.Members) != 1 {
		t.Errorf("Members = %d, want 1", len(gs.Members))
	}
	if gs.ActiveMembers() != 1 {
		t.Errorf("ActiveMembers = %d, want 1", gs.ActiveMembers())
	}
	if gs.GroupID != groupID {
		t.Error("GroupID mismatch")
	}
}

func TestAddMember(t *testing.T) {
	creatorKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-add"))

	gs, err := CreateGroup(groupID, creatorKP)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Create key package for new member
	var identityKey [32]byte
	copy(identityKey[:], []byte("member-identity-key"))
	pkg, _, err := CreateKeyPackage(identityKey, []byte("member-credential"))
	if err != nil {
		t.Fatalf("CreateKeyPackage: %v", err)
	}

	welcome, commit, err := gs.AddMember(pkg)
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	if gs.Epoch != 1 {
		t.Errorf("Epoch = %d, want 1", gs.Epoch)
	}
	if gs.ActiveMembers() != 2 {
		t.Errorf("ActiveMembers = %d, want 2", gs.ActiveMembers())
	}
	if welcome == nil {
		t.Error("expected non-nil Welcome")
	}
	if commit == nil {
		t.Error("expected non-nil Commit")
	}
	if welcome.Epoch != 1 {
		t.Errorf("Welcome.Epoch = %d, want 1", welcome.Epoch)
	}
	if commit.Type != CommitAdd {
		t.Errorf("Commit.Type = %d, want CommitAdd (%d)", commit.Type, CommitAdd)
	}
}

func TestRemoveMember(t *testing.T) {
	creatorKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-remove"))

	gs, err := CreateGroup(groupID, creatorKP)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Add 2 members
	var members []uint32
	for i := 0; i < 2; i++ {
		var ik [32]byte
		ik[0] = byte(i + 1)
		pkg, _, err := CreateKeyPackage(ik, []byte("cred"))
		if err != nil {
			t.Fatalf("CreateKeyPackage %d: %v", i, err)
		}
		welcome, _, err := gs.AddMember(pkg)
		if err != nil {
			t.Fatalf("AddMember %d: %v", i, err)
		}
		members = append(members, welcome.MemberIndex)
	}

	if gs.ActiveMembers() != 3 {
		t.Fatalf("ActiveMembers before remove = %d, want 3", gs.ActiveMembers())
	}

	// Remove the first added member
	commit, err := gs.RemoveMember(members[0])
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if commit.Type != CommitRemove {
		t.Errorf("Commit.Type = %d, want CommitRemove (%d)", commit.Type, CommitRemove)
	}
	if gs.ActiveMembers() != 2 {
		t.Errorf("ActiveMembers after remove = %d, want 2", gs.ActiveMembers())
	}
}

func TestRemoveNonexistent(t *testing.T) {
	creatorKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-noexist"))

	gs, err := CreateGroup(groupID, creatorKP)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	_, err = gs.RemoveMember(999)
	if err == nil {
		t.Error("expected error when removing non-existent member")
	}
}

func TestGroupEncryptDecrypt(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-encrypt"))

	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	plaintext := []byte("secret message for the group")

	ciphertext, err := gs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if len(ciphertext) == 0 {
		t.Fatal("expected non-empty ciphertext")
	}

	decrypted, err := gs.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("Decrypt = %q, want %q", decrypted, plaintext)
	}
}

func TestGroupEncryptDecryptAfterAddMember(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-group-enc-add"))

	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Add a member to advance epoch
	var ik [32]byte
	ik[0] = 0x42
	pkg, _, err := CreateKeyPackage(ik, []byte("new-member"))
	if err != nil {
		t.Fatalf("CreateKeyPackage: %v", err)
	}
	if _, _, err := gs.AddMember(pkg); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	plaintext := []byte("message after member added")

	ciphertext, err := gs.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt after add: %v", err)
	}

	decrypted, err := gs.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt after add: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("Decrypt = %q, want %q", decrypted, plaintext)
	}
}

func TestGroupDecryptWrongGroup(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var gid1 [32]byte
	copy(gid1[:], []byte("group-one"))
	var gid2 [32]byte
	copy(gid2[:], []byte("group-two"))

	gs1, err := CreateGroup(gid1, kp1)
	if err != nil {
		t.Fatalf("CreateGroup 1: %v", err)
	}
	gs2, err := CreateGroup(gid2, kp2)
	if err != nil {
		t.Fatalf("CreateGroup 2: %v", err)
	}

	ciphertext, err := gs1.Encrypt([]byte("only for group 1"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = gs2.Decrypt(ciphertext)
	if err == nil {
		t.Error("expected error when decrypting with wrong group")
	}
}

func TestRatchetTree(t *testing.T) {
	tree := NewRatchetTree()

	leaf1 := LeafNode{PublicKey: [32]byte{1}}
	tree.AddLeaf(leaf1)
	root1 := tree.RootHash()
	if root1 == ([32]byte{}) {
		t.Error("expected non-zero root hash with one leaf")
	}

	leaf2 := LeafNode{PublicKey: [32]byte{2}}
	tree.AddLeaf(leaf2)
	root2 := tree.RootHash()
	if root2 == root1 {
		t.Error("root hash should change after adding a leaf")
	}

	// Blank a leaf
	tree.BlankLeaf(1)
	root3 := tree.RootHash()
	if root3 == root2 {
		t.Error("root hash should change after blanking a leaf")
	}
}

func TestRatchetTreeRootHashDeterministic(t *testing.T) {
	buildTree := func() [32]byte {
		tree := NewRatchetTree()
		tree.AddLeaf(LeafNode{PublicKey: [32]byte{0xAA}})
		tree.AddLeaf(LeafNode{PublicKey: [32]byte{0xBB}})
		tree.AddLeaf(LeafNode{PublicKey: [32]byte{0xCC}})
		return tree.RootHash()
	}

	root1 := buildTree()
	root2 := buildTree()
	if root1 != root2 {
		t.Error("same leaves should produce same root hash")
	}
}

func TestKeyPackageCreateValidate(t *testing.T) {
	var identityKey [32]byte
	copy(identityKey[:], []byte("identity-key-for-test"))

	pkg, kp, err := CreateKeyPackage(identityKey, []byte("test-credential"))
	if err != nil {
		t.Fatalf("CreateKeyPackage: %v", err)
	}
	if kp == nil {
		t.Fatal("expected non-nil key pair")
	}
	if pkg.Version != 1 {
		t.Errorf("Version = %d, want 1", pkg.Version)
	}
	if pkg.CipherSuite != CipherSuiteID {
		t.Errorf("CipherSuite = %d, want %d", pkg.CipherSuite, CipherSuiteID)
	}

	if err := ValidateKeyPackage(pkg); err != nil {
		t.Fatalf("ValidateKeyPackage: %v", err)
	}
}

func TestKeyPackageExpired(t *testing.T) {
	var identityKey [32]byte
	copy(identityKey[:], []byte("expired-key"))

	pkg, _, err := CreateKeyPackage(identityKey, []byte("cred"))
	if err != nil {
		t.Fatalf("CreateKeyPackage: %v", err)
	}

	// Set expiry to the past
	pkg.ExpiresAt = time.Now().Add(-1 * time.Hour).Unix()

	err = ValidateKeyPackage(pkg)
	if err == nil {
		t.Error("expected error for expired key package")
	}
}

func TestSecretTree(t *testing.T) {
	var epochSecret [32]byte
	copy(epochSecret[:], []byte("epoch-secret-for-testing"))

	size := 4
	st := NewSecretTree(epochSecret, size)

	// Each member should get a different secret
	secrets := make(map[[32]byte]uint32)
	for i := uint32(0); i < uint32(size); i++ {
		s, ok := st.GetSecret(i)
		if !ok {
			t.Fatalf("GetSecret(%d) returned false", i)
		}
		if prev, dup := secrets[s]; dup {
			t.Errorf("member %d has same secret as member %d", i, prev)
		}
		secrets[s] = i
	}

	// Non-existent member should return false
	_, ok := st.GetSecret(uint32(size + 10))
	if ok {
		t.Error("expected false for non-existent member")
	}
}

func TestProcessCommitAdd(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-process-commit"))

	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if gs.Epoch != 0 {
		t.Fatalf("initial Epoch = %d, want 0", gs.Epoch)
	}

	// A commit must carry the sender's signature to be processed.
	newMemberKey := [32]byte{0xDE, 0xAD}
	commit := &Commit{
		GroupID:         groupID,
		Epoch:           1, // must be current+1
		Sender:          0,
		Type:            CommitAdd,
		MemberPublicKey: newMemberKey,
	}
	commit.Signature = ed25519.Sign(kp.Signer(), commitSigBytes(commit))

	if err := gs.ProcessCommit(commit); err != nil {
		t.Fatalf("ProcessCommit: %v", err)
	}

	if gs.Epoch != 1 {
		t.Errorf("Epoch after commit = %d, want 1", gs.Epoch)
	}
	if gs.ActiveMembers() != 2 {
		t.Errorf("ActiveMembers after commit = %d, want 2", gs.ActiveMembers())
	}
}

func TestProcessCommitRejectsForgery(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	var groupID [32]byte
	copy(groupID[:], []byte("test-commit-forgery"))
	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	mk := func() *Commit {
		return &Commit{
			GroupID:         groupID,
			Epoch:           1,
			Sender:          0,
			Type:            CommitRemove,
			MemberIndex:     0,
			MemberPublicKey: [32]byte{0x01},
		}
	}

	// Unsigned commit: rejected.
	if err := gs.ProcessCommit(mk()); err == nil {
		t.Fatal("unsigned commit must be rejected")
	}

	// Signed by a key that is NOT the sender's: rejected.
	outsider, _ := GenerateKeyPair()
	c := mk()
	c.Signature = ed25519.Sign(outsider.Signer(), commitSigBytes(c))
	if err := gs.ProcessCommit(c); err == nil {
		t.Fatal("outsider-signed commit must be rejected")
	}

	// Valid signature but tampered content: rejected.
	c = mk()
	c.Signature = ed25519.Sign(kp.Signer(), commitSigBytes(c))
	c.MemberIndex = 42 // redirect the removal after signing
	if err := gs.ProcessCommit(c); err == nil {
		t.Fatal("tampered commit must be rejected")
	}

	// Sender index that is not a member: rejected.
	c = mk()
	c.Sender = 7
	c.Signature = ed25519.Sign(kp.Signer(), commitSigBytes(c))
	if err := gs.ProcessCommit(c); err == nil {
		t.Fatal("unknown-sender commit must be rejected")
	}

	// A removed member must not be able to commit: build a two-member group,
	// remove the second member, then replay a commit it signed.
	memberKP, _ := GenerateKeyPair()
	pkg, _, err := CreateKeyPackage(memberKP.PrivateKey, []byte("member-2"))
	if err != nil {
		t.Fatalf("CreateKeyPackage: %v", err)
	}
	if _, _, err := gs.AddMember(pkg); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	removed := gs.Members[1]
	if _, err := gs.RemoveMember(removed.Index); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	evicted := &Commit{
		GroupID: groupID,
		Epoch:   gs.Epoch + 1,
		Sender:  removed.Index,
		Type:    CommitRemove,
		// tries to remove the creator
		MemberIndex: 0,
	}
	evicted.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(memberKP.PrivateKey[:]), commitSigBytes(evicted))
	if err := gs.ProcessCommit(evicted); err == nil {
		t.Fatal("commit from an evicted member must be rejected")
	}
}

func TestActiveMembers(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	var groupID [32]byte
	copy(groupID[:], []byte("test-active-members"))

	gs, err := CreateGroup(groupID, kp)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Add 3 members
	var memberIndices []uint32
	for i := 0; i < 3; i++ {
		var ik [32]byte
		ik[0] = byte(i + 10)
		pkg, _, err := CreateKeyPackage(ik, []byte("cred"))
		if err != nil {
			t.Fatalf("CreateKeyPackage %d: %v", i, err)
		}
		welcome, _, err := gs.AddMember(pkg)
		if err != nil {
			t.Fatalf("AddMember %d: %v", i, err)
		}
		memberIndices = append(memberIndices, welcome.MemberIndex)
	}

	// Should have creator + 3 = 4 active
	if gs.ActiveMembers() != 4 {
		t.Errorf("ActiveMembers = %d, want 4", gs.ActiveMembers())
	}

	// Remove 1
	if _, err := gs.RemoveMember(memberIndices[0]); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	// Should have 3 active
	if gs.ActiveMembers() != 3 {
		t.Errorf("ActiveMembers after remove = %d, want 3", gs.ActiveMembers())
	}
}
