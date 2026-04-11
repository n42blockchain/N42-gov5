// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Core types for training data governance: Dataset, Vote,
// ReviewResult plus enums DatasetStatus (Pending, UnderReview,
// Approved, Rejected) and EthicsCategory (Fairness, Privacy,
// ContentSafety, Transparency). Also defines the sentinel errors
// returned by the Committee and DatasetRegistry.

package governance

import (
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// DatasetStatus represents the lifecycle status of a training dataset.
type DatasetStatus uint8

const (
	// DatasetPending indicates the dataset has been registered but not yet reviewed.
	DatasetPending DatasetStatus = 0
	// DatasetUnderReview indicates voting has begun on the dataset.
	DatasetUnderReview DatasetStatus = 1
	// DatasetApproved indicates the dataset passed all required ethics reviews.
	DatasetApproved DatasetStatus = 2
	// DatasetRejected indicates the dataset failed one or more ethics reviews.
	DatasetRejected DatasetStatus = 3
	// DatasetRevoked indicates a previously approved dataset has been revoked.
	DatasetRevoked DatasetStatus = 4
)

// String returns a human-readable representation of the DatasetStatus.
func (s DatasetStatus) String() string {
	switch s {
	case DatasetPending:
		return "Pending"
	case DatasetUnderReview:
		return "UnderReview"
	case DatasetApproved:
		return "Approved"
	case DatasetRejected:
		return "Rejected"
	case DatasetRevoked:
		return "Revoked"
	default:
		return "Unknown"
	}
}

// EthicsCategory represents a category of ethical review required for a dataset.
type EthicsCategory uint8

const (
	// CategoryFairness covers bias, representativeness, and equitable outcomes.
	CategoryFairness EthicsCategory = 0
	// CategoryPrivacy covers PII exposure, consent, and data minimisation.
	CategoryPrivacy EthicsCategory = 1
	// CategoryContentSafety covers harmful, abusive, or illegal content.
	CategoryContentSafety EthicsCategory = 2
	// CategoryTransparency covers data provenance, licensing, and documentation.
	CategoryTransparency EthicsCategory = 3
)

// String returns a human-readable representation of the EthicsCategory.
func (c EthicsCategory) String() string {
	switch c {
	case CategoryFairness:
		return "Fairness"
	case CategoryPrivacy:
		return "Privacy"
	case CategoryContentSafety:
		return "ContentSafety"
	case CategoryTransparency:
		return "Transparency"
	default:
		return "Unknown"
	}
}

// Dataset represents a registered training dataset subject to governance review.
type Dataset struct {
	// ID is the unique identifier derived from Keccak256(owner || name || version).
	ID types.Hash
	// ContentHash is the Keccak256 digest of the raw training data.
	ContentHash types.Hash
	// Owner is the Ethereum address of the dataset registrant.
	Owner types.Address
	// Name is the human-readable dataset name.
	Name string
	// Version is the dataset version string.
	Version string
	// Size is the total size of the dataset in bytes.
	Size uint64
	// RecordCount is the number of records in the dataset.
	RecordCount uint64
	// Categories lists the ethics review categories required for this dataset.
	Categories []EthicsCategory
	// Status is the current lifecycle status.
	Status DatasetStatus
	// RegisteredAt is the time the dataset was registered.
	RegisteredAt time.Time
	// MetadataHash is the CAS hash of the dataset's metadata JSON.
	MetadataHash types.Hash
}

// VoteDecision represents a committee member's vote on a dataset.
type VoteDecision uint8

const (
	// VoteApprove indicates approval of the dataset for the given category.
	VoteApprove VoteDecision = 0
	// VoteReject indicates rejection of the dataset for the given category.
	VoteReject VoteDecision = 1
	// VoteAbstain indicates the voter abstains from the decision.
	VoteAbstain VoteDecision = 2
)

// Vote records a single committee member's decision on a dataset review.
type Vote struct {
	// DatasetID is the identifier of the dataset being reviewed.
	DatasetID types.Hash
	// Voter is the Ethereum address of the committee member casting the vote.
	Voter types.Address
	// Decision is the vote outcome (approve, reject, abstain).
	Decision VoteDecision
	// Category is the ethics category being voted on.
	Category EthicsCategory
	// Reason is an optional human-readable justification.
	Reason string
	// Timestamp is when the vote was cast.
	Timestamp time.Time
	// Signature is a secp256k1 signature over Keccak256(datasetID || decision || category).
	Signature []byte
}

// ReviewResult summarises the tally of votes for a specific ethics category.
type ReviewResult struct {
	// DatasetID is the identifier of the reviewed dataset.
	DatasetID types.Hash
	// Category is the ethics category this result pertains to.
	Category EthicsCategory
	// ApproveCount is the number of approval votes.
	ApproveCount int
	// RejectCount is the number of rejection votes.
	RejectCount int
	// AbstainCount is the number of abstention votes.
	AbstainCount int
	// Quorum is the minimum number of non-abstain votes required.
	Quorum int
	// Threshold is the minimum ratio of approvals to (approvals + rejections).
	Threshold float64
	// Passed indicates whether the category review passed.
	Passed bool
	// FinalizedAt is when the tally was completed.
	FinalizedAt time.Time
}

// Sentinel errors for the governance package.
var (
	// ErrDatasetExists is returned when registering a dataset with an ID that already exists.
	ErrDatasetExists = errors.New("governance: dataset already exists")
	// ErrDatasetNotFound is returned when a referenced dataset ID is not in the registry.
	ErrDatasetNotFound = errors.New("governance: dataset not found")
	// ErrNotCommitteeMember is returned when a non-member attempts a committee action.
	ErrNotCommitteeMember = errors.New("governance: address is not a committee member")
	// ErrAlreadyVoted is returned when a member tries to vote twice on the same dataset.
	ErrAlreadyVoted = errors.New("governance: member has already voted on this dataset")
	// ErrVotingClosed is returned when voting is attempted on a finalized dataset.
	ErrVotingClosed = errors.New("governance: voting is closed for this dataset")
	// ErrInvalidSignature is returned when vote signature verification fails.
	ErrInvalidSignature = errors.New("governance: invalid vote signature")
	// ErrQuorumNotReached is returned when not enough votes have been cast.
	ErrQuorumNotReached = errors.New("governance: quorum not reached")
	// ErrDatasetAlreadyReviewed is returned when trying to finalize an already-reviewed dataset.
	ErrDatasetAlreadyReviewed = errors.New("governance: dataset has already been reviewed")
	// ErrNoCategories is returned when registering a dataset with an empty categories list.
	ErrNoCategories = errors.New("governance: dataset must have at least one ethics category")
	// ErrRegistryFull is returned when the registry has reached its maximum capacity.
	ErrRegistryFull = errors.New("governance: registry has reached maximum capacity")
)
