// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// AgentRegistry: on-node discovery store for AI agents. Agents stake
// at least minStake (1 ETH) and declare capabilities; lookups return
// matching agents sorted by reputation score. Sentinel errors
// (ErrAgentExists, ErrAgentNotFound, ErrInsufficientStake) are
// exposed to callers running the negotiation protocol.

package coord

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/holiman/uint256"
)

// minStake is the minimum stake required for agent registration (1 ETH in wei).
var minStake = uint256.NewInt(1_000_000_000_000_000_000)

// Errors returned by the agent registry.
var (
	ErrAgentExists       = errors.New("coord: already registered")
	ErrAgentNotFound     = errors.New("coord: not found")
	ErrRegistryFull      = errors.New("coord: registry at capacity")
	ErrInsufficientStake = errors.New("coord: stake below minimum")
	ErrNoCapabilities    = errors.New("coord: at least one capability required")
	ErrEmptyEndpoint     = errors.New("coord: endpoint must not be empty")
	ErrEmptyDID          = errors.New("coord: agent DID must not be empty")
)

// AgentInfo describes a registered agent's capabilities and status.
type AgentInfo struct {
	DID            string       `json:"did"`
	Capabilities   []string     `json:"capabilities"`
	Endpoint       string       `json:"endpoint"`
	Stake          *uint256.Int `json:"stake"`
	Reputation     float64      `json:"reputation"`
	RegisteredAt   time.Time    `json:"registeredAt"`
	LastActive     time.Time    `json:"lastActive"`
	TasksCompleted uint64       `json:"tasksCompleted"`
	TasksFailed    uint64       `json:"tasksFailed"`
}

// clone returns a deep copy of the AgentInfo.
func (a *AgentInfo) clone() *AgentInfo {
	cp := *a
	cp.Capabilities = make([]string, len(a.Capabilities))
	copy(cp.Capabilities, a.Capabilities)
	if a.Stake != nil {
		cp.Stake = new(uint256.Int).Set(a.Stake)
	}
	return &cp
}

// AgentRegistry manages the discovery and lookup of AI agents.
// All methods are safe for concurrent use.
type AgentRegistry struct {
	mu           sync.RWMutex
	agents       map[string]*AgentInfo   // keyed by DID
	byCapability map[string][]string     // capability -> []DID
	maxAgents    int
}

// NewAgentRegistry creates an agent registry with the given maximum capacity.
// If maxAgents <= 0 it defaults to 10000.
func NewAgentRegistry(maxAgents int) *AgentRegistry {
	if maxAgents <= 0 {
		maxAgents = 10000
	}
	return &AgentRegistry{
		agents:       make(map[string]*AgentInfo),
		byCapability: make(map[string][]string),
		maxAgents:    maxAgents,
	}
}

// RegisterAgent registers a new agent with the given capabilities and stake.
// The stake must be >= minStake (1 ETH). Duplicate DIDs are rejected.
func (r *AgentRegistry) RegisterAgent(did string, capabilities []string, endpoint string, stake *uint256.Int) error {
	if did == "" {
		return ErrEmptyDID
	}
	if endpoint == "" {
		return ErrEmptyEndpoint
	}
	if len(capabilities) == 0 {
		return ErrNoCapabilities
	}
	if stake == nil || stake.Cmp(minStake) < 0 {
		return ErrInsufficientStake
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.agents[did]; exists {
		return ErrAgentExists
	}
	if len(r.agents) >= r.maxAgents {
		return ErrRegistryFull
	}

	// Deduplicate capabilities before indexing.
	seen := make(map[string]struct{}, len(capabilities))
	deduped := make([]string, 0, len(capabilities))
	for _, cap := range capabilities {
		if _, dup := seen[cap]; !dup {
			seen[cap] = struct{}{}
			deduped = append(deduped, cap)
		}
	}

	now := time.Now()
	info := &AgentInfo{
		DID:          did,
		Capabilities: deduped,
		Endpoint:     endpoint,
		Stake:        new(uint256.Int).Set(stake),
		Reputation:   50.0, // default starting reputation
		RegisteredAt: now,
		LastActive:   now,
	}

	r.agents[did] = info

	// Index by capability.
	for _, cap := range deduped {
		r.byCapability[cap] = append(r.byCapability[cap], did)
	}

	return nil
}

// UnregisterAgent removes an agent and its capability index entries.
func (r *AgentRegistry) UnregisterAgent(did string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agents[did]
	if !exists {
		return ErrAgentNotFound
	}

	// Remove from capability indexes.
	for _, cap := range info.Capabilities {
		dids := r.byCapability[cap]
		for i, d := range dids {
			if d == did {
				r.byCapability[cap] = append(dids[:i], dids[i+1:]...)
				break
			}
		}
		if len(r.byCapability[cap]) == 0 {
			delete(r.byCapability, cap)
		}
	}

	delete(r.agents, did)
	return nil
}

// FindByCapability returns agents with the given capability, sorted by
// reputation in descending order. Returns nil if no agents match.
func (r *AgentRegistry) FindByCapability(capability string) []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	dids, ok := r.byCapability[capability]
	if !ok || len(dids) == 0 {
		return nil
	}

	result := make([]*AgentInfo, 0, len(dids))
	for _, did := range dids {
		if info, exists := r.agents[did]; exists {
			result = append(result, info.clone())
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Reputation > result[j].Reputation
	})

	return result
}

// GetAgent returns a copy of the agent info for the given DID.
func (r *AgentRegistry) GetAgent(did string) (*AgentInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	info, exists := r.agents[did]
	if !exists {
		return nil, ErrAgentNotFound
	}
	return info.clone(), nil
}

// UpdateReputation adjusts an agent's reputation by delta.
// The resulting reputation is clamped to the range [0, 100].
func (r *AgentRegistry) UpdateReputation(did string, delta float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	info, exists := r.agents[did]
	if !exists {
		return ErrAgentNotFound
	}

	info.Reputation += delta
	if info.Reputation > 100 {
		info.Reputation = 100
	}
	if info.Reputation < 0 {
		info.Reputation = 0
	}
	return nil
}

// UpdateLastActive sets the agent's LastActive timestamp to now.
func (r *AgentRegistry) UpdateLastActive(did string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if info, exists := r.agents[did]; exists {
		info.LastActive = time.Now()
	}
}

// RecordTaskComplete increments the agent's completed task counter.
func (r *AgentRegistry) RecordTaskComplete(did string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if info, exists := r.agents[did]; exists {
		info.TasksCompleted++
	}
}

// RecordTaskFailed increments the agent's failed task counter.
func (r *AgentRegistry) RecordTaskFailed(did string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if info, exists := r.agents[did]; exists {
		info.TasksFailed++
	}
}

// Count returns the total number of registered agents.
func (r *AgentRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// AllAgents returns a snapshot of all registered agents.
func (r *AgentRegistry) AllAgents() []*AgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*AgentInfo, 0, len(r.agents))
	for _, info := range r.agents {
		result = append(result, info.clone())
	}
	return result
}
