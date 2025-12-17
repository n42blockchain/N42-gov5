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

// Package sync provides concurrent-safe data structures for high-performance scenarios.
package sync

import (
	"hash/fnv"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// ShardCount defines the number of shards for sharded maps.
// Must be a power of 2 for efficient modulo operation.
const ShardCount = 256

// ShardedAddressMap is a concurrent map sharded by address for reduced lock contention.
type ShardedAddressMap[V any] struct {
	shards [ShardCount]struct {
		sync.RWMutex
		data map[types.Address]V
	}
}

// NewShardedAddressMap creates a new sharded address map.
func NewShardedAddressMap[V any]() *ShardedAddressMap[V] {
	m := &ShardedAddressMap[V]{}
	for i := range m.shards {
		m.shards[i].data = make(map[types.Address]V)
	}
	return m
}

// getShard returns the shard index for an address.
func (m *ShardedAddressMap[V]) getShard(addr types.Address) uint8 {
	// Use first byte XOR with a hash of last bytes for better distribution
	return addr[0] ^ addr[19]
}

// Get retrieves a value by address.
func (m *ShardedAddressMap[V]) Get(addr types.Address) (V, bool) {
	shard := &m.shards[m.getShard(addr)]
	shard.RLock()
	v, ok := shard.data[addr]
	shard.RUnlock()
	return v, ok
}

// Set stores a value by address.
func (m *ShardedAddressMap[V]) Set(addr types.Address, value V) {
	shard := &m.shards[m.getShard(addr)]
	shard.Lock()
	shard.data[addr] = value
	shard.Unlock()
}

// Delete removes a value by address.
func (m *ShardedAddressMap[V]) Delete(addr types.Address) {
	shard := &m.shards[m.getShard(addr)]
	shard.Lock()
	delete(shard.data, addr)
	shard.Unlock()
}

// Has checks if an address exists.
func (m *ShardedAddressMap[V]) Has(addr types.Address) bool {
	shard := &m.shards[m.getShard(addr)]
	shard.RLock()
	_, ok := shard.data[addr]
	shard.RUnlock()
	return ok
}

// Len returns the total number of entries.
func (m *ShardedAddressMap[V]) Len() int {
	total := 0
	for i := range m.shards {
		m.shards[i].RLock()
		total += len(m.shards[i].data)
		m.shards[i].RUnlock()
	}
	return total
}

// Range iterates over all entries. The callback should not modify the map.
func (m *ShardedAddressMap[V]) Range(f func(addr types.Address, value V) bool) {
	for i := range m.shards {
		m.shards[i].RLock()
		for addr, value := range m.shards[i].data {
			if !f(addr, value) {
				m.shards[i].RUnlock()
				return
			}
		}
		m.shards[i].RUnlock()
	}
}

// ShardedHashMap is a concurrent map sharded by hash for reduced lock contention.
type ShardedHashMap[V any] struct {
	shards [ShardCount]struct {
		sync.RWMutex
		data map[types.Hash]V
	}
}

// NewShardedHashMap creates a new sharded hash map.
func NewShardedHashMap[V any]() *ShardedHashMap[V] {
	m := &ShardedHashMap[V]{}
	for i := range m.shards {
		m.shards[i].data = make(map[types.Hash]V)
	}
	return m
}

// getShard returns the shard index for a hash.
func (m *ShardedHashMap[V]) getShard(hash types.Hash) uint8 {
	return hash[0]
}

// Get retrieves a value by hash.
func (m *ShardedHashMap[V]) Get(hash types.Hash) (V, bool) {
	shard := &m.shards[m.getShard(hash)]
	shard.RLock()
	v, ok := shard.data[hash]
	shard.RUnlock()
	return v, ok
}

// Set stores a value by hash.
func (m *ShardedHashMap[V]) Set(hash types.Hash, value V) {
	shard := &m.shards[m.getShard(hash)]
	shard.Lock()
	shard.data[hash] = value
	shard.Unlock()
}

// Delete removes a value by hash.
func (m *ShardedHashMap[V]) Delete(hash types.Hash) {
	shard := &m.shards[m.getShard(hash)]
	shard.Lock()
	delete(shard.data, hash)
	shard.Unlock()
}

// ShardedStringMap is a concurrent map sharded by string key.
type ShardedStringMap[V any] struct {
	shards [ShardCount]struct {
		sync.RWMutex
		data map[string]V
	}
}

// NewShardedStringMap creates a new sharded string map.
func NewShardedStringMap[V any]() *ShardedStringMap[V] {
	m := &ShardedStringMap[V]{}
	for i := range m.shards {
		m.shards[i].data = make(map[string]V)
	}
	return m
}

// getShard returns the shard index for a string key.
func (m *ShardedStringMap[V]) getShard(key string) uint8 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return uint8(h.Sum32())
}

// Get retrieves a value by key.
func (m *ShardedStringMap[V]) Get(key string) (V, bool) {
	shard := &m.shards[m.getShard(key)]
	shard.RLock()
	v, ok := shard.data[key]
	shard.RUnlock()
	return v, ok
}

// Set stores a value by key.
func (m *ShardedStringMap[V]) Set(key string, value V) {
	shard := &m.shards[m.getShard(key)]
	shard.Lock()
	shard.data[key] = value
	shard.Unlock()
}

// Delete removes a value by key.
func (m *ShardedStringMap[V]) Delete(key string) {
	shard := &m.shards[m.getShard(key)]
	shard.Lock()
	delete(shard.data, key)
	shard.Unlock()
}

