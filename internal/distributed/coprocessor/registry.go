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

	"github.com/n42blockchain/N42/crypto"
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

// SetBytecode attaches WASM bytecode to a registered program. The bytecode
// must hash (Keccak256) to the program hash, making the registry a verified
// bytecode source: a provider that fetches it can trust it executes the
// program the submitter named.
func (r *Registry) SetBytecode(programHash types.Hash, bytecode []byte) error {
	if len(bytecode) == 0 {
		return fmt.Errorf("bytecode required")
	}
	if got := crypto.Keccak256Hash(bytecode); got != programHash {
		return fmt.Errorf("bytecode hash %s does not match program %s", got.Hex(), programHash.Hex())
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	p, exists := r.programs[programHash]
	if !exists {
		return fmt.Errorf("program %s not registered", programHash.Hex())
	}
	p.Bytecode = append([]byte(nil), bytecode...)
	return nil
}

// Bytecode returns the WASM bytecode attached to a program, if any.
func (r *Registry) Bytecode(programHash types.Hash) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, exists := r.programs[programHash]
	if !exists || len(p.Bytecode) == 0 {
		return nil, false
	}
	return p.Bytecode, true
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
