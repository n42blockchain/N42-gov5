// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reputation system for AI agents. Computes an overall score from
// four weighted sub-signals: completion rate (40), non-dispute rate
// (30), response time (20) and staked amount (10). New agents start
// at defaultReputation (50) and scores decay over time so inactive
// agents lose standing.

package coord

import (
	"math"
	"sync"
	"time"
)

// Default weights for the overall reputation score computation.
const (
	weightCompletion   = 40.0
	weightNonDispute   = 30.0
	weightResponseTime = 20.0
	weightStake        = 10.0

	// defaultReputation is the starting score for unknown agents.
	defaultReputation = 50.0

	// maxResponseTime is the response time threshold (in seconds) at which
	// the response time score is zero. Faster responses yield higher scores.
	maxResponseTime = 3600.0 // 1 hour

	// defaultStakeQuality is the initial stake quality score assigned to new
	// agents. This value (0.0-1.0) contributes to the overall reputation via
	// weightStake (10%). Override by calling SetStakeQuality after creation.
	defaultStakeQuality = 0.5
)

// ReputationScore holds the detailed breakdown of an agent's reputation.
type ReputationScore struct {
	DID            string    `json:"did"`
	CompletionRate float64   `json:"completionRate"` // 0.0 - 1.0
	ResponseTime   float64   `json:"responseTime"`   // average in seconds
	DisputeRate    float64   `json:"disputeRate"`     // 0.0 - 1.0
	StakeQuality   float64   `json:"stakeQuality"`   // 0.0 - 1.0
	OverallScore   float64   `json:"overallScore"`   // 0 - 100
	TotalTasks     uint64    `json:"totalTasks"`
	Completions    uint64    `json:"completions"`
	Failures       uint64    `json:"failures"`
	Disputes       uint64    `json:"disputes"`
	LastUpdated    time.Time `json:"lastUpdated"`
}

// ReputationSystem tracks and computes reputation scores for agents.
// All methods are safe for concurrent use.
type ReputationSystem struct {
	mu    sync.RWMutex
	scores map[string]*ReputationScore
	decay  float64 // decay factor per epoch (e.g., 0.95)
}

// NewReputationSystem creates a reputation system with the given decay factor.
// The decay factor should be in (0, 1]; values outside this range are clamped.
func NewReputationSystem(decay float64) *ReputationSystem {
	if decay <= 0 {
		decay = 0.01
	}
	if decay > 1.0 {
		decay = 1.0
	}
	return &ReputationSystem{
		scores: make(map[string]*ReputationScore),
		decay:  decay,
	}
}

// Score returns the overall reputation score for an agent (0-100).
// Unknown agents receive the default score of 50.
func (rs *ReputationSystem) Score(did string) float64 {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if s, exists := rs.scores[did]; exists {
		return s.OverallScore
	}
	return defaultReputation
}

// RecordCompletion records a successful task completion with the given
// response time, and recomputes the agent's overall score.
func (rs *ReputationSystem) RecordCompletion(did string, responseTime time.Duration) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	s := rs.getOrCreate(did)
	s.Completions++
	s.TotalTasks++

	// Update completion rate.
	s.CompletionRate = float64(s.Completions) / float64(s.TotalTasks)

	// Update running average response time.
	rtSec := responseTime.Seconds()
	if s.Completions == 1 {
		s.ResponseTime = rtSec
	} else {
		// Incremental average.
		s.ResponseTime = s.ResponseTime + (rtSec-s.ResponseTime)/float64(s.Completions)
	}

	// Recompute dispute rate.
	s.DisputeRate = float64(s.Disputes) / float64(s.TotalTasks)

	s.LastUpdated = time.Now()
	computeOverall(s)
}

// RecordFailure records a task failure and recomputes the agent's score.
func (rs *ReputationSystem) RecordFailure(did string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	s := rs.getOrCreate(did)
	s.Failures++
	s.TotalTasks++

	s.CompletionRate = float64(s.Completions) / float64(s.TotalTasks)
	s.DisputeRate = float64(s.Disputes) / float64(s.TotalTasks)

	s.LastUpdated = time.Now()
	computeOverall(s)
}

// RecordDispute records a dispute against an agent and recomputes the score.
func (rs *ReputationSystem) RecordDispute(did string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	s := rs.getOrCreate(did)
	s.Disputes++

	// Disputes count against the total for dispute rate.
	if s.TotalTasks > 0 {
		s.DisputeRate = float64(s.Disputes) / float64(s.TotalTasks)
	}

	s.LastUpdated = time.Now()
	computeOverall(s)
}

// GetDetails returns a copy of the full reputation score for an agent.
// Returns nil if the agent has no recorded history.
func (rs *ReputationSystem) GetDetails(did string) *ReputationScore {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	s, exists := rs.scores[did]
	if !exists {
		return nil
	}
	cp := *s
	return &cp
}

// ApplyDecay applies the decay factor to all scores, aging out old
// performance. This should be called once per epoch.
func (rs *ReputationSystem) ApplyDecay() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	for _, s := range rs.scores {
		// Decay moves the overall score toward the default.
		s.OverallScore = defaultReputation + (s.OverallScore-defaultReputation)*rs.decay
		s.LastUpdated = time.Now()
	}
}

// SetStakeQuality sets the stake quality score (0.0-1.0) for an agent and
// recomputes the overall reputation score. This allows overriding the
// defaultStakeQuality (0.5) with a value derived from actual on-chain stake.
func (rs *ReputationSystem) SetStakeQuality(did string, quality float64) {
	if quality < 0 {
		quality = 0
	}
	if quality > 1.0 {
		quality = 1.0
	}

	rs.mu.Lock()
	defer rs.mu.Unlock()

	s := rs.getOrCreate(did)
	s.StakeQuality = quality
	s.LastUpdated = time.Now()
	computeOverall(s)
}

// getOrCreate returns the score entry for a DID, creating one if needed.
// Must be called with the write lock held.
func (rs *ReputationSystem) getOrCreate(did string) *ReputationScore {
	s, exists := rs.scores[did]
	if !exists {
		s = &ReputationScore{
			DID:          did,
			OverallScore: defaultReputation,
			StakeQuality: defaultStakeQuality,
			LastUpdated:  time.Now(),
		}
		rs.scores[did] = s
	}
	return s
}

// computeOverall calculates the weighted overall score from component scores.
// Formula: completionRate*40 + (1-disputeRate)*30 + responseTimeScore*20 + stakeQuality*10
func computeOverall(s *ReputationScore) {
	// Response time score: inversely proportional to average response time.
	// Score is 1.0 for instant responses, 0.0 for responses >= maxResponseTime.
	var rtScore float64
	if s.ResponseTime <= 0 {
		rtScore = 1.0
	} else {
		rtScore = math.Max(0, 1.0-s.ResponseTime/maxResponseTime)
	}

	s.OverallScore = s.CompletionRate*weightCompletion +
		(1.0-s.DisputeRate)*weightNonDispute +
		rtScore*weightResponseTime +
		s.StakeQuality*weightStake

	// Clamp to [0, 100].
	if s.OverallScore > 100 {
		s.OverallScore = 100
	}
	if s.OverallScore < 0 {
		s.OverallScore = 0
	}
}
