// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coord

import (
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// NegotiationStatus represents the lifecycle state of a task negotiation.
type NegotiationStatus uint8

const (
	NegotiationOpen      NegotiationStatus = iota
	NegotiationBidding
	NegotiationAccepted
	NegotiationCompleted
	NegotiationDisputed
	NegotiationCancelled
)

// String returns a human-readable name for the negotiation status.
func (s NegotiationStatus) String() string {
	switch s {
	case NegotiationOpen:
		return "open"
	case NegotiationBidding:
		return "bidding"
	case NegotiationAccepted:
		return "accepted"
	case NegotiationCompleted:
		return "completed"
	case NegotiationDisputed:
		return "disputed"
	case NegotiationCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// Errors returned by the negotiation subsystem.
var (
	ErrNegotiationNotFound  = errors.New("negotiation: not found")
	ErrNegotiationClosed    = errors.New("negotiation: not open for bids")
	ErrBidExceedsBudget     = errors.New("negotiation: bid exceeds max budget")
	ErrBidNotFound          = errors.New("negotiation: bid not found")
	ErrNotRequester         = errors.New("negotiation: caller is not the requester")
	ErrNotProvider          = errors.New("negotiation: caller is not the accepted provider")
	ErrInvalidTransition    = errors.New("negotiation: invalid status transition")
	ErrEmptyRequester       = errors.New("negotiation: requester DID must not be empty")
	ErrEmptyCapability      = errors.New("negotiation: capability must not be empty")
	ErrNilBudget            = errors.New("negotiation: max budget must not be nil")
	ErrEmptyProviderDID     = errors.New("negotiation: provider DID must not be empty")
	ErrNilPrice             = errors.New("negotiation: price must not be nil")
	ErrDeadlineInPast       = errors.New("negotiation: deadline must be zero or in the future")
)

// TaskSpec describes a task to be performed by an agent.
type TaskSpec struct {
	Description string       `json:"description"`
	Capability  string       `json:"capability"`
	InputCAS    types.Hash   `json:"inputCAS"`
	MaxBudget   *uint256.Int `json:"maxBudget"`
	Deadline    time.Time    `json:"deadline"`
}

// Bid represents a provider's offer to execute a task.
type Bid struct {
	ID          types.Hash    `json:"id"`
	ProviderDID string        `json:"providerDID"`
	Price       *uint256.Int  `json:"price"`
	ETA         time.Duration `json:"eta"`
	SubmittedAt time.Time     `json:"submittedAt"`
}

// Negotiation tracks the full lifecycle of a task from request to completion.
type Negotiation struct {
	ID           types.Hash        `json:"id"`
	RequesterDID string            `json:"requesterDID"`
	Spec         TaskSpec          `json:"spec"`
	Status       NegotiationStatus `json:"status"`
	Bids         []*Bid            `json:"bids"`
	AcceptedBid  *types.Hash       `json:"acceptedBid,omitempty"`
	ResultCAS    *types.Hash       `json:"resultCAS,omitempty"`
	Escrow       *uint256.Int      `json:"escrow,omitempty"`
	DisputeReason string           `json:"disputeReason,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// TaskNegotiation manages task negotiations between requesters and providers.
// All methods are safe for concurrent use.
type TaskNegotiation struct {
	mu           sync.RWMutex
	negotiations map[types.Hash]*Negotiation
	bidIndex     map[types.Hash]types.Hash // bidID -> negotiationID
	timeout      time.Duration
	nonce        atomic.Uint64
}

// NewTaskNegotiation creates a task negotiation manager with the given
// default timeout for negotiations.
func NewTaskNegotiation(timeout time.Duration) *TaskNegotiation {
	if timeout <= 0 {
		timeout = 1 * time.Hour
	}
	return &TaskNegotiation{
		negotiations: make(map[types.Hash]*Negotiation),
		bidIndex:     make(map[types.Hash]types.Hash),
		timeout:      timeout,
	}
}

// cleanExpired cancels negotiations that have exceeded the timeout duration
// while still in Open or Bidding status, and removes negotiations that have
// been in a terminal state (Completed, Cancelled, Disputed) for longer than
// the timeout to prevent unbounded map growth. Must be called with the write
// lock held.
func (tn *TaskNegotiation) cleanExpired() {
	now := time.Now()
	for id, neg := range tn.negotiations {
		if neg.Status == NegotiationOpen || neg.Status == NegotiationBidding {
			if now.After(neg.CreatedAt.Add(tn.timeout)) {
				neg.Status = NegotiationCancelled
				neg.UpdatedAt = now
			}
			continue
		}
		// Remove terminal negotiations older than timeout to bound map growth.
		if neg.Status == NegotiationCompleted || neg.Status == NegotiationCancelled || neg.Status == NegotiationDisputed {
			if now.After(neg.UpdatedAt.Add(tn.timeout)) {
				for _, b := range neg.Bids {
					delete(tn.bidIndex, b.ID)
				}
				delete(tn.negotiations, id)
			}
		}
	}
}

// RequestTask creates a new task negotiation and returns its unique ID.
// The ID is derived from Keccak256(requesterDID + nonce).
func (tn *TaskNegotiation) RequestTask(requesterDID string, spec TaskSpec) (types.Hash, error) {
	if requesterDID == "" {
		return types.Hash{}, ErrEmptyRequester
	}
	if spec.Capability == "" {
		return types.Hash{}, ErrEmptyCapability
	}
	if spec.MaxBudget == nil || spec.MaxBudget.IsZero() {
		return types.Hash{}, ErrNilBudget
	}
	if !spec.Deadline.IsZero() && spec.Deadline.Before(time.Now()) {
		return types.Hash{}, ErrDeadlineInPast
	}

	nonce := tn.nonce.Add(1)
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonce)
	id := crypto.Keccak256Hash([]byte(requesterDID), nonceBytes)

	now := time.Now()
	neg := &Negotiation{
		ID:           id,
		RequesterDID: requesterDID,
		Spec:         spec,
		Status:       NegotiationOpen,
		Bids:         make([]*Bid, 0),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	// Deep copy the budget to prevent external mutation.
	neg.Spec.MaxBudget = new(uint256.Int).Set(spec.MaxBudget)

	tn.mu.Lock()
	defer tn.mu.Unlock()

	tn.cleanExpired()
	tn.negotiations[id] = neg

	return id, nil
}

// BidOnTask adds a bid from a provider to an open or bidding negotiation.
// The bid price must not exceed the task's MaxBudget.
func (tn *TaskNegotiation) BidOnTask(providerDID string, negotiationID types.Hash, price *uint256.Int, eta time.Duration) (types.Hash, error) {
	if providerDID == "" {
		return types.Hash{}, ErrEmptyProviderDID
	}
	if price == nil {
		return types.Hash{}, ErrNilPrice
	}

	tn.mu.Lock()
	defer tn.mu.Unlock()

	neg, exists := tn.negotiations[negotiationID]
	if !exists {
		return types.Hash{}, ErrNegotiationNotFound
	}

	if neg.Status != NegotiationOpen && neg.Status != NegotiationBidding {
		return types.Hash{}, ErrNegotiationClosed
	}

	if price.Cmp(neg.Spec.MaxBudget) > 0 {
		return types.Hash{}, ErrBidExceedsBudget
	}

	// Compute a unique bid ID from negotiation ID + provider DID + bid count.
	bidCountBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bidCountBytes, uint64(len(neg.Bids)))
	bidID := crypto.Keccak256Hash(negotiationID[:], []byte(providerDID), bidCountBytes)

	bid := &Bid{
		ID:          bidID,
		ProviderDID: providerDID,
		Price:       new(uint256.Int).Set(price),
		ETA:         eta,
		SubmittedAt: time.Now(),
	}

	neg.Bids = append(neg.Bids, bid)
	if neg.Status == NegotiationOpen {
		neg.Status = NegotiationBidding
	}
	neg.UpdatedAt = time.Now()

	tn.bidIndex[bidID] = negotiationID

	return bidID, nil
}

// AcceptBid accepts a bid, transitioning the negotiation to Accepted and
// setting the escrow amount to the bid price. Only the requester can accept.
func (tn *TaskNegotiation) AcceptBid(requesterDID string, bidID types.Hash) error {
	tn.mu.Lock()
	defer tn.mu.Unlock()

	negID, exists := tn.bidIndex[bidID]
	if !exists {
		return ErrBidNotFound
	}

	neg, exists := tn.negotiations[negID]
	if !exists {
		return ErrNegotiationNotFound
	}

	if neg.RequesterDID != requesterDID {
		return ErrNotRequester
	}

	if neg.Status != NegotiationBidding {
		return ErrInvalidTransition
	}

	// Find the accepted bid and set escrow.
	var acceptedBid *Bid
	for _, b := range neg.Bids {
		if b.ID == bidID {
			acceptedBid = b
			break
		}
	}
	if acceptedBid == nil {
		return ErrBidNotFound
	}

	bidIDCopy := bidID
	neg.AcceptedBid = &bidIDCopy
	neg.Escrow = new(uint256.Int).Set(acceptedBid.Price)
	neg.Status = NegotiationAccepted
	neg.UpdatedAt = time.Now()

	return nil
}

// CompleteTask marks a negotiation as completed with the given result CAS hash.
// Only the accepted provider can complete the task.
func (tn *TaskNegotiation) CompleteTask(providerDID string, negotiationID types.Hash, resultCAS types.Hash) error {
	tn.mu.Lock()
	defer tn.mu.Unlock()

	neg, exists := tn.negotiations[negotiationID]
	if !exists {
		return ErrNegotiationNotFound
	}

	if neg.Status != NegotiationAccepted {
		return ErrInvalidTransition
	}

	// Verify the caller is the accepted provider.
	if neg.AcceptedBid == nil {
		return ErrInvalidTransition
	}
	var accepted *Bid
	for _, b := range neg.Bids {
		if b.ID == *neg.AcceptedBid {
			accepted = b
			break
		}
	}
	if accepted == nil || accepted.ProviderDID != providerDID {
		return ErrNotProvider
	}

	result := resultCAS
	neg.ResultCAS = &result
	neg.Status = NegotiationCompleted
	neg.UpdatedAt = time.Now()

	return nil
}

// DisputeTask marks a negotiation as disputed. Only the requester can dispute,
// and only accepted or completed negotiations can be disputed.
func (tn *TaskNegotiation) DisputeTask(requesterDID string, negotiationID types.Hash, reason string) error {
	tn.mu.Lock()
	defer tn.mu.Unlock()

	neg, exists := tn.negotiations[negotiationID]
	if !exists {
		return ErrNegotiationNotFound
	}

	if neg.RequesterDID != requesterDID {
		return ErrNotRequester
	}

	if neg.Status != NegotiationAccepted && neg.Status != NegotiationCompleted {
		return ErrInvalidTransition
	}

	neg.Status = NegotiationDisputed
	neg.DisputeReason = reason
	neg.UpdatedAt = time.Now()

	return nil
}

// CancelTask cancels a negotiation. Only the requester can cancel,
// and only open or bidding negotiations can be cancelled.
func (tn *TaskNegotiation) CancelTask(requesterDID string, negotiationID types.Hash) error {
	tn.mu.Lock()
	defer tn.mu.Unlock()

	neg, exists := tn.negotiations[negotiationID]
	if !exists {
		return ErrNegotiationNotFound
	}

	if neg.RequesterDID != requesterDID {
		return ErrNotRequester
	}

	if neg.Status != NegotiationOpen && neg.Status != NegotiationBidding {
		return ErrInvalidTransition
	}

	neg.Status = NegotiationCancelled
	neg.UpdatedAt = time.Now()

	return nil
}

// GetNegotiation returns a copy of the negotiation with the given ID.
func (tn *TaskNegotiation) GetNegotiation(id types.Hash) (*Negotiation, error) {
	tn.mu.RLock()
	defer tn.mu.RUnlock()

	neg, exists := tn.negotiations[id]
	if !exists {
		return nil, ErrNegotiationNotFound
	}

	return tn.cloneNegotiation(neg), nil
}

// PendingNegotiations returns all negotiations that are open or bidding.
// Expired negotiations are lazily cleaned before filtering.
func (tn *TaskNegotiation) PendingNegotiations() []*Negotiation {
	tn.mu.Lock()
	defer tn.mu.Unlock()

	tn.cleanExpired()

	var result []*Negotiation
	for _, neg := range tn.negotiations {
		if neg.Status == NegotiationOpen || neg.Status == NegotiationBidding {
			result = append(result, tn.cloneNegotiation(neg))
		}
	}
	return result
}

// cloneNegotiation returns a deep copy of a negotiation. Must be called
// with at least a read lock held.
func (tn *TaskNegotiation) cloneNegotiation(neg *Negotiation) *Negotiation {
	cp := *neg
	cp.Bids = make([]*Bid, len(neg.Bids))
	for i, b := range neg.Bids {
		bc := *b
		if b.Price != nil {
			bc.Price = new(uint256.Int).Set(b.Price)
		}
		cp.Bids[i] = &bc
	}
	if neg.Spec.MaxBudget != nil {
		cp.Spec.MaxBudget = new(uint256.Int).Set(neg.Spec.MaxBudget)
	}
	if neg.Escrow != nil {
		cp.Escrow = new(uint256.Int).Set(neg.Escrow)
	}
	if neg.AcceptedBid != nil {
		ab := *neg.AcceptedBid
		cp.AcceptedBid = &ab
	}
	if neg.ResultCAS != nil {
		rc := *neg.ResultCAS
		cp.ResultCAS = &rc
	}
	return &cp
}
