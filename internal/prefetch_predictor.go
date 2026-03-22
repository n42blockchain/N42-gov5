// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// PrefetchPredictor tracks frequently accessed storage slots per contract
// for predictive prefetching. Learns from historical execution patterns.
type PrefetchPredictor struct {
	mu       sync.RWMutex
	hotSlots map[types.Address]map[types.Hash]uint64 // contract -> slot -> count
	maxSlots int
}

// NewPrefetchPredictor creates a new predictor. maxSlots caps the number of
// tracked slots per contract to bound memory usage.
func NewPrefetchPredictor(maxSlots int) *PrefetchPredictor {
	if maxSlots <= 0 {
		maxSlots = 64
	}
	return &PrefetchPredictor{
		hotSlots: make(map[types.Address]map[types.Hash]uint64),
		maxSlots: maxSlots,
	}
}

// slotCount is a helper for sorting slots by access count.
type slotCount struct {
	slot  types.Hash
	count uint64
}

// PredictSlots returns the top-N most accessed slots for a contract.
// Returns nil if the contract has no recorded history.
func (p *PrefetchPredictor) PredictSlots(contract types.Address, maxPrefetch int) []types.Hash {
	p.mu.RLock()
	defer p.mu.RUnlock()

	slots, ok := p.hotSlots[contract]
	if !ok || len(slots) == 0 {
		return nil
	}

	// Collect all slot counts.
	entries := make([]slotCount, 0, len(slots))
	for slot, count := range slots {
		entries = append(entries, slotCount{slot: slot, count: count})
	}

	// Sort descending by count.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})

	// Return top maxPrefetch.
	n := maxPrefetch
	if n > len(entries) {
		n = len(entries)
	}

	result := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		result[i] = entries[i].slot
	}
	return result
}

// RecordSlotAccess records a single slot access for a contract.
// If the contract's slot map exceeds maxSlots, the least-accessed slot is evicted.
func (p *PrefetchPredictor) RecordSlotAccess(contract types.Address, slot types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()

	slots, ok := p.hotSlots[contract]
	if !ok {
		slots = make(map[types.Hash]uint64)
		p.hotSlots[contract] = slots
	}

	// If slot already tracked, just increment.
	if _, exists := slots[slot]; exists {
		slots[slot]++
		return
	}

	// New slot: check capacity.
	if len(slots) >= p.maxSlots {
		// Evict the slot with the lowest count.
		var minSlot types.Hash
		var minCount uint64
		first := true
		for s, c := range slots {
			if first || c < minCount {
				minSlot = s
				minCount = c
				first = false
			}
		}
		delete(slots, minSlot)
	}

	slots[slot] = 1
}

// Decay reduces all counts by the given factor (0 < factor < 1).
// Removes entries that drop to zero. Removes contracts with no remaining slots.
func (p *PrefetchPredictor) Decay(factor float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for contract, slots := range p.hotSlots {
		for slot, count := range slots {
			newCount := uint64(float64(count) * factor)
			if newCount == 0 {
				delete(slots, slot)
			} else {
				slots[slot] = newCount
			}
		}
		if len(slots) == 0 {
			delete(p.hotSlots, contract)
		}
	}
}

// Stats returns the number of tracked contracts and total tracked slots.
func (p *PrefetchPredictor) Stats() (contracts, slots int) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	contracts = len(p.hotSlots)
	for _, s := range p.hotSlots {
		slots += len(s)
	}
	return contracts, slots
}
