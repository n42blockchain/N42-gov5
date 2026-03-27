// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package inference implements AI model inference with optimistic machine learning
// verification (opML pattern from ORA Protocol).
package inference

import (
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// ModelFormat identifies the serialization format of an AI model.
type ModelFormat uint8

const (
	FormatONNX        ModelFormat = 0
	FormatSafeTensors ModelFormat = 1
	FormatCustom      ModelFormat = 2
)

func (f ModelFormat) String() string {
	switch f {
	case FormatONNX:
		return "onnx"
	case FormatSafeTensors:
		return "safetensors"
	case FormatCustom:
		return "custom"
	default:
		return "unknown"
	}
}

// Model represents a registered AI model available for inference.
type Model struct {
	Hash         types.Hash  `json:"hash"`         // Keccak256 of model weights
	Name         string      `json:"name"`
	Format       ModelFormat `json:"format"`
	Size         uint64      `json:"size"`         // model size in bytes
	Version      string      `json:"version"`
	Capabilities []string    `json:"capabilities"` // e.g., ["text-classification", "embedding"]
	RegisteredAt time.Time   `json:"registeredAt"`
}

// InferenceRequest represents a request to run inference on a model.
type InferenceRequest struct {
	ID        types.Hash    `json:"id"`
	ModelHash types.Hash    `json:"modelHash"`
	Input     []byte        `json:"input"`
	Submitter types.Address `json:"submitter"`
	CreatedAt time.Time     `json:"createdAt"`
}

// InferenceResult holds the output of an inference execution.
type InferenceResult struct {
	RequestID    types.Hash `json:"requestId"`
	Output       []byte     `json:"output"`
	Confidence   float64    `json:"confidence"`   // 0.0-1.0
	ModelVersion string     `json:"modelVersion"`
	GasUsed      uint64     `json:"gasUsed"`
	ComputedAt   time.Time  `json:"computedAt"`
}

// ModelRegistry manages registered AI models.
type ModelRegistry struct {
	mu     sync.RWMutex
	models map[types.Hash]*Model
}

// NewModelRegistry creates an empty model registry.
func NewModelRegistry() *ModelRegistry {
	return &ModelRegistry{
		models: make(map[types.Hash]*Model),
	}
}

// Register adds a model to the registry.
func (r *ModelRegistry) Register(name string, format ModelFormat, size uint64, version string, capabilities []string) (types.Hash, error) {
	if name == "" {
		return types.Hash{}, fmt.Errorf("inference: model name required")
	}

	// Derive model hash from name + version + format
	data := []byte(fmt.Sprintf("%s:%s:%d", name, version, format))
	hash := crypto.Keccak256Hash(data)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.models[hash]; exists {
		return hash, nil // idempotent
	}

	caps := make([]string, len(capabilities))
	copy(caps, capabilities)

	r.models[hash] = &Model{
		Hash:         hash,
		Name:         name,
		Format:       format,
		Size:         size,
		Version:      version,
		Capabilities: caps,
		RegisteredAt: time.Now(),
	}
	return hash, nil
}

// Unregister removes a model from the registry.
func (r *ModelRegistry) Unregister(hash types.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.models[hash]; !exists {
		return fmt.Errorf("inference: model %s not registered", hash.Hex())
	}
	delete(r.models, hash)
	return nil
}

// Get returns a model by hash.
func (r *ModelRegistry) Get(hash types.Hash) (*Model, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[hash]
	return m, ok
}

// List returns all registered models.
func (r *ModelRegistry) List() []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Model, 0, len(r.models))
	for _, m := range r.models {
		result = append(result, m)
	}
	return result
}

// FindByCapability returns models that advertise the given capability.
func (r *ModelRegistry) FindByCapability(capability string) []*Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Model
	for _, m := range r.models {
		for _, cap := range m.Capabilities {
			if cap == capability {
				result = append(result, m)
				break
			}
		}
	}
	return result
}

// Count returns the number of registered models.
func (r *ModelRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.models)
}
