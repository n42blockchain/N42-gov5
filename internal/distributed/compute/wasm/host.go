// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package wasm

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// HostFunc represents a host function callable from WASM modules.
type HostFunc struct {
	Name    string
	Handler func(args []byte) ([]byte, uint64, error) // returns result, gas cost, error
}

// HostFunctions manages the set of host functions available to WASM modules.
// Sandbox policy: no filesystem, no network, deterministic execution only.
type HostFunctions struct {
	mu    sync.RWMutex
	funcs map[string]*HostFunc

	// CAS callbacks for content-addressed storage integration
	casLoad  func(hash types.Hash) ([]byte, error)
	casStore func(data []byte) (types.Hash, error)

	// Log buffer for deterministic logging (no stdout)
	logMu   sync.Mutex
	logBuf  []string
	gasTable *GasTable
}

// NewHostFunctions creates the default host function set.
func NewHostFunctions() *HostFunctions {
	hf := &HostFunctions{
		funcs:    make(map[string]*HostFunc),
		gasTable: DefaultGasTable(),
	}
	hf.registerDefaults()
	return hf
}

// SetCASCallbacks configures the CAS load/store callbacks.
func (hf *HostFunctions) SetCASCallbacks(
	load func(types.Hash) ([]byte, error),
	store func([]byte) (types.Hash, error),
) {
	hf.mu.Lock()
	defer hf.mu.Unlock()
	hf.casLoad = load
	hf.casStore = store
}

// Get returns a host function by name.
func (hf *HostFunctions) Get(name string) (*HostFunc, bool) {
	hf.mu.RLock()
	defer hf.mu.RUnlock()
	f, ok := hf.funcs[name]
	return f, ok
}

// Register adds a custom host function.
func (hf *HostFunctions) Register(f *HostFunc) {
	hf.mu.Lock()
	defer hf.mu.Unlock()
	hf.funcs[f.Name] = f
}

// LogOutput returns and clears the accumulated log buffer.
func (hf *HostFunctions) LogOutput() []string {
	hf.logMu.Lock()
	defer hf.logMu.Unlock()
	out := make([]string, len(hf.logBuf))
	copy(out, hf.logBuf)
	hf.logBuf = hf.logBuf[:0]
	return out
}

// List returns the names of all registered host functions.
func (hf *HostFunctions) List() []string {
	hf.mu.RLock()
	defer hf.mu.RUnlock()
	names := make([]string, 0, len(hf.funcs))
	for name := range hf.funcs {
		names = append(names, name)
	}
	return names
}

func (hf *HostFunctions) registerDefaults() {
	// cas_load: retrieve content from CAS by hash
	hf.funcs["cas_load"] = &HostFunc{
		Name: "cas_load",
		Handler: func(args []byte) ([]byte, uint64, error) {
			if len(args) < 32 {
				return nil, 0, fmt.Errorf("cas_load: hash must be 32 bytes")
			}
			hash := types.BytesToHash(args[:32])
			hf.mu.RLock()
			loader := hf.casLoad
			hf.mu.RUnlock()
			if loader == nil {
				return nil, 0, fmt.Errorf("cas_load: CAS not configured")
			}
			data, err := loader(hash)
			return data, hf.gasTable.HostCASLoad, err
		},
	}

	// cas_store: store content in CAS, returns hash
	hf.funcs["cas_store"] = &HostFunc{
		Name: "cas_store",
		Handler: func(args []byte) ([]byte, uint64, error) {
			hf.mu.RLock()
			storer := hf.casStore
			hf.mu.RUnlock()
			if storer == nil {
				return nil, 0, fmt.Errorf("cas_store: CAS not configured")
			}
			hash, err := storer(args)
			if err != nil {
				return nil, 0, err
			}
			return hash[:], hf.gasTable.HostCASStore, nil
		},
	}

	// keccak256: compute Keccak-256 hash
	hf.funcs["keccak256"] = &HostFunc{
		Name: "keccak256",
		Handler: func(args []byte) ([]byte, uint64, error) {
			hash := crypto.Keccak256Hash(args)
			cost := hf.gasTable.Keccak256Cost(len(args))
			return hash[:], cost, nil
		},
	}

	// log_msg: append a log message (deterministic, no stdout)
	hf.funcs["log_msg"] = &HostFunc{
		Name: "log_msg",
		Handler: func(args []byte) ([]byte, uint64, error) {
			msg := string(args)
			hf.logMu.Lock()
			hf.logBuf = append(hf.logBuf, msg)
			hf.logMu.Unlock()
			cost := hf.gasTable.LogMsgCost(len(args))
			return nil, cost, nil
		},
	}

	// sha256: compute SHA-256 hash
	hf.funcs["sha256"] = &HostFunc{
		Name: "sha256",
		Handler: func(args []byte) ([]byte, uint64, error) {
			h := sha256.Sum256(args)
			cost := hf.gasTable.Keccak256Cost(len(args)) // same cost model
			return h[:], cost, nil
		},
	}
}
