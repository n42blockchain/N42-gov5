// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Provider types for the distributed compute network. Each Provider
// carries an address, stake, advertised Capabilities and a 0-10000
// basis-point Reputation score. ProviderStatus cycles between Active,
// Suspended and Slashed based on task outcomes and challenges.

package coprocessor

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// ProviderStatus represents the state of a compute provider.
type ProviderStatus uint8

const (
	ProviderActive    ProviderStatus = 0
	ProviderSuspended ProviderStatus = 1
	ProviderSlashed   ProviderStatus = 2
)

func (s ProviderStatus) String() string {
	switch s {
	case ProviderActive:
		return "active"
	case ProviderSuspended:
		return "suspended"
	case ProviderSlashed:
		return "slashed"
	default:
		return "unknown"
	}
}

// Provider represents a compute provider in the network.
type Provider struct {
	Address      types.Address  `json:"address"`
	Stake        uint64         `json:"stake"`
	Capabilities []Capability   `json:"capabilities"`
	Reputation   uint64         `json:"reputation"` // 0-10000 basis points
	Status       ProviderStatus `json:"status"`
	TasksTotal   uint64         `json:"tasksTotal"`
	TasksFailed  uint64         `json:"tasksFailed"`
	TotalRewards uint64         `json:"totalRewards"`
	RegisteredAt time.Time      `json:"registeredAt"`
}

// HasCapability returns true if the provider supports the given capability.
func (p *Provider) HasCapability(cap Capability) bool {
	for _, c := range p.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ProviderRegistry manages the set of registered compute providers.
// Thread-safe for concurrent access.
type ProviderRegistry struct {
	mu           sync.RWMutex
	providers    map[types.Address]*Provider
	minStake     uint64
}

// NewProviderRegistry creates a provider registry with the given minimum stake.
func NewProviderRegistry(minStake uint64) *ProviderRegistry {
	return &ProviderRegistry{
		providers: make(map[types.Address]*Provider),
		minStake:  minStake,
	}
}

// Register adds a new compute provider.
func (pr *ProviderRegistry) Register(addr types.Address, stake uint64, capabilities []Capability) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.providers[addr]; exists {
		return ErrProviderExists
	}
	if stake < pr.minStake {
		return ErrInsufficientStake
	}

	caps := make([]Capability, len(capabilities))
	copy(caps, capabilities)

	pr.providers[addr] = &Provider{
		Address:      addr,
		Stake:        stake,
		Capabilities: caps,
		Reputation:   5000, // start at 50%
		Status:       ProviderActive,
		RegisteredAt: time.Now(),
	}
	return nil
}

// Unregister removes a provider. Only active/suspended providers can be removed.
func (pr *ProviderRegistry) Unregister(addr types.Address) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.providers[addr]; !exists {
		return ErrProviderNotFound
	}
	delete(pr.providers, addr)
	return nil
}

// Get returns a provider by address.
// NOTE: the returned pointer references the live provider object; reading its
// mutable fields (Stake, Reputation, Status, counters) concurrently with
// registry writes is racy. Use GetSnapshot for a race-free copy.
func (pr *ProviderRegistry) Get(addr types.Address) (*Provider, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	p, ok := pr.providers[addr]
	return p, ok
}

// GetSnapshot returns a copy of the provider, safe to read without locks.
func (pr *ProviderRegistry) GetSnapshot(addr types.Address) (Provider, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	p, ok := pr.providers[addr]
	if !ok {
		return Provider{}, false
	}
	snap := *p
	snap.Capabilities = append([]Capability(nil), p.Capabilities...)
	return snap, true
}

// UpdateStatus changes a provider's status.
func (pr *ProviderRegistry) UpdateStatus(addr types.Address, status ProviderStatus) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	p, ok := pr.providers[addr]
	if !ok {
		return ErrProviderNotFound
	}
	p.Status = status
	return nil
}

// UpdateReputation adjusts a provider's reputation score.
func (pr *ProviderRegistry) UpdateReputation(addr types.Address, delta int64) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	p, ok := pr.providers[addr]
	if !ok {
		return ErrProviderNotFound
	}
	newRep := int64(p.Reputation) + delta
	if newRep < 0 {
		newRep = 0
	}
	if newRep > 10000 {
		newRep = 10000
	}
	p.Reputation = uint64(newRep)
	return nil
}

// DeductStake reduces a provider's stake by the given amount.
func (pr *ProviderRegistry) DeductStake(addr types.Address, amount uint64) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	p, ok := pr.providers[addr]
	if !ok {
		return ErrProviderNotFound
	}
	if amount > p.Stake {
		p.Stake = 0
	} else {
		p.Stake -= amount
	}
	return nil
}

// IncrementTasks records a task completion (success or failure).
func (pr *ProviderRegistry) IncrementTasks(addr types.Address, failed bool) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	p, ok := pr.providers[addr]
	if !ok {
		return
	}
	p.TasksTotal++
	if failed {
		p.TasksFailed++
	}
}

// ListActive returns all providers with Active status.
func (pr *ProviderRegistry) ListActive() []*Provider {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	var result []*Provider
	for _, p := range pr.providers {
		if p.Status == ProviderActive {
			result = append(result, p)
		}
	}
	return result
}

// ListByCapability returns active providers that support the given capability.
func (pr *ProviderRegistry) ListByCapability(cap Capability) []*Provider {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	var result []*Provider
	for _, p := range pr.providers {
		if p.Status == ProviderActive && p.HasCapability(cap) {
			result = append(result, p)
		}
	}
	return result
}

// ActiveCount returns the number of active providers.
func (pr *ProviderRegistry) ActiveCount() int {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	count := 0
	for _, p := range pr.providers {
		if p.Status == ProviderActive {
			count++
		}
	}
	return count
}

// TotalCount returns the total number of registered providers.
func (pr *ProviderRegistry) TotalCount() int {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return len(pr.providers)
}
