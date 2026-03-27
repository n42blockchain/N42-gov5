// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package wasm

import (
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// Module represents a registered WASM module with its metadata and ABI.
type Module struct {
	Hash         types.Hash `json:"hash"`         // Keccak256 of bytecode
	Name         string     `json:"name"`
	Bytecode     []byte     `json:"-"`            // raw WASM bytecode
	Size         uint64     `json:"size"`
	Exports      []string   `json:"exports"`      // exported function names
	RegisteredAt time.Time  `json:"registeredAt"`
}

// ModuleRegistry manages registered WASM modules.
// Thread-safe for concurrent access.
type ModuleRegistry struct {
	mu      sync.RWMutex
	modules map[types.Hash]*Module
}

// NewModuleRegistry creates an empty module registry.
func NewModuleRegistry() *ModuleRegistry {
	return &ModuleRegistry{
		modules: make(map[types.Hash]*Module),
	}
}

// Register adds a WASM module to the registry. The hash is derived from bytecode.
func (r *ModuleRegistry) Register(name string, bytecode []byte, exports []string) (types.Hash, error) {
	if len(bytecode) == 0 {
		return types.Hash{}, fmt.Errorf("wasm: empty bytecode")
	}

	hash := crypto.Keccak256Hash(bytecode)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.modules[hash]; exists {
		return hash, nil // idempotent: same bytecode = same hash
	}

	bc := make([]byte, len(bytecode))
	copy(bc, bytecode)

	exp := make([]string, len(exports))
	copy(exp, exports)

	r.modules[hash] = &Module{
		Hash:         hash,
		Name:         name,
		Bytecode:     bc,
		Size:         uint64(len(bytecode)),
		Exports:      exp,
		RegisteredAt: time.Now(),
	}
	return hash, nil
}

// Unregister removes a module from the registry.
func (r *ModuleRegistry) Unregister(hash types.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[hash]; !exists {
		return fmt.Errorf("wasm: module %s not registered", hash.Hex())
	}
	delete(r.modules, hash)
	return nil
}

// Get returns a module by hash.
func (r *ModuleRegistry) Get(hash types.Hash) (*Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[hash]
	return m, ok
}

// GetBytecode returns a copy of the bytecode for a module. Returns nil if not found.
func (r *ModuleRegistry) GetBytecode(hash types.Hash) []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[hash]
	if !ok {
		return nil
	}
	result := make([]byte, len(m.Bytecode))
	copy(result, m.Bytecode)
	return result
}

// List returns all registered modules (without bytecode for efficiency).
func (r *ModuleRegistry) List() []*Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Module, 0, len(r.modules))
	for _, m := range r.modules {
		result = append(result, m)
	}
	return result
}

// Count returns the number of registered modules.
func (r *ModuleRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.modules)
}

// HasExport checks if a module exports the named function.
func (r *ModuleRegistry) HasExport(hash types.Hash, funcName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.modules[hash]
	if !ok {
		return false
	}
	for _, exp := range m.Exports {
		if exp == funcName {
			return true
		}
	}
	return false
}
