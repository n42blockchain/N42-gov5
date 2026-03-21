// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package coprocessor

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// ChallengeStatus represents the lifecycle state of a challenge.
type ChallengeStatus uint8

const (
	ChallengePending  ChallengeStatus = 0 // Awaiting resolution
	ChallengeUpheld   ChallengeStatus = 1 // Challenge successful, original result invalid
	ChallengeRejected ChallengeStatus = 2 // Challenge failed, original result stands
	ChallengeExpired  ChallengeStatus = 3 // Challenge window expired without resolution
)

func (s ChallengeStatus) String() string {
	switch s {
	case ChallengePending:
		return "pending"
	case ChallengeUpheld:
		return "upheld"
	case ChallengeRejected:
		return "rejected"
	case ChallengeExpired:
		return "expired"
	default:
		return "unknown"
	}
}

// Challenge represents a dispute against an optimistic-verified task.
type Challenge struct {
	ID         types.Hash      `json:"id"`
	TaskID     types.Hash      `json:"taskId"`
	Challenger types.Address   `json:"challenger"`
	Bond       uint64          `json:"bond"`
	Status     ChallengeStatus `json:"status"`
	CreatedAt  time.Time       `json:"createdAt"`
	ResolvedAt time.Time       `json:"resolvedAt,omitempty"`
	Reason     string          `json:"reason"`
}

// ChallengeManager manages the challenge lifecycle for optimistic verification.
// It tracks active challenges and determines when optimistic tasks can be finalized.
type ChallengeManager struct {
	mu         sync.RWMutex
	challenges map[types.Hash]*Challenge   // challengeID → Challenge
	byTask     map[types.Hash][]types.Hash // taskID → challengeIDs
	windowSec  int
}

// NewChallengeManager creates a challenge manager with the given challenge window.
func NewChallengeManager(windowSec int) *ChallengeManager {
	return &ChallengeManager{
		challenges: make(map[types.Hash]*Challenge),
		byTask:     make(map[types.Hash][]types.Hash),
		windowSec:  windowSec,
	}
}

// Submit files a new challenge against a task.
func (cm *ChallengeManager) Submit(taskID types.Hash, challenger types.Address, bond uint64, reason string) (types.Hash, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Derive deterministic challenge ID from taskID + challenger
	data := make([]byte, 0, 52)
	data = append(data, taskID[:]...)
	data = append(data, challenger[:]...)
	challengeID := crypto.Keccak256Hash(data)

	if _, exists := cm.challenges[challengeID]; exists {
		return types.Hash{}, ErrDuplicateChallenge
	}

	cm.challenges[challengeID] = &Challenge{
		ID:         challengeID,
		TaskID:     taskID,
		Challenger: challenger,
		Bond:       bond,
		Status:     ChallengePending,
		CreatedAt:  time.Now(),
		Reason:     reason,
	}
	cm.byTask[taskID] = append(cm.byTask[taskID], challengeID)
	return challengeID, nil
}

// Resolve marks a challenge as upheld or rejected.
// If upheld, the original optimistic result is invalidated and the challenger is rewarded.
// If rejected, the challenger's bond is forfeited.
func (cm *ChallengeManager) Resolve(challengeID types.Hash, upheld bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	c, ok := cm.challenges[challengeID]
	if !ok {
		return ErrChallengeNotFound
	}
	if c.Status != ChallengePending {
		return ErrChallengeResolved
	}

	if upheld {
		c.Status = ChallengeUpheld
	} else {
		c.Status = ChallengeRejected
	}
	c.ResolvedAt = time.Now()
	return nil
}

// Get returns a challenge by ID.
func (cm *ChallengeManager) Get(challengeID types.Hash) (*Challenge, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	c, ok := cm.challenges[challengeID]
	return c, ok
}

// GetByTask returns all challenges for a given task.
func (cm *ChallengeManager) GetByTask(taskID types.Hash) []*Challenge {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	ids := cm.byTask[taskID]
	result := make([]*Challenge, 0, len(ids))
	for _, id := range ids {
		if c, ok := cm.challenges[id]; ok {
			result = append(result, c)
		}
	}
	return result
}

// HasPendingChallenge returns true if any challenge for the task is still pending.
func (cm *ChallengeManager) HasPendingChallenge(taskID types.Hash) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, id := range cm.byTask[taskID] {
		if c, ok := cm.challenges[id]; ok && c.Status == ChallengePending {
			return true
		}
	}
	return false
}

// ExpireWindow checks for optimistic-verified tasks whose challenge window has
// expired with no pending challenges. Returns task IDs ready for finalization.
func (cm *ChallengeManager) ExpireWindow(tasks []*Task) []types.Hash {
	now := time.Now()
	var ready []types.Hash
	for _, t := range tasks {
		if t.Status == TaskOptimisticVerified && now.After(t.ChallengeDeadline) {
			if !cm.HasPendingChallenge(t.ID) {
				ready = append(ready, t.ID)
			}
		}
	}
	return ready
}

// Prune removes resolved challenges older than maxAge.
// Returns the number of pruned challenges.
func (cm *ChallengeManager) Prune(maxAge time.Duration) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	pruned := 0
	for id, c := range cm.challenges {
		if c.Status != ChallengePending && c.ResolvedAt.Before(cutoff) {
			delete(cm.challenges, id)
			pruned++
		}
	}
	return pruned
}

// Count returns the total number of tracked challenges.
func (cm *ChallengeManager) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.challenges)
}
