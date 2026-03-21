// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coprocessor

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// SlashCondition defines the reason for slashing a provider.
type SlashCondition uint8

const (
	SlashTimeout        SlashCondition = 0 // Task expired without proof
	SlashInvalidProof   SlashCondition = 1 // Submitted proof failed verification
	SlashChallengeUpheld SlashCondition = 2 // Challenge against provider's result was upheld
)

func (c SlashCondition) String() string {
	switch c {
	case SlashTimeout:
		return "timeout"
	case SlashInvalidProof:
		return "invalid_proof"
	case SlashChallengeUpheld:
		return "challenge_upheld"
	default:
		return "unknown"
	}
}

// SlashEvent records a slashing action against a provider.
type SlashEvent struct {
	Provider  types.Address  `json:"provider"`
	Amount    uint64         `json:"amount"`
	Condition SlashCondition `json:"condition"`
	TaskID    types.Hash     `json:"taskId"`
	Timestamp time.Time      `json:"timestamp"`
}

// RewardEvent records a reward distribution to a provider.
type RewardEvent struct {
	Provider  types.Address `json:"provider"`
	Amount    uint64        `json:"amount"`
	TaskID    types.Hash    `json:"taskId"`
	Timestamp time.Time     `json:"timestamp"`
}

// SlashManager enforces economic penalties for misbehavior and distributes
// rewards for successful task completion. Implements the Verify-or-Slash model.
type SlashManager struct {
	mu             sync.RWMutex
	providers      *ProviderRegistry
	slashEvents    []SlashEvent
	rewardEvents   []RewardEvent
	slashPercent   int // 0-100, percentage of stake to slash
}

// NewSlashManager creates a slash manager with the given provider registry.
func NewSlashManager(providers *ProviderRegistry, slashPercent int) *SlashManager {
	if slashPercent < 0 {
		slashPercent = 0
	}
	if slashPercent > 100 {
		slashPercent = 100
	}
	return &SlashManager{
		providers:    providers,
		slashPercent: slashPercent,
	}
}

// Slash penalizes a provider for misbehavior. Deducts a percentage of their
// stake and updates their reputation and status.
// Provider operations are called without holding sm.mu to avoid nested locks.
func (sm *SlashManager) Slash(providerAddr types.Address, condition SlashCondition, taskID types.Hash) {
	provider, ok := sm.providers.Get(providerAddr)
	if !ok {
		return
	}

	// Calculate slash amount from snapshot (provider.Stake is read under provider lock)
	amount := provider.Stake * uint64(sm.slashPercent) / 100
	if amount == 0 && provider.Stake > 0 {
		amount = 1 // slash at least 1 wei
	}

	// Provider operations are individually thread-safe
	sm.providers.DeductStake(providerAddr, amount)
	sm.providers.IncrementTasks(providerAddr, true)

	var repPenalty int64
	switch condition {
	case SlashTimeout:
		repPenalty = -500
	case SlashInvalidProof:
		repPenalty = -1000
	case SlashChallengeUpheld:
		repPenalty = -2000
		sm.providers.UpdateStatus(providerAddr, ProviderSlashed)
	}
	sm.providers.UpdateReputation(providerAddr, repPenalty)

	// Only hold sm.mu for event recording
	sm.mu.Lock()
	sm.slashEvents = append(sm.slashEvents, SlashEvent{
		Provider:  providerAddr,
		Amount:    amount,
		Condition: condition,
		TaskID:    taskID,
		Timestamp: time.Now(),
	})
	sm.mu.Unlock()

	log.Warn("Provider slashed",
		"provider", providerAddr.Hex()[:10],
		"amount", amount,
		"condition", condition.String(),
		"taskID", taskID.Hex()[:10],
	)
}

// Reward distributes a reward to a provider for successful task completion.
// Provider operations are called without holding sm.mu to avoid nested locks.
func (sm *SlashManager) Reward(providerAddr types.Address, amount uint64, taskID types.Hash) {
	if _, ok := sm.providers.Get(providerAddr); !ok {
		return
	}

	sm.providers.IncrementTasks(providerAddr, false)
	sm.providers.UpdateReputation(providerAddr, 100)

	sm.mu.Lock()
	sm.rewardEvents = append(sm.rewardEvents, RewardEvent{
		Provider:  providerAddr,
		Amount:    amount,
		TaskID:    taskID,
		Timestamp: time.Now(),
	})
	sm.mu.Unlock()
}

// SlashHistory returns all recorded slash events.
func (sm *SlashManager) SlashHistory() []SlashEvent {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]SlashEvent, len(sm.slashEvents))
	copy(result, sm.slashEvents)
	return result
}

// RewardHistory returns all recorded reward events.
func (sm *SlashManager) RewardHistory() []RewardEvent {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]RewardEvent, len(sm.rewardEvents))
	copy(result, sm.rewardEvents)
	return result
}

// TotalSlashed returns the total amount slashed across all events.
func (sm *SlashManager) TotalSlashed() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var total uint64
	for _, e := range sm.slashEvents {
		total += e.Amount
	}
	return total
}

// TotalRewarded returns the total amount rewarded across all events.
func (sm *SlashManager) TotalRewarded() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	var total uint64
	for _, e := range sm.rewardEvents {
		total += e.Amount
	}
	return total
}
