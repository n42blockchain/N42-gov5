// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ethics committee voting. CommitteeConfig declares quorum and
// approval threshold, Committee aggregates secp256k1-signed votes
// per (datasetID, category), recovering voter addresses via
// crypto.SigToPub. Finalises a dataset to Approved or Rejected once
// quorum is reached across the required ethics categories.

package governance

import (
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// CommitteeConfig holds the parameters for ethics committee voting.
type CommitteeConfig struct {
	// Quorum is the minimum number of non-abstain votes required for a valid tally.
	Quorum int
	// Threshold is the minimum ratio of approvals to (approvals + rejections)
	// required for a category to pass. Must be in the range [0.0, 1.0].
	Threshold float64
}

// Committee manages a human ethics review committee that votes on training
// dataset approvals. It enforces quorum requirements, threshold-based
// approval, and cryptographic vote verification.
//
// All methods are safe for concurrent use.
type Committee struct {
	mu       sync.RWMutex
	members  map[types.Address]bool
	votes    map[types.Hash]map[types.Address]*Vote          // datasetID -> voter -> vote
	results  map[types.Hash]map[EthicsCategory]*ReviewResult // datasetID -> category -> result
	config   CommitteeConfig
	registry *DatasetRegistry
}

// NewCommittee creates a new ethics review committee with the given
// configuration and dataset registry.
//
// Panics if registry is nil. Defaults Quorum to 1 if less than 1, and
// clamps Threshold to [0.0, 1.0].
func NewCommittee(cfg CommitteeConfig, registry *DatasetRegistry) *Committee {
	if registry == nil {
		panic("governance: registry must not be nil")
	}
	if cfg.Quorum < 1 {
		cfg.Quorum = 1
	}
	if cfg.Threshold < 0 {
		cfg.Threshold = 0
	}
	if cfg.Threshold > 1.0 {
		cfg.Threshold = 1.0
	}
	return &Committee{
		members:  make(map[types.Address]bool),
		votes:    make(map[types.Hash]map[types.Address]*Vote),
		results:  make(map[types.Hash]map[EthicsCategory]*ReviewResult),
		config:   cfg,
		registry: registry,
	}
}

// AddMember adds an address to the ethics review committee.
// If the address is already a member this is a no-op.
func (c *Committee) AddMember(addr types.Address) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.members[addr] = true
	return nil
}

// RemoveMember removes an address from the ethics review committee.
// If the address is not a member this is a no-op.
func (c *Committee) RemoveMember(addr types.Address) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.members, addr)
	return nil
}

// IsMember returns true if the given address is currently a committee member.
func (c *Committee) IsMember(addr types.Address) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.members[addr]
}

// MemberCount returns the current number of committee members.
func (c *Committee) MemberCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.members)
}

// CastVote records a committee member's vote on a dataset. The vote is
// validated for membership, duplicate prevention, dataset status, and
// cryptographic signature verification.
//
// The signature must be a 65-byte secp256k1 recoverable signature over the
// Keccak256 hash of (datasetID || byte(decision) || byte(category)). The
// recovered address must match the declared voter.
//
// On the first vote received for a dataset, its status is transitioned from
// Pending to UnderReview.
//
// Errors:
//   - ErrNotCommitteeMember if the voter is not a member
//   - ErrDatasetNotFound if the dataset does not exist
//   - ErrVotingClosed if the dataset status is not Pending or UnderReview
//   - ErrAlreadyVoted if the member has already voted on this dataset
//   - ErrInvalidSignature if signature recovery or address matching fails
func (c *Committee) CastVote(vote *Vote) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 1. Membership check.
	if !c.members[vote.Voter] {
		return ErrNotCommitteeMember
	}

	// 2. Dataset existence and status check.
	// Hold the registry lock to prevent races with concurrent registry mutations.
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()

	ds, ok := c.registry.getDatasetUnsafe(vote.DatasetID)
	if !ok {
		return ErrDatasetNotFound
	}
	if ds.Status != DatasetPending && ds.Status != DatasetUnderReview {
		return ErrVotingClosed
	}

	// 3. Duplicate vote check.
	if voters, exists := c.votes[vote.DatasetID]; exists {
		if _, voted := voters[vote.Voter]; voted {
			return ErrAlreadyVoted
		}
	}

	// 4. Signature verification.
	if err := verifyVoteSignature(vote); err != nil {
		return err
	}

	// 5. Record vote.
	if c.votes[vote.DatasetID] == nil {
		c.votes[vote.DatasetID] = make(map[types.Address]*Vote)
	}

	// Store a copy to prevent external mutation.
	stored := *vote
	if len(vote.Signature) > 0 {
		stored.Signature = make([]byte, len(vote.Signature))
		copy(stored.Signature, vote.Signature)
	}
	c.votes[vote.DatasetID][vote.Voter] = &stored

	// 6. Transition dataset to UnderReview on first vote.
	if ds.Status == DatasetPending {
		if err := c.registry.updateStatusUnsafe(vote.DatasetID, DatasetUnderReview); err != nil {
			return fmt.Errorf("governance: failed to transition dataset to UnderReview: %w", err)
		}
	}

	return nil
}

// GetVotes returns copies of all votes cast for the given dataset.
func (c *Committee) GetVotes(datasetID types.Hash) []*Vote {
	c.mu.RLock()
	defer c.mu.RUnlock()

	voters, ok := c.votes[datasetID]
	if !ok {
		return nil
	}

	result := make([]*Vote, 0, len(voters))
	for _, v := range voters {
		cp := *v
		if len(v.Signature) > 0 {
			cp.Signature = make([]byte, len(v.Signature))
			copy(cp.Signature, v.Signature)
		}
		result = append(result, &cp)
	}
	return result
}

// TallyVotes counts the votes for a specific ethics category on the given
// dataset, applies quorum and threshold checks, and returns the result.
//
// Errors:
//   - ErrDatasetNotFound if the dataset does not exist
//   - ErrQuorumNotReached if the non-abstain vote count is below the quorum
func (c *Committee) TallyVotes(datasetID types.Hash, category EthicsCategory) (*ReviewResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.registry.mu.RLock()
	defer c.registry.mu.RUnlock()

	return c.tallyVotesLocked(datasetID, category)
}

// tallyVotesLocked is the internal implementation of TallyVotes. The caller
// MUST hold c.mu and at least a read lock on c.registry.mu before calling.
func (c *Committee) tallyVotesLocked(datasetID types.Hash, category EthicsCategory) (*ReviewResult, error) {
	// Verify dataset exists.
	if _, ok := c.registry.getDatasetUnsafe(datasetID); !ok {
		return nil, ErrDatasetNotFound
	}

	voters := c.votes[datasetID]

	var approveCount, rejectCount, abstainCount int
	for _, v := range voters {
		if v.Category != category {
			continue
		}
		switch v.Decision {
		case VoteApprove:
			approveCount++
		case VoteReject:
			rejectCount++
		case VoteAbstain:
			abstainCount++
		}
	}

	nonAbstain := approveCount + rejectCount
	if nonAbstain < c.config.Quorum {
		return nil, ErrQuorumNotReached
	}

	// Determine whether the category passed.
	var passed bool
	if nonAbstain > 0 {
		ratio := float64(approveCount) / float64(nonAbstain)
		passed = ratio >= c.config.Threshold
	}

	result := &ReviewResult{
		DatasetID:    datasetID,
		Category:     category,
		ApproveCount: approveCount,
		RejectCount:  rejectCount,
		AbstainCount: abstainCount,
		Quorum:       c.config.Quorum,
		Threshold:    c.config.Threshold,
		Passed:       passed,
		FinalizedAt:  time.Now(),
	}

	// Cache the result.
	if c.results[datasetID] == nil {
		c.results[datasetID] = make(map[EthicsCategory]*ReviewResult)
	}
	c.results[datasetID][category] = result

	return result, nil
}

// FinalizeReview tallies all required ethics categories for the given dataset
// and updates its status to Approved (if all categories pass) or Rejected
// (if any category fails).
//
// The lock is held throughout the entire operation to prevent concurrent
// finalization (TOCTOU race).
//
// Errors:
//   - ErrDatasetNotFound if the dataset does not exist
//   - ErrDatasetAlreadyReviewed if the dataset is already Approved/Rejected/Revoked
//   - ErrQuorumNotReached if any category has insufficient votes
func (c *Committee) FinalizeReview(datasetID types.Hash) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()

	ds, ok := c.registry.getDatasetUnsafe(datasetID)
	if !ok {
		return ErrDatasetNotFound
	}
	if ds.Status == DatasetApproved || ds.Status == DatasetRejected || ds.Status == DatasetRevoked {
		return ErrDatasetAlreadyReviewed
	}

	categories := make([]EthicsCategory, len(ds.Categories))
	copy(categories, ds.Categories)

	allPassed := true
	for _, cat := range categories {
		result, err := c.tallyVotesLocked(datasetID, cat)
		if err != nil {
			return err
		}
		if !result.Passed {
			allPassed = false
		}
	}

	if allPassed {
		return c.registry.updateStatusUnsafe(datasetID, DatasetApproved)
	}
	return c.registry.updateStatusUnsafe(datasetID, DatasetRejected)
}

// GetResult returns the cached review result for a specific dataset and
// category. Returns false if no result has been tallied yet.
func (c *Committee) GetResult(datasetID types.Hash, category EthicsCategory) (*ReviewResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cats, ok := c.results[datasetID]
	if !ok {
		return nil, false
	}
	r, ok := cats[category]
	if !ok {
		return nil, false
	}

	// Return a copy.
	cp := *r
	return &cp, true
}

// IsApproved returns true if and only if the dataset status is DatasetApproved.
func (c *Committee) IsApproved(datasetID types.Hash) bool {
	ds, ok := c.registry.Get(datasetID)
	if !ok {
		return false
	}
	return ds.Status == DatasetApproved
}

// RevokeDataset sets the dataset status to DatasetRevoked, preventing any
// further voting or finalization. This can be used to revoke a previously
// approved dataset or to cancel an in-progress review.
//
// Errors:
//   - ErrDatasetNotFound if the dataset does not exist
func (c *Committee) RevokeDataset(datasetID types.Hash) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()

	if _, ok := c.registry.getDatasetUnsafe(datasetID); !ok {
		return ErrDatasetNotFound
	}
	return c.registry.updateStatusUnsafe(datasetID, DatasetRevoked)
}

// verifyVoteSignature verifies the secp256k1 recoverable signature on a vote.
// The signed message is Keccak256(datasetID || byte(decision) || byte(category)).
func verifyVoteSignature(vote *Vote) error {
	if len(vote.Signature) != 65 {
		return ErrInvalidSignature
	}

	// Build the message: datasetID (32 bytes) || decision (1 byte) || category (1 byte).
	msg := make([]byte, types.HashLength+2)
	copy(msg, vote.DatasetID[:])
	msg[types.HashLength] = byte(vote.Decision)
	msg[types.HashLength+1] = byte(vote.Category)

	// Hash the message.
	hash := crypto.Keccak256(msg)

	// Recover the public key from the signature.
	pubkey, err := crypto.SigToPub(hash, vote.Signature)
	if err != nil {
		return ErrInvalidSignature
	}

	// Derive the address from the recovered public key.
	recovered := crypto.PubkeyToAddress(*pubkey)

	// Compare against the declared voter address.
	if recovered != vote.Voter {
		return ErrInvalidSignature
	}

	return nil
}

// getDatasetUnsafe returns the internal dataset pointer without copying or
// locking. The caller MUST hold a lock that prevents concurrent registry
// mutation (either r.mu directly, or an external lock that serialises all
// registry access).
func (r *DatasetRegistry) getDatasetUnsafe(datasetID types.Hash) (*Dataset, bool) {
	ds, ok := r.datasets[datasetID]
	return ds, ok
}

// updateStatusUnsafe changes the status without acquiring r.mu.
// The caller MUST hold an external lock that serialises registry writes.
func (r *DatasetRegistry) updateStatusUnsafe(datasetID types.Hash, status DatasetStatus) error {
	ds, ok := r.datasets[datasetID]
	if !ok {
		return ErrDatasetNotFound
	}
	ds.Status = status
	return nil
}
