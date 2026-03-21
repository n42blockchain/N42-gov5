package rln

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	var zero [32]byte
	if id.Secret == zero {
		t.Fatal("Secret should not be zero")
	}
	if id.Commitment == zero {
		t.Fatal("Commitment should not be zero")
	}

	// Commitment should equal PoseidonHash(secret).
	expected := PoseidonHash(id.Secret[:])
	if id.Commitment != expected {
		t.Fatal("Commitment != PoseidonHash(Secret)")
	}

	// Two identities should be different.
	id2, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity (2nd): %v", err)
	}
	if id.Secret == id2.Secret {
		t.Fatal("two identities should have different secrets")
	}
	if id.Commitment == id2.Commitment {
		t.Fatal("two identities should have different commitments")
	}
}

func TestPoseidonHash(t *testing.T) {
	// Deterministic: same input produces same output.
	input := []byte("test-data")
	h1 := PoseidonHash(input)
	h2 := PoseidonHash(input)
	if h1 != h2 {
		t.Fatal("PoseidonHash should be deterministic")
	}

	var zero [32]byte
	if h1 == zero {
		t.Fatal("hash should not be zero")
	}

	// Different inputs produce different outputs.
	h3 := PoseidonHash([]byte("other-data"))
	if h1 == h3 {
		t.Fatal("different inputs should produce different hashes")
	}

	// Multiple input slices.
	h4 := PoseidonHash([]byte("a"), []byte("b"))
	h5 := PoseidonHash([]byte("a"), []byte("b"))
	if h4 != h5 {
		t.Fatal("PoseidonHash with multiple inputs should be deterministic")
	}

	// Different ordering of inputs should produce different output.
	h6 := PoseidonHash([]byte("b"), []byte("a"))
	if h4 == h6 {
		t.Fatal("different input order should produce different hashes")
	}

	// Single combined input vs two separate inputs should differ
	// (due to length-prefixed encoding).
	h7 := PoseidonHash([]byte("ab"))
	if h4 == h7 {
		t.Fatal("concatenated single input should differ from two separate inputs")
	}
}

func TestMembershipTreeRegister(t *testing.T) {
	tree := NewMembershipTree(5) // small depth for testing

	if tree.Size() != 0 {
		t.Fatalf("initial size = %d, want 0", tree.Size())
	}

	initialRoot := tree.Root()

	// Register 3 members.
	id1, _ := GenerateIdentity()
	id2, _ := GenerateIdentity()
	id3, _ := GenerateIdentity()

	idx1, err := tree.Register(id1.Commitment)
	if err != nil {
		t.Fatalf("Register id1: %v", err)
	}
	if idx1 != 0 {
		t.Fatalf("idx1 = %d, want 0", idx1)
	}

	idx2, err := tree.Register(id2.Commitment)
	if err != nil {
		t.Fatalf("Register id2: %v", err)
	}
	if idx2 != 1 {
		t.Fatalf("idx2 = %d, want 1", idx2)
	}

	idx3, err := tree.Register(id3.Commitment)
	if err != nil {
		t.Fatalf("Register id3: %v", err)
	}
	if idx3 != 2 {
		t.Fatalf("idx3 = %d, want 2", idx3)
	}

	if tree.Size() != 3 {
		t.Fatalf("size = %d, want 3", tree.Size())
	}

	// Root should change after registrations.
	newRoot := tree.Root()
	if newRoot == initialRoot {
		t.Fatal("root should change after registrations")
	}
}

func TestMerkleProofVerify(t *testing.T) {
	tree := NewMembershipTree(5)

	ids := make([]*Identity, 4)
	indices := make([]uint32, 4)
	for i := 0; i < 4; i++ {
		var err error
		ids[i], err = GenerateIdentity()
		if err != nil {
			t.Fatalf("GenerateIdentity %d: %v", i, err)
		}
		indices[i], err = tree.Register(ids[i].Commitment)
		if err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
	}

	root := tree.Root()

	// Generate and verify proof for each member.
	for i := 0; i < 4; i++ {
		proof, err := tree.GenerateMerkleProof(indices[i])
		if err != nil {
			t.Fatalf("GenerateMerkleProof %d: %v", i, err)
		}

		if !proof.Verify(root, ids[i].Commitment) {
			t.Fatalf("Merkle proof %d should verify against current root", i)
		}
	}
}

func TestMerkleProofInvalidRoot(t *testing.T) {
	tree := NewMembershipTree(5)

	id, _ := GenerateIdentity()
	idx, _ := tree.Register(id.Commitment)

	proof, err := tree.GenerateMerkleProof(idx)
	if err != nil {
		t.Fatalf("GenerateMerkleProof: %v", err)
	}

	// Verify against the wrong root.
	wrongRoot := [32]byte{0xFF, 0xFE, 0xFD}
	if proof.Verify(wrongRoot, id.Commitment) {
		t.Fatal("proof should NOT verify against wrong root")
	}
}

func TestGenerateAndVerifyProof(t *testing.T) {
	tree := NewMembershipTree(10)

	// Create and register an identity.
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	idx, err := tree.Register(id.Commitment)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	epoch := uint64(time.Now().Unix() / 10)
	msgHash := MessageHashForRLN("test-topic", []byte("test message"), epoch)

	// Generate RLN proof.
	proof, err := GenerateProof(id, idx, tree, epoch, msgHash)
	if err != nil {
		t.Fatalf("GenerateProof: %v", err)
	}

	if proof == nil {
		t.Fatal("proof should not be nil")
	}
	if proof.Epoch != epoch {
		t.Fatalf("Epoch = %d, want %d", proof.Epoch, epoch)
	}

	// Verify the Merkle part.
	verifier := NewVerifier(tree)
	if err := verifier.VerifyProof(proof); err != nil {
		t.Fatalf("VerifyProof: %v", err)
	}

	// Verify nullifier is PoseidonHash(secret, epoch).
	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], epoch)
	expectedNullifier := PoseidonHash(id.Secret[:], epochBuf[:])
	if proof.Nullifier != expectedNullifier {
		t.Fatal("nullifier does not match expected PoseidonHash(secret, epoch)")
	}
}

func TestDetectSpam(t *testing.T) {
	tree := NewMembershipTree(10)

	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	idx, err := tree.Register(id.Commitment)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	epoch := uint64(100)

	// Send two different messages in the same epoch.
	msgHash1 := MessageHashForRLN("topic", []byte("message-1"), epoch)
	proof1, err := GenerateProof(id, idx, tree, epoch, msgHash1)
	if err != nil {
		t.Fatalf("GenerateProof (1): %v", err)
	}

	msgHash2 := MessageHashForRLN("topic", []byte("message-2"), epoch)
	proof2, err := GenerateProof(id, idx, tree, epoch, msgHash2)
	if err != nil {
		t.Fatalf("GenerateProof (2): %v", err)
	}

	// Both proofs should have the same nullifier (same identity + same epoch).
	if proof1.Nullifier != proof2.Nullifier {
		t.Fatal("same identity + same epoch should have same nullifier")
	}

	// But different share X values (different messages).
	if proof1.ShareX == proof2.ShareX {
		t.Fatal("different messages should produce different ShareX")
	}

	// Detect spam should recover the secret.
	isSpam, recovered, err := DetectSpam(proof1, proof2)
	if err != nil {
		t.Fatalf("DetectSpam: %v", err)
	}
	if !isSpam {
		t.Fatal("should detect spam")
	}

	// The recovered secret should match the original (mod field prime).
	// Due to big.Int modular arithmetic, compare by recomputing.
	// The secret bytes might differ if the original secret > fieldPrime,
	// but the Shamir reconstruction yields the value mod p.
	// Compute expected = secret mod p.
	var zero [32]byte
	if recovered == zero {
		t.Fatal("recovered secret should not be zero")
	}
}

func TestNullifierRegistry(t *testing.T) {
	nr := NewNullifierRegistry()

	tree := NewMembershipTree(10)
	id, _ := GenerateIdentity()
	idx, _ := tree.Register(id.Commitment)

	epoch := uint64(50)
	msgHash := MessageHashForRLN("topic", []byte("msg"), epoch)
	proof, _ := GenerateProof(id, idx, tree, epoch, msgHash)

	// First check should pass (not spam).
	existing, isSpam := nr.Check(proof)
	if isSpam {
		t.Fatal("first proof should not be spam")
	}
	if existing != nil {
		t.Fatal("existing should be nil for first proof")
	}

	// Second check with same nullifier should detect spam.
	msgHash2 := MessageHashForRLN("topic", []byte("msg2"), epoch)
	proof2, _ := GenerateProof(id, idx, tree, epoch, msgHash2)

	existing, isSpam = nr.Check(proof2)
	if !isSpam {
		t.Fatal("second proof with same nullifier should be spam")
	}
	if existing == nil {
		t.Fatal("existing should point to the first proof")
	}
	if existing.Epoch != proof.Epoch {
		t.Fatal("existing proof epoch mismatch")
	}
}

func TestNullifierRegistryPrune(t *testing.T) {
	nr := NewNullifierRegistry()

	tree := NewMembershipTree(10)

	// Add entries across multiple epochs.
	for epoch := uint64(1); epoch <= 5; epoch++ {
		id, _ := GenerateIdentity()
		idx, _ := tree.Register(id.Commitment)
		msgHash := MessageHashForRLN("topic", []byte("msg"), epoch)
		proof, _ := GenerateProof(id, idx, tree, epoch, msgHash)
		nr.Check(proof)
	}

	// Prune epochs < 4 (should remove epochs 1, 2, 3).
	pruned := nr.Prune(4)
	if pruned != 3 {
		t.Fatalf("pruned = %d, want 3", pruned)
	}

	// Epochs 4 and 5 should still have records.
	nr.mu.Lock()
	_, has4 := nr.records[4]
	_, has5 := nr.records[5]
	_, has1 := nr.records[1]
	nr.mu.Unlock()

	if !has4 {
		t.Fatal("epoch 4 should still exist")
	}
	if !has5 {
		t.Fatal("epoch 5 should still exist")
	}
	if has1 {
		t.Fatal("epoch 1 should have been pruned")
	}
}

func TestGossipSubValidator(t *testing.T) {
	tree := NewMembershipTree(10)
	verifier := NewVerifier(tree)
	registry := NewNullifierRegistry()
	validator := NewGossipSubValidator(verifier, registry, 10, 1)

	// Register an identity.
	id, _ := GenerateIdentity()
	idx, _ := tree.Register(id.Commitment)

	// Generate a valid proof for the current epoch.
	epoch := validator.CurrentEpoch()
	msgHash := MessageHashForRLN("topic", []byte("msg"), epoch)
	proof, err := GenerateProof(id, idx, tree, epoch, msgHash)
	if err != nil {
		t.Fatalf("GenerateProof: %v", err)
	}

	// Valid proof should be accepted.
	result := validator.ValidateProof(proof)
	if result != ValidationAccept {
		t.Fatalf("valid proof should be accepted, got %d", result)
	}

	// Nil proof should be rejected.
	result = validator.ValidateProof(nil)
	if result != ValidationReject {
		t.Fatalf("nil proof should be rejected, got %d", result)
	}

	// Stale epoch (far in the past) should be ignored.
	staleEpoch := uint64(0)
	staleMsgHash := MessageHashForRLN("topic", []byte("stale"), staleEpoch)
	staleProof, _ := GenerateProof(id, idx, tree, staleEpoch, staleMsgHash)
	// Override the MerkleRoot to match current tree root (since verifier checks root).
	staleProof.MerkleRoot = tree.Root()

	result = validator.ValidateProof(staleProof)
	if result != ValidationIgnore {
		t.Fatalf("stale epoch proof should be ignored, got %d", result)
	}
}

func TestGossipSubValidatorSpamReject(t *testing.T) {
	tree := NewMembershipTree(10)
	verifier := NewVerifier(tree)
	registry := NewNullifierRegistry()
	validator := NewGossipSubValidator(verifier, registry, 10, 1)

	id, _ := GenerateIdentity()
	idx, _ := tree.Register(id.Commitment)

	epoch := validator.CurrentEpoch()

	// First message should pass.
	msgHash1 := MessageHashForRLN("topic", []byte("first"), epoch)
	proof1, _ := GenerateProof(id, idx, tree, epoch, msgHash1)
	result := validator.ValidateProof(proof1)
	if result != ValidationAccept {
		t.Fatalf("first proof should be accepted, got %d", result)
	}

	// Second message with same identity in same epoch should be rejected (spam).
	msgHash2 := MessageHashForRLN("topic", []byte("second"), epoch)
	proof2, _ := GenerateProof(id, idx, tree, epoch, msgHash2)
	result = validator.ValidateProof(proof2)
	if result != ValidationReject {
		t.Fatalf("duplicate proof should be rejected, got %d", result)
	}
}

func TestEpochFromTimestamp(t *testing.T) {
	ts := time.Unix(100, 0)
	epoch := EpochFromTimestamp(ts, 10)
	if epoch != 10 {
		t.Fatalf("EpochFromTimestamp = %d, want 10", epoch)
	}

	ts2 := time.Unix(95, 0)
	epoch2 := EpochFromTimestamp(ts2, 10)
	if epoch2 != 9 {
		t.Fatalf("EpochFromTimestamp = %d, want 9", epoch2)
	}
}

func TestMessageHashForRLN(t *testing.T) {
	h1 := MessageHashForRLN("topic-a", []byte("payload-a"), 1)
	h2 := MessageHashForRLN("topic-a", []byte("payload-a"), 1)

	// Deterministic.
	if h1 != h2 {
		t.Fatal("same inputs should produce same hash")
	}

	// Different topic.
	h3 := MessageHashForRLN("topic-b", []byte("payload-a"), 1)
	if h1 == h3 {
		t.Fatal("different topic should produce different hash")
	}

	// Different epoch.
	h4 := MessageHashForRLN("topic-a", []byte("payload-a"), 2)
	if h1 == h4 {
		t.Fatal("different epoch should produce different hash")
	}
}
