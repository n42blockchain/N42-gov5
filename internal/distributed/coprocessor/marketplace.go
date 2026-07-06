// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package coprocessor marketplace implements a reverse-auction task marketplace
// (Akash model) where task publishers set a maximum price and providers bid down.

package coprocessor

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// SelectionStrategy determines how the winning bid is chosen.
type SelectionStrategy uint8

const (
	StrategyLowestPrice      SelectionStrategy = 0
	StrategyFastestETA       SelectionStrategy = 1
	StrategyHighestReputation SelectionStrategy = 2
)

// Bid represents a provider's offer to execute a task.
type Bid struct {
	ID         types.Hash    `json:"id"`
	TaskID     types.Hash    `json:"taskId"`
	ProviderID types.Address `json:"providerId"`
	Price      uint64        `json:"price"`    // wei offered
	ETA        time.Duration `json:"eta"`      // estimated completion time
	CreatedAt  time.Time     `json:"createdAt"`
}

// Assignment records a task-to-provider assignment resulting from marketplace bidding.
type Assignment struct {
	TaskID     types.Hash    `json:"taskId"`
	ProviderID types.Address `json:"providerId"`
	BidID      types.Hash    `json:"bidId"`
	Price      uint64        `json:"price"`
	AssignedAt time.Time     `json:"assignedAt"`
}

// Marketplace manages the reverse-auction bidding process for compute tasks.
type Marketplace struct {
	mu          sync.RWMutex
	bids        map[types.Hash][]*Bid         // taskID → bids
	assignments map[types.Hash]*Assignment     // taskID → assignment
	providers   *ProviderRegistry
}

// NewMarketplace creates a marketplace backed by the given provider registry.
func NewMarketplace(providers *ProviderRegistry) *Marketplace {
	return &Marketplace{
		bids:        make(map[types.Hash][]*Bid),
		assignments: make(map[types.Hash]*Assignment),
		providers:   providers,
	}
}

// PublishTask opens a task for bidding. The task's RewardAmount is the max price.
func (m *Marketplace) PublishTask(taskID types.Hash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.bids[taskID]; !exists {
		m.bids[taskID] = make([]*Bid, 0)
	}
}

// SubmitBid places a bid for a task. The bid price must not exceed the task reward.
func (m *Marketplace) SubmitBid(taskID types.Hash, providerAddr types.Address, price uint64, eta time.Duration) (types.Hash, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, assigned := m.assignments[taskID]; assigned {
		return types.Hash{}, ErrTaskAlreadyAssigned
	}

	// Verify provider exists and is active. Snapshot read: the live pointer's
	// Status field races with concurrent UpdateStatus writers.
	provider, ok := m.providers.GetSnapshot(providerAddr)
	if !ok {
		return types.Hash{}, ErrProviderNotFound
	}
	if provider.Status != ProviderActive {
		return types.Hash{}, ErrProviderSuspended
	}

	bidID := computeBidID(taskID, providerAddr)
	bid := &Bid{
		ID:         bidID,
		TaskID:     taskID,
		ProviderID: providerAddr,
		Price:      price,
		ETA:        eta,
		CreatedAt:  time.Now(),
	}
	m.bids[taskID] = append(m.bids[taskID], bid)
	return bidID, nil
}

// SelectWinner chooses the winning bid using the given strategy.
func (m *Marketplace) SelectWinner(taskID types.Hash, strategy SelectionStrategy) (*Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, assigned := m.assignments[taskID]; assigned {
		return nil, ErrTaskAlreadyAssigned
	}

	bids := m.bids[taskID]
	if len(bids) == 0 {
		return nil, ErrNoEligibleBids
	}

	var winner *Bid
	switch strategy {
	case StrategyLowestPrice:
		winner = selectLowestPrice(bids)
	case StrategyFastestETA:
		winner = selectFastestETA(bids)
	case StrategyHighestReputation:
		winner = m.selectHighestReputation(bids)
	default:
		winner = selectLowestPrice(bids)
	}

	if winner == nil {
		return nil, ErrNoEligibleBids
	}

	assignment := &Assignment{
		TaskID:     taskID,
		ProviderID: winner.ProviderID,
		BidID:      winner.ID,
		Price:      winner.Price,
		AssignedAt: time.Now(),
	}
	m.assignments[taskID] = assignment
	return assignment, nil
}

// GetAssignment returns the assignment for a task, if any.
func (m *Marketplace) GetAssignment(taskID types.Hash) (*Assignment, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.assignments[taskID]
	return a, ok
}

// GetBids returns all bids for a task.
func (m *Marketplace) GetBids(taskID types.Hash) []*Bid {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bids := m.bids[taskID]
	result := make([]*Bid, len(bids))
	copy(result, bids)
	return result
}

// BidCount returns the number of bids for a task.
func (m *Marketplace) BidCount(taskID types.Hash) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bids[taskID])
}

func selectLowestPrice(bids []*Bid) *Bid {
	if len(bids) == 0 {
		return nil
	}
	best := bids[0]
	for _, b := range bids[1:] {
		if b.Price < best.Price {
			best = b
		}
	}
	return best
}

func selectFastestETA(bids []*Bid) *Bid {
	if len(bids) == 0 {
		return nil
	}
	best := bids[0]
	for _, b := range bids[1:] {
		if b.ETA < best.ETA {
			best = b
		}
	}
	return best
}

func (m *Marketplace) selectHighestReputation(bids []*Bid) *Bid {
	if len(bids) == 0 {
		return nil
	}
	var best *Bid
	var bestRep uint64
	for _, b := range bids {
		provider, ok := m.providers.GetSnapshot(b.ProviderID)
		if !ok {
			continue
		}
		if best == nil || provider.Reputation > bestRep {
			best = b
			bestRep = provider.Reputation
		}
	}
	return best
}

func computeBidID(taskID types.Hash, provider types.Address) types.Hash {
	data := make([]byte, 0, 52)
	data = append(data, taskID[:]...)
	data = append(data, provider[:]...)
	return crypto.Keccak256Hash(data)
}
