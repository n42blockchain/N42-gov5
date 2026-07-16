// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MemCAS is a minimal in-memory content-addressed store for the AI inference
// precompile's input/output blobs. N42's distributed storage layer (coprocessor,
// torrent bridge) is not wired to a generic CAS interface anything can plug
// into yet (see docs/project_distributed_compute_storage_wiring_plan), so this
// package brings its own tiny store rather than block on that larger effort —
// good enough for a single node's own inference requests; not a distributed
// or persistent store.

package inference

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// MemCAS is a thread-safe, in-memory, keccak256-addressed byte store.
type MemCAS struct {
	mu    sync.RWMutex
	store map[types.Hash][]byte
}

// NewMemCAS creates an empty in-memory CAS.
func NewMemCAS() *MemCAS {
	return &MemCAS{store: make(map[types.Hash][]byte)}
}

// Store saves data and returns its keccak256 content hash.
func (c *MemCAS) Store(data []byte) (types.Hash, error) {
	hash := crypto.Keccak256Hash(data)
	cp := make([]byte, len(data))
	copy(cp, data)
	c.mu.Lock()
	c.store[hash] = cp
	c.mu.Unlock()
	return hash, nil
}

// Load retrieves data by its content hash.
func (c *MemCAS) Load(hash types.Hash) ([]byte, error) {
	c.mu.RLock()
	data, ok := c.store[hash]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("memcas: no data for hash %s", hash.Hex())
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}
