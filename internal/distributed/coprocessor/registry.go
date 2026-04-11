// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Program Registry mapping programHash to a verification key. Used by
// the coprocessor service to look up the correct ZK verifier for a
// submitted task. Register clones the vk bytes and enforces unique
// program hashes across the process.

package coprocessor

import (
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// Registry manages registered verifiable programs (programHash → verification key).
// Thread-safe for concurrent access.
type Registry struct {
	mu       sync.RWMutex
	programs map[types.Hash]*Program
}

// NewRegistry creates an empty program registry.
func NewRegistry() *Registry {
	return &Registry{
		programs: make(map[types.Hash]*Program),
	}
}

// Register adds a program to the registry.
func (r *Registry) Register(programHash types.Hash, verificationKey []byte, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.programs[programHash]; exists {
		return fmt.Errorf("program %s already registered", programHash.Hex())
	}
	if len(verificationKey) == 0 {
		return fmt.Errorf("verification key required")
	}

	vk := make([]byte, len(verificationKey))
	copy(vk, verificationKey)

	r.programs[programHash] = &Program{
		Hash:            programHash,
		VerificationKey: vk,
		Name:            name,
		RegisteredAt:    time.Now(),
	}
	return nil
}

// Unregister removes a program from the registry.
func (r *Registry) Unregister(programHash types.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.programs[programHash]; !exists {
		return fmt.Errorf("program %s not registered", programHash.Hex())
	}
	delete(r.programs, programHash)
	return nil
}

// Get returns a program by hash, or nil if not found.
func (r *Registry) Get(programHash types.Hash) (*Program, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.programs[programHash]
	return p, ok
}

// List returns all registered programs.
func (r *Registry) List() []*Program {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Program, 0, len(r.programs))
	for _, p := range r.programs {
		result = append(result, p)
	}
	return result
}

// Count returns the number of registered programs.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.programs)
}
