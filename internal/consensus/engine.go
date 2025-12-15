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

package consensus

import (
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Simplified/Core Engine Interface
// =============================================================================

// CoreEngine defines the minimal interface for a consensus engine.
// This is a simplified version that covers the essential operations.
//
// For full functionality, use the Engine interface defined in consensus.go.
type CoreEngine interface {
	// VerifyHeader checks whether a header conforms to the consensus rules.
	VerifyHeader(chain ChainHeaderReader, header block.IHeader) error

	// VerifyHeaders verifies a batch of headers concurrently.
	// Returns a quit channel to abort and a results channel for async verification.
	VerifyHeaders(chain ChainHeaderReader, headers []block.IHeader) (chan<- struct{}, <-chan error)

	// Prepare initializes the consensus fields of a block header.
	Prepare(chain ChainHeaderReader, header block.IHeader) error

	// Finalize runs any post-transaction state modifications.
	Finalize(chain ChainHeaderReader, header block.IHeader, state *state.IntraBlockState) error

	// Seal generates a new sealing request for the given input block.
	Seal(chain ChainHeaderReader, block block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error

	// Author retrieves the address of the account that minted the given block.
	Author(header block.IHeader) (types.Address, error)

	// APIs returns the RPC APIs this consensus engine provides.
	APIs(chain ConsensusChainReader) []jsonrpc.API

	// Close terminates any background threads maintained by the consensus engine.
	Close() error
}

// =============================================================================
// Engine Adapter - Adapts full Engine to CoreEngine
// =============================================================================

// EngineAdapter adapts the full Engine interface to the simplified CoreEngine interface.
// This allows using the full Engine where CoreEngine is expected.
type EngineAdapter struct {
	engine Engine
}

// NewEngineAdapter creates an adapter for the given Engine.
func NewEngineAdapter(engine Engine) *EngineAdapter {
	return &EngineAdapter{engine: engine}
}

// VerifyHeader implements CoreEngine.
func (a *EngineAdapter) VerifyHeader(chain ChainHeaderReader, header block.IHeader) error {
	return a.engine.VerifyHeader(chain, header, true) // seal=true for default
}

// VerifyHeaders implements CoreEngine.
func (a *EngineAdapter) VerifyHeaders(chain ChainHeaderReader, headers []block.IHeader) (chan<- struct{}, <-chan error) {
	// Create seals slice with all true (verify all seals)
	seals := make([]bool, len(headers))
	for i := range seals {
		seals[i] = true
	}
	return a.engine.VerifyHeaders(chain, headers, seals)
}

// Prepare implements CoreEngine.
func (a *EngineAdapter) Prepare(chain ChainHeaderReader, header block.IHeader) error {
	return a.engine.Prepare(chain, header)
}

// Finalize implements CoreEngine.
func (a *EngineAdapter) Finalize(chain ChainHeaderReader, header block.IHeader, state *state.IntraBlockState) error {
	_, _, err := a.engine.Finalize(chain, header, state, nil, nil)
	return err
}

// Seal implements CoreEngine.
func (a *EngineAdapter) Seal(chain ChainHeaderReader, b block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	return a.engine.Seal(chain, b, results, stop)
}

// Author implements CoreEngine.
func (a *EngineAdapter) Author(header block.IHeader) (types.Address, error) {
	return a.engine.Author(header)
}

// APIs implements CoreEngine.
func (a *EngineAdapter) APIs(chain ConsensusChainReader) []jsonrpc.API {
	return a.engine.APIs(chain)
}

// Close implements CoreEngine.
func (a *EngineAdapter) Close() error {
	return a.engine.Close()
}

// Inner returns the underlying Engine.
func (a *EngineAdapter) Inner() Engine {
	return a.engine
}

// =============================================================================
// Instrumented Engine - Wraps Engine with Metrics
// =============================================================================

// EngineStats holds accumulated statistics for engine operations.
type EngineStats struct {
	// Verification stats
	VerifyHeaderCount   uint64
	VerifyHeaderTimeNs  uint64
	VerifyHeadersCount  uint64
	VerifyHeadersTimeNs uint64

	// Block production stats
	PrepareCount   uint64
	PrepareTimeNs  uint64
	FinalizeCount  uint64
	FinalizeTimeNs uint64
	SealCount      uint64
	SealTimeNs     uint64

	// Other stats
	AuthorCount   uint64
	AuthorTimeNs  uint64
	APICallCount  uint64
}

// TotalVerifyTime returns total time spent in verification operations.
func (s EngineStats) TotalVerifyTime() time.Duration {
	return time.Duration(s.VerifyHeaderTimeNs + s.VerifyHeadersTimeNs)
}

// TotalProductionTime returns total time spent in block production operations.
func (s EngineStats) TotalProductionTime() time.Duration {
	return time.Duration(s.PrepareTimeNs + s.FinalizeTimeNs + s.SealTimeNs)
}

// InstrumentedEngine wraps an Engine with instrumentation for timing and metrics.
// This enables performance monitoring without modifying the consensus implementations.
//
// Usage:
//
//	engine := apoa.New(...)
//	instrumented := consensus.NewInstrumentedEngine(engine, true)
//	// Use instrumented as an Engine
//	instrumented.LogStats()
type InstrumentedEngine struct {
	inner   Engine
	enabled bool

	// Metrics
	verifyHeaderCount   uint64
	verifyHeaderTimeNs  uint64
	verifyHeadersCount  uint64
	verifyHeadersTimeNs uint64
	prepareCount        uint64
	prepareTimeNs       uint64
	finalizeCount       uint64
	finalizeTimeNs      uint64
	sealCount           uint64
	sealTimeNs          uint64
	authorCount         uint64
	authorTimeNs        uint64
	apiCallCount        uint64
}

// NewInstrumentedEngine creates a new instrumented engine wrapper.
// Set enabled=false in production to minimize overhead.
func NewInstrumentedEngine(inner Engine, enabled bool) *InstrumentedEngine {
	return &InstrumentedEngine{
		inner:   inner,
		enabled: enabled,
	}
}

// =============================================================================
// EngineReader Implementation
// =============================================================================

func (e *InstrumentedEngine) Author(header block.IHeader) (types.Address, error) {
	if !e.enabled {
		return e.inner.Author(header)
	}

	start := time.Now()
	addr, err := e.inner.Author(header)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.authorCount, 1)
	atomic.AddUint64(&e.authorTimeNs, elapsed)

	return addr, err
}

func (e *InstrumentedEngine) IsServiceTransaction(sender types.Address, syscall SystemCall) bool {
	return e.inner.IsServiceTransaction(sender, syscall)
}

func (e *InstrumentedEngine) Type() params.ConsensusType {
	return e.inner.Type()
}

// =============================================================================
// Engine Implementation
// =============================================================================

func (e *InstrumentedEngine) VerifyHeader(chain ChainHeaderReader, header block.IHeader, seal bool) error {
	if !e.enabled {
		return e.inner.VerifyHeader(chain, header, seal)
	}

	start := time.Now()
	err := e.inner.VerifyHeader(chain, header, seal)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.verifyHeaderCount, 1)
	atomic.AddUint64(&e.verifyHeaderTimeNs, elapsed)

	return err
}

func (e *InstrumentedEngine) VerifyHeaders(chain ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	if !e.enabled {
		return e.inner.VerifyHeaders(chain, headers, seals)
	}

	start := time.Now()
	quit, results := e.inner.VerifyHeaders(chain, headers, seals)

	// Wrap results channel to capture timing
	wrappedResults := make(chan error, len(headers))
	go func() {
		defer close(wrappedResults)
		for err := range results {
			wrappedResults <- err
		}
		elapsed := uint64(time.Since(start).Nanoseconds())
		atomic.AddUint64(&e.verifyHeadersCount, uint64(len(headers)))
		atomic.AddUint64(&e.verifyHeadersTimeNs, elapsed)
	}()

	return quit, wrappedResults
}

func (e *InstrumentedEngine) VerifyUncles(chain ConsensusChainReader, b block.IBlock) error {
	return e.inner.VerifyUncles(chain, b)
}

func (e *InstrumentedEngine) Prepare(chain ChainHeaderReader, header block.IHeader) error {
	if !e.enabled {
		return e.inner.Prepare(chain, header)
	}

	start := time.Now()
	err := e.inner.Prepare(chain, header)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.prepareCount, 1)
	atomic.AddUint64(&e.prepareTimeNs, elapsed)

	return err
}

func (e *InstrumentedEngine) Finalize(chain ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	if !e.enabled {
		return e.inner.Finalize(chain, header, state, txs, uncles)
	}

	start := time.Now()
	rewards, balanceChanges, err := e.inner.Finalize(chain, header, state, txs, uncles)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.finalizeCount, 1)
	atomic.AddUint64(&e.finalizeTimeNs, elapsed)

	return rewards, balanceChanges, err
}

func (e *InstrumentedEngine) FinalizeAndAssemble(chain ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	if !e.enabled {
		return e.inner.FinalizeAndAssemble(chain, header, state, txs, uncles, receipts)
	}

	start := time.Now()
	b, rewards, balanceChanges, err := e.inner.FinalizeAndAssemble(chain, header, state, txs, uncles, receipts)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.finalizeCount, 1)
	atomic.AddUint64(&e.finalizeTimeNs, elapsed)

	return b, rewards, balanceChanges, err
}

func (e *InstrumentedEngine) Seal(chain ChainHeaderReader, b block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	if !e.enabled {
		return e.inner.Seal(chain, b, results, stop)
	}

	start := time.Now()
	err := e.inner.Seal(chain, b, results, stop)
	elapsed := uint64(time.Since(start).Nanoseconds())

	atomic.AddUint64(&e.sealCount, 1)
	atomic.AddUint64(&e.sealTimeNs, elapsed)

	return err
}

func (e *InstrumentedEngine) SealHash(header block.IHeader) types.Hash {
	return e.inner.SealHash(header)
}

func (e *InstrumentedEngine) CalcDifficulty(chain ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return e.inner.CalcDifficulty(chain, time, parent)
}

func (e *InstrumentedEngine) APIs(chain ConsensusChainReader) []jsonrpc.API {
	atomic.AddUint64(&e.apiCallCount, 1)
	return e.inner.APIs(chain)
}

func (e *InstrumentedEngine) Close() error {
	return e.inner.Close()
}

// =============================================================================
// Statistics
// =============================================================================

// Stats returns the accumulated statistics.
func (e *InstrumentedEngine) Stats() EngineStats {
	return EngineStats{
		VerifyHeaderCount:   atomic.LoadUint64(&e.verifyHeaderCount),
		VerifyHeaderTimeNs:  atomic.LoadUint64(&e.verifyHeaderTimeNs),
		VerifyHeadersCount:  atomic.LoadUint64(&e.verifyHeadersCount),
		VerifyHeadersTimeNs: atomic.LoadUint64(&e.verifyHeadersTimeNs),
		PrepareCount:        atomic.LoadUint64(&e.prepareCount),
		PrepareTimeNs:       atomic.LoadUint64(&e.prepareTimeNs),
		FinalizeCount:       atomic.LoadUint64(&e.finalizeCount),
		FinalizeTimeNs:      atomic.LoadUint64(&e.finalizeTimeNs),
		SealCount:           atomic.LoadUint64(&e.sealCount),
		SealTimeNs:          atomic.LoadUint64(&e.sealTimeNs),
		AuthorCount:         atomic.LoadUint64(&e.authorCount),
		AuthorTimeNs:        atomic.LoadUint64(&e.authorTimeNs),
		APICallCount:        atomic.LoadUint64(&e.apiCallCount),
	}
}

// LogStats logs the accumulated statistics at debug level.
func (e *InstrumentedEngine) LogStats() {
	stats := e.Stats()
	log.Debug("Consensus engine stats",
		"type", e.inner.Type(),
		"verify_header_count", stats.VerifyHeaderCount,
		"verify_header_time", time.Duration(stats.VerifyHeaderTimeNs),
		"verify_headers_count", stats.VerifyHeadersCount,
		"verify_headers_time", time.Duration(stats.VerifyHeadersTimeNs),
		"prepare_count", stats.PrepareCount,
		"prepare_time", time.Duration(stats.PrepareTimeNs),
		"finalize_count", stats.FinalizeCount,
		"finalize_time", time.Duration(stats.FinalizeTimeNs),
		"seal_count", stats.SealCount,
		"seal_time", time.Duration(stats.SealTimeNs),
		"author_count", stats.AuthorCount,
		"api_calls", stats.APICallCount,
	)
}

// ResetStats clears all counters.
func (e *InstrumentedEngine) ResetStats() {
	atomic.StoreUint64(&e.verifyHeaderCount, 0)
	atomic.StoreUint64(&e.verifyHeaderTimeNs, 0)
	atomic.StoreUint64(&e.verifyHeadersCount, 0)
	atomic.StoreUint64(&e.verifyHeadersTimeNs, 0)
	atomic.StoreUint64(&e.prepareCount, 0)
	atomic.StoreUint64(&e.prepareTimeNs, 0)
	atomic.StoreUint64(&e.finalizeCount, 0)
	atomic.StoreUint64(&e.finalizeTimeNs, 0)
	atomic.StoreUint64(&e.sealCount, 0)
	atomic.StoreUint64(&e.sealTimeNs, 0)
	atomic.StoreUint64(&e.authorCount, 0)
	atomic.StoreUint64(&e.authorTimeNs, 0)
	atomic.StoreUint64(&e.apiCallCount, 0)
}

// Inner returns the underlying Engine.
func (e *InstrumentedEngine) Inner() Engine {
	return e.inner
}

// =============================================================================
// Compile-time Interface Compliance
// =============================================================================

var (
	_ Engine       = (*InstrumentedEngine)(nil)
	_ EngineReader = (*InstrumentedEngine)(nil)
	_ CoreEngine   = (*EngineAdapter)(nil)
)

