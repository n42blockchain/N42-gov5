// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package governance

import (
	"crypto/ecdsa"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// helper: register a dataset with sensible defaults and return the ID.
func registerTestDataset(t *testing.T, reg *DatasetRegistry, owner types.Address, name, version string, categories []EthicsCategory) types.Hash {
	t.Helper()
	contentHash := types.BytesToHash(crypto.Keccak256([]byte(name + version + "content")))
	metadataHash := types.BytesToHash(crypto.Keccak256([]byte(name + version + "metadata")))
	id, err := reg.Register(owner, name, version, contentHash, metadataHash, 1024, 100, categories)
	if err != nil {
		t.Fatalf("Register(%s, %s): unexpected error: %v", name, version, err)
	}
	return id
}

// TestDatasetRegistry_RegisterAndGet registers a dataset and verifies that
// Get returns a copy with all fields intact.
func TestDatasetRegistry_RegisterAndGet(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0x1111111111111111111111111111111111111111")
	name := "test-dataset"
	version := "v1.0"
	contentHash := types.BytesToHash(crypto.Keccak256([]byte("raw-data")))
	metadataHash := types.BytesToHash(crypto.Keccak256([]byte("metadata")))
	categories := []EthicsCategory{CategoryFairness, CategoryPrivacy}
	size := uint64(4096)
	recordCount := uint64(500)

	before := time.Now()
	id, err := reg.Register(owner, name, version, contentHash, metadataHash, size, recordCount, categories)
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	if id == (types.Hash{}) {
		t.Fatal("Register: returned zero hash")
	}

	ds, ok := reg.Get(id)
	if !ok {
		t.Fatal("Get: dataset not found after registration")
	}

	if ds.ID != id {
		t.Errorf("ID: got %s, want %s", ds.ID, id)
	}
	if ds.ContentHash != contentHash {
		t.Errorf("ContentHash: got %s, want %s", ds.ContentHash, contentHash)
	}
	if ds.MetadataHash != metadataHash {
		t.Errorf("MetadataHash: got %s, want %s", ds.MetadataHash, metadataHash)
	}
	if ds.Owner != owner {
		t.Errorf("Owner: got %s, want %s", ds.Owner, owner)
	}
	if ds.Name != name {
		t.Errorf("Name: got %s, want %s", ds.Name, name)
	}
	if ds.Version != version {
		t.Errorf("Version: got %s, want %s", ds.Version, version)
	}
	if ds.Size != size {
		t.Errorf("Size: got %d, want %d", ds.Size, size)
	}
	if ds.RecordCount != recordCount {
		t.Errorf("RecordCount: got %d, want %d", ds.RecordCount, recordCount)
	}
	if len(ds.Categories) != len(categories) {
		t.Fatalf("Categories length: got %d, want %d", len(ds.Categories), len(categories))
	}
	for i, cat := range ds.Categories {
		if cat != categories[i] {
			t.Errorf("Categories[%d]: got %v, want %v", i, cat, categories[i])
		}
	}
	if ds.Status != DatasetPending {
		t.Errorf("Status: got %v, want %v", ds.Status, DatasetPending)
	}
	if ds.RegisteredAt.Before(before) {
		t.Errorf("RegisteredAt: %v is before test start %v", ds.RegisteredAt, before)
	}
	if reg.Count() != 1 {
		t.Errorf("Count: got %d, want 1", reg.Count())
	}
}

// TestDatasetRegistry_DuplicateRejection verifies that registering a dataset
// with the same owner+name+version twice returns ErrDatasetExists.
func TestDatasetRegistry_DuplicateRejection(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0x2222222222222222222222222222222222222222")
	name := "dup-dataset"
	version := "v1.0"
	cats := []EthicsCategory{CategoryFairness}

	_ = registerTestDataset(t, reg, owner, name, version, cats)

	contentHash := types.BytesToHash(crypto.Keccak256([]byte("different-content")))
	metadataHash := types.BytesToHash(crypto.Keccak256([]byte("different-metadata")))
	_, err := reg.Register(owner, name, version, contentHash, metadataHash, 2048, 200, cats)
	if err != ErrDatasetExists {
		t.Fatalf("duplicate Register: got %v, want %v", err, ErrDatasetExists)
	}
	if reg.Count() != 1 {
		t.Errorf("Count after duplicate: got %d, want 1", reg.Count())
	}
}

// TestDatasetRegistry_Capacity fills the registry to its maximum capacity and
// verifies that subsequent registrations return ErrRegistryFull.
func TestDatasetRegistry_Capacity(t *testing.T) {
	const capacity = 3
	reg := NewDatasetRegistry(capacity)

	owner := types.HexToAddress("0x3333333333333333333333333333333333333333")
	cats := []EthicsCategory{CategoryContentSafety}

	for i := 0; i < capacity; i++ {
		registerTestDataset(t, reg, owner, "dataset", "v"+string(rune('0'+i)), cats)
	}
	if reg.Count() != capacity {
		t.Fatalf("Count: got %d, want %d", reg.Count(), capacity)
	}

	contentHash := types.BytesToHash(crypto.Keccak256([]byte("overflow")))
	metadataHash := types.BytesToHash(crypto.Keccak256([]byte("overflow-meta")))
	_, err := reg.Register(owner, "overflow-dataset", "v1", contentHash, metadataHash, 512, 50, cats)
	if err != ErrRegistryFull {
		t.Fatalf("overflow Register: got %v, want %v", err, ErrRegistryFull)
	}
}

// TestDatasetRegistry_GetByOwner registers multiple datasets for one owner and
// verifies that GetByOwner returns all of them.
func TestDatasetRegistry_GetByOwner(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0x4444444444444444444444444444444444444444")
	other := types.HexToAddress("0x5555555555555555555555555555555555555555")
	cats := []EthicsCategory{CategoryTransparency}

	id1 := registerTestDataset(t, reg, owner, "ds-a", "v1", cats)
	id2 := registerTestDataset(t, reg, owner, "ds-b", "v1", cats)
	_ = registerTestDataset(t, reg, other, "ds-c", "v1", cats)

	ownerDatasets := reg.GetByOwner(owner)
	if len(ownerDatasets) != 2 {
		t.Fatalf("GetByOwner: got %d datasets, want 2", len(ownerDatasets))
	}

	ids := make(map[types.Hash]bool)
	for _, ds := range ownerDatasets {
		ids[ds.ID] = true
	}
	if !ids[id1] || !ids[id2] {
		t.Errorf("GetByOwner: missing expected dataset IDs")
	}

	// Owner with no datasets returns nil.
	unknown := types.HexToAddress("0x6666666666666666666666666666666666666666")
	if got := reg.GetByOwner(unknown); got != nil {
		t.Errorf("GetByOwner(unknown): got %v, want nil", got)
	}
}

// TestDatasetRegistry_LinkToModel links datasets to a model hash and verifies
// that GetModelDatasets returns the correct set.
func TestDatasetRegistry_LinkToModel(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0x7777777777777777777777777777777777777777")
	cats := []EthicsCategory{CategoryFairness}

	id1 := registerTestDataset(t, reg, owner, "train-a", "v1", cats)
	id2 := registerTestDataset(t, reg, owner, "train-b", "v1", cats)

	modelHash := types.BytesToHash(crypto.Keccak256([]byte("model-gpt5")))

	if err := reg.LinkToModel(id1, modelHash); err != nil {
		t.Fatalf("LinkToModel(id1): %v", err)
	}
	if err := reg.LinkToModel(id2, modelHash); err != nil {
		t.Fatalf("LinkToModel(id2): %v", err)
	}
	// Duplicate link should be a no-op, not an error.
	if err := reg.LinkToModel(id1, modelHash); err != nil {
		t.Fatalf("LinkToModel(id1 duplicate): %v", err)
	}

	linked := reg.GetModelDatasets(modelHash)
	if len(linked) != 2 {
		t.Fatalf("GetModelDatasets: got %d, want 2", len(linked))
	}

	idSet := make(map[types.Hash]bool)
	for _, h := range linked {
		idSet[h] = true
	}
	if !idSet[id1] || !idSet[id2] {
		t.Error("GetModelDatasets: missing expected dataset IDs")
	}

	// Link to nonexistent dataset should return ErrDatasetNotFound.
	fakeID := types.BytesToHash(crypto.Keccak256([]byte("nonexistent")))
	if err := reg.LinkToModel(fakeID, modelHash); err != ErrDatasetNotFound {
		t.Errorf("LinkToModel(fake): got %v, want %v", err, ErrDatasetNotFound)
	}

	// GetModelDatasets for unknown model returns nil.
	unknownModel := types.BytesToHash(crypto.Keccak256([]byte("unknown-model")))
	if got := reg.GetModelDatasets(unknownModel); got != nil {
		t.Errorf("GetModelDatasets(unknown): got %v, want nil", got)
	}
}

// TestDatasetRegistry_UpdateStatus verifies that UpdateStatus changes the
// dataset status and the change persists on subsequent Get calls.
func TestDatasetRegistry_UpdateStatus(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0x8888888888888888888888888888888888888888")
	cats := []EthicsCategory{CategoryPrivacy}
	id := registerTestDataset(t, reg, owner, "status-test", "v1", cats)

	// Verify initial status is Pending.
	ds, _ := reg.Get(id)
	if ds.Status != DatasetPending {
		t.Fatalf("initial Status: got %v, want %v", ds.Status, DatasetPending)
	}

	// Transition through statuses.
	transitions := []DatasetStatus{DatasetUnderReview, DatasetApproved, DatasetRevoked}
	for _, status := range transitions {
		if err := reg.UpdateStatus(id, status); err != nil {
			t.Fatalf("UpdateStatus(%v): %v", status, err)
		}
		ds, _ = reg.Get(id)
		if ds.Status != status {
			t.Errorf("after UpdateStatus: got %v, want %v", ds.Status, status)
		}
	}

	// UpdateStatus on nonexistent dataset returns ErrDatasetNotFound.
	fakeID := types.BytesToHash(crypto.Keccak256([]byte("no-such-dataset")))
	if err := reg.UpdateStatus(fakeID, DatasetApproved); err != ErrDatasetNotFound {
		t.Errorf("UpdateStatus(fake): got %v, want %v", err, ErrDatasetNotFound)
	}
}

// TestCommittee_MemberManagement verifies add, remove, check, and count
// operations on the committee member set.
func TestCommittee_MemberManagement(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	addr1 := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addr2 := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	if c.MemberCount() != 0 {
		t.Fatalf("initial MemberCount: got %d, want 0", c.MemberCount())
	}

	// Add members.
	if err := c.AddMember(addr1); err != nil {
		t.Fatalf("AddMember(addr1): %v", err)
	}
	if err := c.AddMember(addr2); err != nil {
		t.Fatalf("AddMember(addr2): %v", err)
	}

	if c.MemberCount() != 2 {
		t.Errorf("MemberCount after adds: got %d, want 2", c.MemberCount())
	}
	if !c.IsMember(addr1) {
		t.Error("IsMember(addr1): got false, want true")
	}
	if !c.IsMember(addr2) {
		t.Error("IsMember(addr2): got false, want true")
	}

	// Adding the same member again is a no-op.
	if err := c.AddMember(addr1); err != nil {
		t.Fatalf("AddMember(addr1 duplicate): %v", err)
	}
	if c.MemberCount() != 2 {
		t.Errorf("MemberCount after duplicate add: got %d, want 2", c.MemberCount())
	}

	// Remove a member.
	if err := c.RemoveMember(addr1); err != nil {
		t.Fatalf("RemoveMember(addr1): %v", err)
	}
	if c.IsMember(addr1) {
		t.Error("IsMember(addr1) after removal: got true, want false")
	}
	if c.MemberCount() != 1 {
		t.Errorf("MemberCount after removal: got %d, want 1", c.MemberCount())
	}

	// Removing a non-member is a no-op.
	if err := c.RemoveMember(addr1); err != nil {
		t.Fatalf("RemoveMember(addr1 again): %v", err)
	}

	// Check a non-member.
	unknown := types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")
	if c.IsMember(unknown) {
		t.Error("IsMember(unknown): got true, want false")
	}
}

// TestCommittee_CastVote_NoSignature verifies that CastVote rejects a vote
// with an empty signature, validating the ErrInvalidSignature error path.
func TestCommittee_CastVote_NoSignature(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	voter := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	cats := []EthicsCategory{CategoryFairness}
	dsID := registerTestDataset(t, reg, voter, "nosig-test", "v1", cats)

	if err := c.AddMember(voter); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	vote := &Vote{
		DatasetID: dsID,
		Voter:     voter,
		Decision:  VoteApprove,
		Category:  CategoryFairness,
		Reason:    "looks good",
		Timestamp: time.Now(),
		Signature: nil, // empty signature
	}

	err := c.CastVote(vote)
	if err != ErrInvalidSignature {
		t.Fatalf("CastVote(empty sig): got %v, want %v", err, ErrInvalidSignature)
	}
}

// signVote creates a valid 65-byte secp256k1 recoverable signature for a vote
// using the provided private key.
func signVote(t *testing.T, key *ecdsa.PrivateKey, datasetID types.Hash, decision VoteDecision, category EthicsCategory) []byte {
	t.Helper()
	msg := make([]byte, types.HashLength+2)
	copy(msg, datasetID[:])
	msg[types.HashLength] = byte(decision)
	msg[types.HashLength+1] = byte(category)
	hash := crypto.Keccak256(msg)
	sig, err := crypto.Sign(hash, key)
	if err != nil {
		t.Fatalf("crypto.Sign: %v", err)
	}
	return sig
}

// TestCommittee_VotingLifecycle exercises the full voting flow: generate a real
// key, sign a vote, cast it, tally, and finalize.
func TestCommittee_VotingLifecycle(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	// Generate a real key and derive the voter address.
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	voter := crypto.PubkeyToAddress(key.PublicKey)

	cats := []EthicsCategory{CategoryFairness}
	dsID := registerTestDataset(t, reg, voter, "lifecycle-test", "v1", cats)

	if err := c.AddMember(voter); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Sign and cast vote.
	sig := signVote(t, key, dsID, VoteApprove, CategoryFairness)
	vote := &Vote{
		DatasetID: dsID,
		Voter:     voter,
		Decision:  VoteApprove,
		Category:  CategoryFairness,
		Reason:    "dataset is fair",
		Timestamp: time.Now(),
		Signature: sig,
	}
	if err := c.CastVote(vote); err != nil {
		t.Fatalf("CastVote: %v", err)
	}

	// Dataset should be UnderReview after first vote.
	ds, _ := reg.Get(dsID)
	if ds.Status != DatasetUnderReview {
		t.Errorf("Status after vote: got %v, want %v", ds.Status, DatasetUnderReview)
	}

	// Verify votes are retrievable.
	votes := c.GetVotes(dsID)
	if len(votes) != 1 {
		t.Fatalf("GetVotes: got %d votes, want 1", len(votes))
	}
	if votes[0].Decision != VoteApprove {
		t.Errorf("Vote decision: got %v, want %v", votes[0].Decision, VoteApprove)
	}

	// Tally.
	result, err := c.TallyVotes(dsID, CategoryFairness)
	if err != nil {
		t.Fatalf("TallyVotes: %v", err)
	}
	if result.ApproveCount != 1 {
		t.Errorf("ApproveCount: got %d, want 1", result.ApproveCount)
	}
	if result.RejectCount != 0 {
		t.Errorf("RejectCount: got %d, want 0", result.RejectCount)
	}
	if !result.Passed {
		t.Error("Passed: got false, want true")
	}

	// Finalize — should set status to Approved since the only category passed.
	if err := c.FinalizeReview(dsID); err != nil {
		t.Fatalf("FinalizeReview: %v", err)
	}
	ds, _ = reg.Get(dsID)
	if ds.Status != DatasetApproved {
		t.Errorf("Status after finalize: got %v, want %v", ds.Status, DatasetApproved)
	}
}

// TestCommittee_QuorumEnforcement sets quorum=3, adds only 2 voters, and
// verifies that TallyVotes returns ErrQuorumNotReached.
func TestCommittee_QuorumEnforcement(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 3, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	// Generate 2 voters (quorum needs 3).
	var voters [2]types.Address
	var keys [2]*ecdsa.PrivateKey
	for i := range voters {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey[%d]: %v", i, err)
		}
		keys[i] = k
		voters[i] = crypto.PubkeyToAddress(k.PublicKey)
		if err := c.AddMember(voters[i]); err != nil {
			t.Fatalf("AddMember[%d]: %v", i, err)
		}
	}

	cats := []EthicsCategory{CategoryPrivacy}
	dsID := registerTestDataset(t, reg, voters[0], "quorum-test", "v1", cats)

	// Cast 2 votes.
	for i := range voters {
		sig := signVote(t, keys[i], dsID, VoteApprove, CategoryPrivacy)
		v := &Vote{
			DatasetID: dsID,
			Voter:     voters[i],
			Decision:  VoteApprove,
			Category:  CategoryPrivacy,
			Timestamp: time.Now(),
			Signature: sig,
		}
		if err := c.CastVote(v); err != nil {
			t.Fatalf("CastVote[%d]: %v", i, err)
		}
	}

	// Tally should fail: 2 votes < quorum of 3.
	_, err := c.TallyVotes(dsID, CategoryPrivacy)
	if err != ErrQuorumNotReached {
		t.Fatalf("TallyVotes: got %v, want %v", err, ErrQuorumNotReached)
	}
}

// TestCommittee_ThresholdEnforcement sets threshold=0.67 with 3 voters where
// 1 approves and 2 reject, verifying that the result is Passed=false.
func TestCommittee_ThresholdEnforcement(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.67}
	c := NewCommittee(cfg, reg)

	var voters [3]types.Address
	var keys [3]*ecdsa.PrivateKey
	for i := range voters {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey[%d]: %v", i, err)
		}
		keys[i] = k
		voters[i] = crypto.PubkeyToAddress(k.PublicKey)
		if err := c.AddMember(voters[i]); err != nil {
			t.Fatalf("AddMember[%d]: %v", i, err)
		}
	}

	cats := []EthicsCategory{CategoryContentSafety}
	dsID := registerTestDataset(t, reg, voters[0], "threshold-test", "v1", cats)

	// Voter 0: approve. Voters 1, 2: reject.
	decisions := []VoteDecision{VoteApprove, VoteReject, VoteReject}
	for i := range voters {
		sig := signVote(t, keys[i], dsID, decisions[i], CategoryContentSafety)
		v := &Vote{
			DatasetID: dsID,
			Voter:     voters[i],
			Decision:  decisions[i],
			Category:  CategoryContentSafety,
			Timestamp: time.Now(),
			Signature: sig,
		}
		if err := c.CastVote(v); err != nil {
			t.Fatalf("CastVote[%d]: %v", i, err)
		}
	}

	result, err := c.TallyVotes(dsID, CategoryContentSafety)
	if err != nil {
		t.Fatalf("TallyVotes: %v", err)
	}

	// 1 approve / 3 total = 0.333... < 0.67 threshold.
	if result.Passed {
		t.Error("Passed: got true, want false (1/3 < 0.67)")
	}
	if result.ApproveCount != 1 {
		t.Errorf("ApproveCount: got %d, want 1", result.ApproveCount)
	}
	if result.RejectCount != 2 {
		t.Errorf("RejectCount: got %d, want 2", result.RejectCount)
	}
}

// TestCommittee_DuplicateVoteRejected verifies that a committee member cannot
// vote twice on the same dataset.
func TestCommittee_DuplicateVoteRejected(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	voter := crypto.PubkeyToAddress(key.PublicKey)

	cats := []EthicsCategory{CategoryFairness}
	dsID := registerTestDataset(t, reg, voter, "dup-vote-test", "v1", cats)

	if err := c.AddMember(voter); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// First vote should succeed.
	sig1 := signVote(t, key, dsID, VoteApprove, CategoryFairness)
	v1 := &Vote{
		DatasetID: dsID,
		Voter:     voter,
		Decision:  VoteApprove,
		Category:  CategoryFairness,
		Timestamp: time.Now(),
		Signature: sig1,
	}
	if err := c.CastVote(v1); err != nil {
		t.Fatalf("CastVote(first): %v", err)
	}

	// Second vote should fail with ErrAlreadyVoted.
	sig2 := signVote(t, key, dsID, VoteReject, CategoryFairness)
	v2 := &Vote{
		DatasetID: dsID,
		Voter:     voter,
		Decision:  VoteReject,
		Category:  CategoryFairness,
		Timestamp: time.Now(),
		Signature: sig2,
	}
	err = c.CastVote(v2)
	if err != ErrAlreadyVoted {
		t.Fatalf("CastVote(second): got %v, want %v", err, ErrAlreadyVoted)
	}
}

// TestCommittee_NonMemberRejected verifies that a non-committee-member's vote
// is rejected with ErrNotCommitteeMember.
func TestCommittee_NonMemberRejected(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	nonMember := crypto.PubkeyToAddress(key.PublicKey)

	owner := types.HexToAddress("0xdddddddddddddddddddddddddddddddddddddd")
	cats := []EthicsCategory{CategoryTransparency}
	dsID := registerTestDataset(t, reg, owner, "non-member-test", "v1", cats)

	sig := signVote(t, key, dsID, VoteApprove, CategoryTransparency)
	v := &Vote{
		DatasetID: dsID,
		Voter:     nonMember,
		Decision:  VoteApprove,
		Category:  CategoryTransparency,
		Timestamp: time.Now(),
		Signature: sig,
	}
	err = c.CastVote(v)
	if err != ErrNotCommitteeMember {
		t.Fatalf("CastVote(non-member): got %v, want %v", err, ErrNotCommitteeMember)
	}
}

// TestCommittee_IsApproved exercises a full approval lifecycle and verifies
// that IsApproved returns true only after the dataset has been approved.
func TestCommittee_IsApproved(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	// Generate two voters for two categories.
	key1, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey(1): %v", err)
	}
	voter1 := crypto.PubkeyToAddress(key1.PublicKey)

	key2, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey(2): %v", err)
	}
	voter2 := crypto.PubkeyToAddress(key2.PublicKey)

	if err := c.AddMember(voter1); err != nil {
		t.Fatalf("AddMember(1): %v", err)
	}
	if err := c.AddMember(voter2); err != nil {
		t.Fatalf("AddMember(2): %v", err)
	}

	cats := []EthicsCategory{CategoryFairness, CategoryPrivacy}
	dsID := registerTestDataset(t, reg, voter1, "approved-test", "v1", cats)

	// IsApproved should be false before any votes.
	if c.IsApproved(dsID) {
		t.Error("IsApproved before votes: got true, want false")
	}

	// Voter 1 approves Fairness.
	sig1 := signVote(t, key1, dsID, VoteApprove, CategoryFairness)
	if err := c.CastVote(&Vote{
		DatasetID: dsID,
		Voter:     voter1,
		Decision:  VoteApprove,
		Category:  CategoryFairness,
		Timestamp: time.Now(),
		Signature: sig1,
	}); err != nil {
		t.Fatalf("CastVote(voter1, Fairness): %v", err)
	}

	// Voter 2 approves Privacy.
	sig2 := signVote(t, key2, dsID, VoteApprove, CategoryPrivacy)
	if err := c.CastVote(&Vote{
		DatasetID: dsID,
		Voter:     voter2,
		Decision:  VoteApprove,
		Category:  CategoryPrivacy,
		Timestamp: time.Now(),
		Signature: sig2,
	}); err != nil {
		t.Fatalf("CastVote(voter2, Privacy): %v", err)
	}

	// Still not approved — not yet finalized.
	if c.IsApproved(dsID) {
		t.Error("IsApproved before finalize: got true, want false")
	}

	// Finalize.
	if err := c.FinalizeReview(dsID); err != nil {
		t.Fatalf("FinalizeReview: %v", err)
	}

	// Now it should be approved.
	if !c.IsApproved(dsID) {
		t.Error("IsApproved after finalize: got false, want true")
	}

	// Verify the dataset status directly.
	ds, _ := reg.Get(dsID)
	if ds.Status != DatasetApproved {
		t.Errorf("Status: got %v, want %v", ds.Status, DatasetApproved)
	}

	// Finalizing again should return ErrDatasetAlreadyReviewed.
	if err := c.FinalizeReview(dsID); err != ErrDatasetAlreadyReviewed {
		t.Errorf("FinalizeReview(again): got %v, want %v", err, ErrDatasetAlreadyReviewed)
	}

	// IsApproved for nonexistent dataset returns false.
	fakeID := types.BytesToHash(crypto.Keccak256([]byte("nonexistent")))
	if c.IsApproved(fakeID) {
		t.Error("IsApproved(fake): got true, want false")
	}
}

// TestCommittee_ConfigValidation verifies that NewCommittee clamps Quorum
// and Threshold to valid ranges.
func TestCommittee_ConfigValidation(t *testing.T) {
	reg := NewDatasetRegistry(100)

	// Quorum=0 should default to 1.
	c1 := NewCommittee(CommitteeConfig{Quorum: 0, Threshold: 0.5}, reg)
	if got := c1.config.Quorum; got != 1 {
		t.Errorf("Quorum=0: got %d, want 1", got)
	}
	if got := c1.config.Threshold; got != 0.5 {
		t.Errorf("Threshold=0.5: got %f, want 0.5", got)
	}

	// Threshold=1.5 should be clamped to 1.0.
	c2 := NewCommittee(CommitteeConfig{Quorum: 2, Threshold: 1.5}, reg)
	if got := c2.config.Threshold; got != 1.0 {
		t.Errorf("Threshold=1.5: got %f, want 1.0", got)
	}
	if got := c2.config.Quorum; got != 2 {
		t.Errorf("Quorum=2: got %d, want 2", got)
	}

	// Threshold=-0.5 should be clamped to 0.0.
	c3 := NewCommittee(CommitteeConfig{Quorum: 1, Threshold: -0.5}, reg)
	if got := c3.config.Threshold; got != 0.0 {
		t.Errorf("Threshold=-0.5: got %f, want 0.0", got)
	}

	// Negative quorum should also default to 1.
	c4 := NewCommittee(CommitteeConfig{Quorum: -5, Threshold: 0.5}, reg)
	if got := c4.config.Quorum; got != 1 {
		t.Errorf("Quorum=-5: got %d, want 1", got)
	}
}

// TestCommittee_NilRegistryPanics verifies that NewCommittee panics when
// given a nil registry.
func TestCommittee_NilRegistryPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewCommittee(nil registry) did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not a string: %v", r)
		}
		if msg != "governance: registry must not be nil" {
			t.Errorf("panic message: got %q, want %q", msg, "governance: registry must not be nil")
		}
	}()

	NewCommittee(CommitteeConfig{Quorum: 1, Threshold: 0.5}, nil)
}

// TestCommittee_RevokeDataset verifies that revoking a dataset prevents
// further voting and finalization.
func TestCommittee_RevokeDataset(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	voter := crypto.PubkeyToAddress(key.PublicKey)

	cats := []EthicsCategory{CategoryFairness}
	dsID := registerTestDataset(t, reg, voter, "revoke-test", "v1", cats)

	if err := c.AddMember(voter); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Revoke the dataset before any votes.
	if err := c.RevokeDataset(dsID); err != nil {
		t.Fatalf("RevokeDataset: %v", err)
	}

	// Verify the dataset status is Revoked.
	ds, ok := reg.Get(dsID)
	if !ok {
		t.Fatal("dataset not found after revocation")
	}
	if ds.Status != DatasetRevoked {
		t.Errorf("Status after revoke: got %v, want %v", ds.Status, DatasetRevoked)
	}

	// Attempt to vote on revoked dataset — should return ErrVotingClosed.
	sig := signVote(t, key, dsID, VoteApprove, CategoryFairness)
	vote := &Vote{
		DatasetID: dsID,
		Voter:     voter,
		Decision:  VoteApprove,
		Category:  CategoryFairness,
		Reason:    "good dataset",
		Timestamp: time.Now(),
		Signature: sig,
	}
	if err := c.CastVote(vote); err != ErrVotingClosed {
		t.Fatalf("CastVote on revoked dataset: got %v, want %v", err, ErrVotingClosed)
	}

	// Attempt to finalize revoked dataset — should return ErrDatasetAlreadyReviewed.
	if err := c.FinalizeReview(dsID); err != ErrDatasetAlreadyReviewed {
		t.Fatalf("FinalizeReview on revoked dataset: got %v, want %v", err, ErrDatasetAlreadyReviewed)
	}
}

// TestDatasetRegistry_PurgeFinalized verifies that PurgeFinalized removes
// only datasets in terminal statuses (Approved, Rejected, Revoked) and
// leaves pending datasets untouched.
func TestDatasetRegistry_PurgeFinalized(t *testing.T) {
	reg := NewDatasetRegistry(100)

	owner := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	cats := []EthicsCategory{CategoryFairness}

	// Dataset 1: will be approved.
	id1 := registerTestDataset(t, reg, owner, "purge-approved", "v1", cats)
	if err := reg.UpdateStatus(id1, DatasetApproved); err != nil {
		t.Fatalf("UpdateStatus(Approved): %v", err)
	}

	// Dataset 2: will be rejected.
	id2 := registerTestDataset(t, reg, owner, "purge-rejected", "v1", cats)
	if err := reg.UpdateStatus(id2, DatasetRejected); err != nil {
		t.Fatalf("UpdateStatus(Rejected): %v", err)
	}

	// Dataset 3: stays pending.
	id3 := registerTestDataset(t, reg, owner, "purge-pending", "v1", cats)

	if reg.Count() != 3 {
		t.Fatalf("Count before purge: got %d, want 3", reg.Count())
	}

	purged := reg.PurgeFinalized()
	if purged != 2 {
		t.Errorf("PurgeFinalized: got %d purged, want 2", purged)
	}
	if reg.Count() != 1 {
		t.Errorf("Count after purge: got %d, want 1", reg.Count())
	}

	// The pending dataset should still be accessible.
	if _, ok := reg.Get(id3); !ok {
		t.Error("pending dataset not found after purge")
	}

	// The approved and rejected datasets should be gone.
	if _, ok := reg.Get(id1); ok {
		t.Error("approved dataset still found after purge")
	}
	if _, ok := reg.Get(id2); ok {
		t.Error("rejected dataset still found after purge")
	}
}

// TestCommittee_ConcurrentVoting verifies that concurrent voting from
// multiple goroutines is race-free and all valid votes are recorded.
func TestCommittee_ConcurrentVoting(t *testing.T) {
	reg := NewDatasetRegistry(100)
	cfg := CommitteeConfig{Quorum: 1, Threshold: 0.5}
	c := NewCommittee(cfg, reg)

	const numVoters = 5

	keys := make([]*ecdsa.PrivateKey, numVoters)
	voters := make([]types.Address, numVoters)
	for i := 0; i < numVoters; i++ {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey[%d]: %v", i, err)
		}
		keys[i] = k
		voters[i] = crypto.PubkeyToAddress(k.PublicKey)
		if err := c.AddMember(voters[i]); err != nil {
			t.Fatalf("AddMember[%d]: %v", i, err)
		}
	}

	cats := []EthicsCategory{CategoryFairness}
	dsID := registerTestDataset(t, reg, voters[0], "concurrent-vote-test", "v1", cats)

	var wg sync.WaitGroup
	errs := make([]error, numVoters)

	for i := 0; i < numVoters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sig := signVote(t, keys[idx], dsID, VoteApprove, CategoryFairness)
			v := &Vote{
				DatasetID: dsID,
				Voter:     voters[idx],
				Decision:  VoteApprove,
				Category:  CategoryFairness,
				Reason:    "concurrent approval",
				Timestamp: time.Now(),
				Signature: sig,
			}
			errs[idx] = c.CastVote(v)
		}(i)
	}
	wg.Wait()

	// All votes should have succeeded (each voter votes once).
	for i, err := range errs {
		if err != nil {
			t.Errorf("CastVote[%d]: unexpected error: %v", i, err)
		}
	}

	// All votes should be recorded.
	allVotes := c.GetVotes(dsID)
	if len(allVotes) != numVoters {
		t.Errorf("GetVotes: got %d votes, want %d", len(allVotes), numVoters)
	}
}
