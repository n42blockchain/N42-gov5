// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package wallet

import (
	"fmt"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// SpendingPolicy defines constraints on agent transaction behavior.
type SpendingPolicy interface {
	// Evaluate checks whether the given operation is allowed.
	// It returns allow=true if the policy permits the operation,
	// or allow=false with a human-readable reason.
	Evaluate(op *Operation) (allow bool, reason string)

	// Name returns the policy identifier.
	Name() string
}

// Operation describes a transaction operation for policy evaluation.
type Operation struct {
	From      types.Address
	To        types.Address
	Value     *uint256.Int
	Gas       uint64
	Data      []byte
	Timestamp time.Time
}

// ---------- RatePolicy ----------

// RatePolicy limits the number of transactions within a sliding time window.
type RatePolicy struct {
	mu         sync.Mutex
	maxCount   int
	window     time.Duration
	timestamps []time.Time
}

// NewRatePolicy creates a rate policy allowing at most maxCount operations
// within the given time window.
func NewRatePolicy(maxCount int, window time.Duration) *RatePolicy {
	return &RatePolicy{
		maxCount:   maxCount,
		window:     window,
		timestamps: make([]time.Time, 0, maxCount),
	}
}

func (p *RatePolicy) Name() string { return "rate" }

// Evaluate checks whether the operation falls within the rate limit.
func (p *RatePolicy) Evaluate(op *Operation) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := op.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// Prune timestamps outside the current window.
	cutoff := now.Add(-p.window)
	valid := p.timestamps[:0]
	for _, t := range p.timestamps {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	p.timestamps = valid

	if len(p.timestamps) >= p.maxCount {
		return false, fmt.Sprintf("rate limit exceeded: %d/%d in %v", len(p.timestamps), p.maxCount, p.window)
	}

	// Record this operation's timestamp.
	p.timestamps = append(p.timestamps, now)
	return true, ""
}

// ---------- CapPolicy ----------

// CapPolicy enforces per-transaction and daily spending caps.
type CapPolicy struct {
	mu        sync.Mutex
	txCap     *uint256.Int
	dailyCap  *uint256.Int
	dailyUsed map[string]*uint256.Int // date string → cumulative spend
}

// NewCapPolicy creates a cap policy with the given per-transaction and daily limits.
func NewCapPolicy(txCap, dailyCap *uint256.Int) *CapPolicy {
	return &CapPolicy{
		txCap:     new(uint256.Int).Set(txCap),
		dailyCap:  new(uint256.Int).Set(dailyCap),
		dailyUsed: make(map[string]*uint256.Int),
	}
}

func (p *CapPolicy) Name() string { return "cap" }

// Evaluate checks per-transaction and daily spending limits.
func (p *CapPolicy) Evaluate(op *Operation) (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	val := op.Value
	if val == nil {
		val = uint256.NewInt(0)
	}

	// Per-transaction cap.
	if val.Cmp(p.txCap) > 0 {
		return false, fmt.Sprintf("transaction value %s exceeds per-tx cap %s", val.String(), p.txCap.String())
	}

	// Daily cap using the operation timestamp.
	ts := op.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	dateKey := ts.UTC().Format("2006-01-02")

	// Prune stale date entries to prevent unbounded map growth.
	if len(p.dailyUsed) > 2 {
		for k := range p.dailyUsed {
			if k != dateKey {
				delete(p.dailyUsed, k)
			}
		}
	}

	used, ok := p.dailyUsed[dateKey]
	if !ok {
		used = uint256.NewInt(0)
		p.dailyUsed[dateKey] = used
	}

	projected := new(uint256.Int).Add(used, val)
	if projected.Cmp(p.dailyCap) > 0 {
		return false, fmt.Sprintf("daily spend %s + %s exceeds daily cap %s", used.String(), val.String(), p.dailyCap.String())
	}

	// Record the spend.
	used.Set(projected)
	return true, ""
}

// ---------- AllowlistPolicy ----------

// AllowlistPolicy restricts transactions to a set of whitelisted addresses.
type AllowlistPolicy struct {
	allowed map[types.Address]struct{}
}

// NewAllowlistPolicy creates an allowlist policy with the given addresses.
func NewAllowlistPolicy(addresses []types.Address) *AllowlistPolicy {
	m := make(map[types.Address]struct{}, len(addresses))
	for _, addr := range addresses {
		m[addr] = struct{}{}
	}
	return &AllowlistPolicy{allowed: m}
}

func (p *AllowlistPolicy) Name() string { return "allowlist" }

// Evaluate checks whether the target address is in the allowlist.
func (p *AllowlistPolicy) Evaluate(op *Operation) (bool, string) {
	if _, ok := p.allowed[op.To]; !ok {
		return false, fmt.Sprintf("address %s not in allowlist", op.To.Hex())
	}
	return true, ""
}

// ---------- CompositePolicy ----------

// CompositePolicy combines multiple policies with AND or OR logic.
type CompositePolicy struct {
	Mode     string // "and" or "or"
	Policies []SpendingPolicy
}

// NewCompositePolicy creates a composite policy.
// Valid modes are "and" and "or". In "and" mode all policies must allow
// the operation. In "or" mode at least one policy must allow it.
// Unrecognized modes silently default to "and" for backward compatibility.
func NewCompositePolicy(mode string, policies []SpendingPolicy) *CompositePolicy {
	if mode != "and" && mode != "or" {
		mode = "and"
	}
	return &CompositePolicy{
		Mode:     mode,
		Policies: policies,
	}
}

func (p *CompositePolicy) Name() string { return "composite(" + p.Mode + ")" }

// Evaluate checks all sub-policies according to the composite mode.
func (p *CompositePolicy) Evaluate(op *Operation) (bool, string) {
	if len(p.Policies) == 0 {
		return true, ""
	}

	switch p.Mode {
	case "or":
		var lastReason string
		for _, pol := range p.Policies {
			allow, reason := pol.Evaluate(op)
			if allow {
				return true, ""
			}
			lastReason = reason
		}
		return false, fmt.Sprintf("no policy allowed operation: %s", lastReason)

	default: // "and" (default behavior)
		for _, pol := range p.Policies {
			allow, reason := pol.Evaluate(op)
			if !allow {
				return false, reason
			}
		}
		return true, ""
	}
}

// EvaluateAll evaluates all policies against the given operation. In AND
// semantics, all policies must allow the operation. Returns the first failure
// reason if any policy rejects.
func EvaluateAll(policies []SpendingPolicy, op *Operation) (bool, string) {
	for _, pol := range policies {
		allow, reason := pol.Evaluate(op)
		if !allow {
			return false, fmt.Sprintf("policy %q denied: %s", pol.Name(), reason)
		}
	}
	return true, ""
}
