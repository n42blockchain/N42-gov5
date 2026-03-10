// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"sort"
	"sync"
	"time"
)

// Pacemaker manages view timeouts with exponential backoff (Jolteon-style).
//
// Timeout formula: min(effective_base * 2^consecutive_timeouts, max_timeout)
//
// Adaptive mode: when commit latencies are observed, the effective base timeout
// adapts to max(configured_base, 2 × p95_latency). This prevents premature timeouts
// on slow global networks while keeping fast timeouts locally.
type Pacemaker struct {
	mu sync.RWMutex

	baseTimeout time.Duration
	maxTimeout  time.Duration
	deadline    time.Time

	// Adaptive timeout: ring buffer of recent commit latencies.
	recentLatencies []time.Duration
	latencyCursor   uint64
}

const (
	latencyWindow      = 20  // Number of recent latency samples to keep
	maxBackoffExponent = 63  // Prevent overflow in 2^n calculation
)

// NewPacemaker creates a new pacemaker with the given base and maximum timeout durations.
func NewPacemaker(baseTimeoutMs, maxTimeoutMs uint64) *Pacemaker {
	base := time.Duration(baseTimeoutMs) * time.Millisecond
	max := time.Duration(maxTimeoutMs) * time.Millisecond
	return &Pacemaker{
		baseTimeout:     base,
		maxTimeout:      max,
		deadline:        time.Now().Add(base),
		recentLatencies: make([]time.Duration, 0, latencyWindow),
	}
}

// ObserveCommitLatency records an observed commit latency for adaptive timeout calculation.
func (p *Pacemaker) ObserveCommitLatency(latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.recentLatencies) < latencyWindow {
		p.recentLatencies = append(p.recentLatencies, latency)
	} else {
		p.recentLatencies[p.latencyCursor%latencyWindow] = latency
	}
	p.latencyCursor++
}

// effectiveBaseTimeout returns the adaptive base timeout considering observed latencies.
// Caller must hold at least p.mu.RLock.
func (p *Pacemaker) effectiveBaseTimeout() time.Duration {
	if len(p.recentLatencies) < 5 {
		return p.baseTimeout
	}

	// Compute p95 of recent latencies. Copy the slice to avoid sorting the original.
	sorted := make([]int64, len(p.recentLatencies))
	for i, d := range p.recentLatencies {
		sorted[i] = d.Milliseconds()
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p95Idx := len(sorted) * 95 / 100
	if p95Idx >= len(sorted) {
		p95Idx = len(sorted) - 1
	}
	p95Ms := sorted[p95Idx]

	// Effective base = max(configured, 2 × p95), capped at max_timeout.
	adaptiveMs := p95Ms * 2
	effectiveMs := p.baseTimeout.Milliseconds()
	if adaptiveMs > effectiveMs {
		effectiveMs = adaptiveMs
	}
	maxMs := p.maxTimeout.Milliseconds()
	if effectiveMs > maxMs {
		effectiveMs = maxMs
	}
	return time.Duration(effectiveMs) * time.Millisecond
}

// TimeoutDuration returns the timeout duration for the given number of consecutive timeouts.
// Uses exponential backoff: effective_base * 2^consecutive_timeouts, capped at max_timeout.
func (p *Pacemaker) TimeoutDuration(consecutiveTimeouts uint32) time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()

	base := p.effectiveBaseTimeout()
	exp := consecutiveTimeouts
	if exp > maxBackoffExponent {
		exp = maxBackoffExponent
	}
	multiplier := uint64(1) << exp
	timeoutMs := uint64(base.Milliseconds()) * multiplier
	maxMs := uint64(p.maxTimeout.Milliseconds())
	if timeoutMs > maxMs || timeoutMs < uint64(base.Milliseconds()) { // overflow check
		timeoutMs = maxMs
	}
	return time.Duration(timeoutMs) * time.Millisecond
}

// ResetForView resets the timer for a new view with the appropriate backoff duration.
func (p *Pacemaker) ResetForView(view ViewNumber, consecutiveTimeouts uint32) {
	duration := p.TimeoutDuration(consecutiveTimeouts)
	p.mu.Lock()
	p.deadline = time.Now().Add(duration)
	p.mu.Unlock()
}

// IsTimedOut returns true if the current view has timed out.
func (p *Pacemaker) IsTimedOut() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return time.Now().After(p.deadline) || time.Now().Equal(p.deadline)
}

// Remaining returns the remaining time until timeout.
func (p *Pacemaker) Remaining() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	remaining := time.Until(p.deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// Deadline returns the current deadline.
func (p *Pacemaker) Deadline() time.Time {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.deadline
}

// ExtendDeadline extends the current deadline by the given duration.
func (p *Pacemaker) ExtendDeadline(extra time.Duration) {
	p.mu.Lock()
	p.deadline = p.deadline.Add(extra)
	p.mu.Unlock()
}

// TimeoutChan returns a channel that fires when the current view times out.
func (p *Pacemaker) TimeoutChan() <-chan time.Time {
	p.mu.RLock()
	deadline := p.deadline
	p.mu.RUnlock()
	return time.After(time.Until(deadline))
}
