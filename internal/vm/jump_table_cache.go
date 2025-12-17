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

package vm

import (
	"sync"

	"github.com/n42blockchain/N42/params"
)

// JumpTableCache provides cached jump tables to avoid repeated allocations.
// Jump tables are immutable once created, so they can be safely shared.
var jumpTableCache = &jumpTableCacheType{
	tables: make(map[string]JumpTable),
}

type jumpTableCacheType struct {
	mu     sync.RWMutex
	tables map[string]JumpTable
}

// GetCachedJumpTable returns a cached jump table for the given rules.
// If the table doesn't exist, it creates and caches it.
func GetCachedJumpTable(chainID uint64, rules *params.Rules) JumpTable {
	key := jumpTableCacheKey(rules)

	// Fast path: read lock
	jumpTableCache.mu.RLock()
	table, ok := jumpTableCache.tables[key]
	jumpTableCache.mu.RUnlock()
	if ok {
		return table
	}

	// Slow path: create and cache
	jumpTableCache.mu.Lock()
	defer jumpTableCache.mu.Unlock()

	// Double-check after acquiring write lock
	if table, ok = jumpTableCache.tables[key]; ok {
		return table
	}

	// Create new table
	table = newJumpTableForRules(rules)
	jumpTableCache.tables[key] = table
	return table
}

// jumpTableCacheKey generates a cache key for the given chain rules.
func jumpTableCacheKey(rules *params.Rules) string {
	// Build key based on relevant rule flags
	key := ""
	if rules.IsHomestead {
		key += "H"
	}
	if rules.IsTangerineWhistle {
		key += "TW"
	}
	if rules.IsSpuriousDragon {
		key += "SD"
	}
	if rules.IsByzantium {
		key += "B"
	}
	if rules.IsConstantinople {
		key += "C"
	}
	if rules.IsPetersburg {
		key += "P"
	}
	if rules.IsIstanbul {
		key += "I"
	}
	if rules.IsBerlin {
		key += "Be"
	}
	if rules.IsLondon {
		key += "L"
	}
	if rules.IsShanghai {
		key += "S"
	}
	if rules.IsCancun {
		key += "Ca"
	}
	if rules.IsPectra {
		key += "Pe"
	}
	if rules.IsOsaka {
		key += "O"
	}
	if key == "" {
		key = "frontier"
	}
	return key
}

// newJumpTableForRules creates a new jump table for the given rules.
func newJumpTableForRules(rules *params.Rules) JumpTable {
	switch {
	case rules.IsOsaka:
		return newOsakaInstructionSet()
	case rules.IsPectra:
		return newPectraInstructionSet()
	case rules.IsCancun:
		return newCancunInstructionSet()
	case rules.IsShanghai:
		return newShanghaiInstructionSet()
	case rules.IsLondon:
		return newLondonInstructionSet()
	case rules.IsBerlin:
		return newBerlinInstructionSet()
	case rules.IsIstanbul:
		return newIstanbulInstructionSet()
	case rules.IsConstantinople:
		return newConstantinopleInstructionSet()
	case rules.IsByzantium:
		return newByzantiumInstructionSet()
	case rules.IsSpuriousDragon:
		return newSpuriousDragonInstructionSet()
	case rules.IsTangerineWhistle:
		return newTangerineWhistleInstructionSet()
	case rules.IsHomestead:
		return newHomesteadInstructionSet()
	default:
		return newFrontierInstructionSet()
	}
}

// PrewarmJumpTables pre-creates jump tables for all known hard forks.
// This should be called during node startup to avoid allocation during execution.
func PrewarmJumpTables() {
	forks := []params.Rules{
		{}, // Frontier
		{IsHomestead: true},
		{IsHomestead: true, IsTangerineWhistle: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true},
		{IsHomestead: true, IsTangerineWhistle: true, IsSpuriousDragon: true, IsByzantium: true, IsConstantinople: true, IsPetersburg: true, IsIstanbul: true, IsBerlin: true, IsLondon: true, IsShanghai: true, IsCancun: true, IsPectra: true, IsOsaka: true},
	}

	for i := range forks {
		GetCachedJumpTable(0, &forks[i])
	}
}

