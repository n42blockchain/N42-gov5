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

package parallel

import (
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// FuncSelector is the first 4 bytes of calldata identifying the function.
type FuncSelector [4]byte

// DependencyPredictor analyzes transactions to predict state conflicts
// before Block-STM execution. Groups likely-conflicting txs together
// so conflicts resolve in the first wave rather than cascading.
type DependencyPredictor struct {
	mu sync.RWMutex
	// history: contract -> selector -> set of storage slot hashes accessed
	history  map[types.Address]map[FuncSelector]map[types.Hash]bool
	maxSlots int
}

// NewDependencyPredictor creates a new predictor with maxSlots history per selector.
func NewDependencyPredictor(maxSlots int) *DependencyPredictor {
	if maxSlots <= 0 {
		maxSlots = 128
	}
	return &DependencyPredictor{
		history:  make(map[types.Address]map[FuncSelector]map[types.Hash]bool),
		maxSlots: maxSlots,
	}
}

// txInfo captures the relevant fields from a transaction for grouping.
type txInfo struct {
	index    int
	to       *types.Address
	selector FuncSelector
}

// extractTxInfo extracts To address and function selector from a transaction.
func extractTxInfo(index int, tx *transaction.Transaction) txInfo {
	info := txInfo{index: index}
	info.to = tx.To()
	data := tx.Data()
	if len(data) >= 4 {
		copy(info.selector[:], data[:4])
	}
	return info
}

// PredictOrder reorders transaction indices to cluster likely-conflicting
// transactions together. Returns a permutation of [0..len(txs)-1].
// Preserves relative order within each group.
func (p *DependencyPredictor) PredictOrder(txs []*transaction.Transaction) []int {
	n := len(txs)
	if n == 0 {
		return nil
	}

	// Extract info for all txs.
	infos := make([]txInfo, n)
	for i, tx := range txs {
		infos[i] = extractTxInfo(i, tx)
	}

	// Group txs by To address.
	// nil To (contract creation) are always independent.
	type addrGroup struct {
		addr  types.Address
		items []txInfo
	}
	groups := make(map[types.Address]*addrGroup)
	var groupOrder []types.Address
	var independent []int

	for _, info := range infos {
		if info.to == nil {
			independent = append(independent, info.index)
			continue
		}
		addr := *info.to
		g, exists := groups[addr]
		if !exists {
			g = &addrGroup{addr: addr}
			groups[addr] = g
			groupOrder = append(groupOrder, addr)
		}
		g.items = append(g.items, info)
	}

	// Determine conflict groups.
	var conflicting []int
	var nonConflicting []int

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, addr := range groupOrder {
		g := groups[addr]
		if len(g.items) <= 1 {
			// Single tx to this contract - non-conflicting.
			nonConflicting = append(nonConflicting, g.items[0].index)
			continue
		}

		// Multiple txs to same contract. Check for conflicts.
		hasConflict := false

		// Check if any two txs share a selector (likely conflict).
		selCounts := make(map[FuncSelector]int)
		for _, item := range g.items {
			selCounts[item.selector]++
			if selCounts[item.selector] > 1 {
				hasConflict = true
				break
			}
		}

		// If no duplicate selectors, check historical slot overlap.
		if !hasConflict {
			contractHistory := p.history[addr]
			if contractHistory != nil {
				hasConflict = p.selectorsOverlap(contractHistory, g.items)
			}
		}

		if hasConflict {
			for _, item := range g.items {
				conflicting = append(conflicting, item.index)
			}
		} else {
			for _, item := range g.items {
				nonConflicting = append(nonConflicting, item.index)
			}
		}
	}

	// Output: conflict group indices first (in original order), then independent.
	result := make([]int, 0, n)
	result = append(result, conflicting...)
	result = append(result, nonConflicting...)
	result = append(result, independent...)
	return result
}

// selectorsOverlap checks if any two selectors in the group have overlapping
// historical storage slot accesses.
func (p *DependencyPredictor) selectorsOverlap(contractHistory map[FuncSelector]map[types.Hash]bool, items []txInfo) bool {
	// Collect unique selectors from the group.
	sels := make([]FuncSelector, 0, len(items))
	seen := make(map[FuncSelector]bool)
	for _, item := range items {
		if !seen[item.selector] {
			seen[item.selector] = true
			sels = append(sels, item.selector)
		}
	}

	// For each pair of selectors, check if their slot sets overlap.
	for i := 0; i < len(sels); i++ {
		slotsA := contractHistory[sels[i]]
		if len(slotsA) == 0 {
			continue
		}
		for j := i + 1; j < len(sels); j++ {
			slotsB := contractHistory[sels[j]]
			if len(slotsB) == 0 {
				continue
			}
			// Check overlap: iterate the smaller set.
			small, big := slotsA, slotsB
			if len(small) > len(big) {
				small, big = big, small
			}
			for slot := range small {
				if big[slot] {
					return true
				}
			}
		}
	}
	return false
}

// RecordAccess records which slots a function on a contract accessed.
func (p *DependencyPredictor) RecordAccess(contract types.Address, selector FuncSelector, slots []types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()

	contractMap, ok := p.history[contract]
	if !ok {
		contractMap = make(map[FuncSelector]map[types.Hash]bool)
		p.history[contract] = contractMap
	}

	selectorMap, ok := contractMap[selector]
	if !ok {
		selectorMap = make(map[types.Hash]bool)
		contractMap[selector] = selectorMap
	}

	for _, slot := range slots {
		if len(selectorMap) >= p.maxSlots {
			break
		}
		selectorMap[slot] = true
	}
}

// Decay reduces history to adapt to changing patterns.
// Clears all recorded slot history.
func (p *DependencyPredictor) Decay() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.history = make(map[types.Address]map[FuncSelector]map[types.Hash]bool)
}
