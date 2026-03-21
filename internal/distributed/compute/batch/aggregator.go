// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package batch

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// ReduceFunc defines a user-provided aggregation function that combines
// multiple map outputs into a single result.
type ReduceFunc func(inputs [][]byte) ([]byte, error)

// Aggregator collects map phase results and executes the reduce phase.
// It caches partial results and submits the final output.
type Aggregator struct {
	mu          sync.RWMutex
	partials    map[types.Hash]map[int][]byte // jobID → partition → data
	reduceFuncs map[types.Hash]ReduceFunc     // jobID → reduce function
}

// NewAggregator creates a new result aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{
		partials:    make(map[types.Hash]map[int][]byte),
		reduceFuncs: make(map[types.Hash]ReduceFunc),
	}
}

// RegisterReduceFunc sets the reduce function for a job.
func (a *Aggregator) RegisterReduceFunc(jobID types.Hash, fn ReduceFunc) error {
	if fn == nil {
		return fmt.Errorf("aggregator: reduce function must not be nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reduceFuncs[jobID] = fn
	return nil
}

// AddPartialResult caches a map task's output for later aggregation.
func (a *Aggregator) AddPartialResult(jobID types.Hash, partition int, data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, exists := a.partials[jobID]; !exists {
		a.partials[jobID] = make(map[int][]byte)
	}

	result := make([]byte, len(data))
	copy(result, data)
	a.partials[jobID][partition] = result
}

// PartialCount returns the number of cached partial results for a job.
func (a *Aggregator) PartialCount(jobID types.Hash) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.partials[jobID])
}

// Reduce executes the reduce function over all cached partial results,
// ordered by partition index. Returns the aggregated output.
func (a *Aggregator) Reduce(jobID types.Hash, totalPartitions int) ([]byte, error) {
	a.mu.RLock()
	fn, hasFn := a.reduceFuncs[jobID]
	partials := a.partials[jobID]
	a.mu.RUnlock()

	if !hasFn {
		return nil, fmt.Errorf("aggregator: no reduce function registered for job %s", jobID.Hex())
	}

	if len(partials) < totalPartitions {
		return nil, fmt.Errorf("aggregator: incomplete results (%d/%d)", len(partials), totalPartitions)
	}

	// Collect partials in order
	inputs := make([][]byte, totalPartitions)
	for i := 0; i < totalPartitions; i++ {
		data, ok := partials[i]
		if !ok {
			return nil, fmt.Errorf("aggregator: missing partition %d", i)
		}
		inputs[i] = data
	}

	var result []byte
	var reduceErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				reduceErr = fmt.Errorf("aggregator: reduce function panicked: %v", r)
			}
		}()
		result, reduceErr = fn(inputs)
	}()
	if reduceErr != nil {
		return nil, fmt.Errorf("aggregator: reduce failed: %w", reduceErr)
	}

	return result, nil
}

// Cleanup removes all cached data for a job.
func (a *Aggregator) Cleanup(jobID types.Hash) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.partials, jobID)
	delete(a.reduceFuncs, jobID)
}

// GetPartials returns cached partial results for a job (for inspection/debugging).
func (a *Aggregator) GetPartials(jobID types.Hash) map[int][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()

	src := a.partials[jobID]
	if len(src) == 0 {
		return nil
	}

	result := make(map[int][]byte, len(src))
	for k, v := range src {
		cp := make([]byte, len(v))
		copy(cp, v)
		result[k] = cp
	}
	return result
}
