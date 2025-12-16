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

package sync

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/log"
)

// =============================================================================
// Fetcher Interface
// =============================================================================

// BlockFetcher defines the interface for fetching blocks from peers.
// This abstraction allows different fetching strategies (round-robin, parallel, etc.)
// and enables easy testing with mock implementations.
//
// Implementations:
//   - BasicFetcher: Simple single-peer fetching
//   - InstrumentedFetcher: Wraps any fetcher with metrics collection
//
// Usage:
//
//	fetcher := sync.NewBasicFetcher(ctx, blockchain, p2p, config)
//	fetcher.Start()
//	defer fetcher.Stop()
//	result, err := fetcher.FetchBlocks(ctx, startBlock, count)
type BlockFetcher interface {
	// FetchBlocks fetches a range of blocks starting from the given block number.
	// Returns the fetched blocks as raw bytes and any error encountered.
	FetchBlocks(ctx context.Context, start *uint256.Int, count uint64) (*FetchResult, error)

	// FetchBlocksByHash fetches specific blocks by their hashes.
	FetchBlocksByHash(ctx context.Context, hashes [][]byte) (*FetchResult, error)

	// Start starts the fetcher background processes.
	Start() error

	// Stop stops the fetcher and releases resources.
	Stop() error

	// Metrics returns the fetcher metrics.
	Metrics() *FetcherMetrics
}

// FetchResult holds the result of a fetch operation.
type FetchResult struct {
	// Blocks contains the fetched block data.
	Blocks [][]byte

	// PeerID is the peer that provided the blocks.
	PeerID peer.ID

	// Start is the starting block number of the fetch.
	Start *uint256.Int

	// Count is the number of blocks requested.
	Count uint64

	// Duration is how long the fetch took.
	Duration time.Duration
}

// =============================================================================
// Fetcher Metrics
// =============================================================================

// FetcherMetrics collects metrics for fetch operations.
type FetcherMetrics struct {
	mu sync.RWMutex

	// Fetch operation counts
	fetchesTotal     uint64
	fetchesSucceeded uint64
	fetchesFailed    uint64

	// Block counts
	blocksRequested uint64
	blocksReceived  uint64

	// Timing
	totalFetchTime time.Duration
	batchLatencies []time.Duration

	// Peer tracking
	peerFetches map[peer.ID]uint64
	peerErrors  map[peer.ID]uint64
}

// NewFetcherMetrics creates a new FetcherMetrics instance.
func NewFetcherMetrics() *FetcherMetrics {
	return &FetcherMetrics{
		batchLatencies: make([]time.Duration, 0, 1000),
		peerFetches:    make(map[peer.ID]uint64),
		peerErrors:     make(map[peer.ID]uint64),
	}
}

// RecordFetch records a fetch operation result.
func (m *FetcherMetrics) RecordFetch(pid peer.ID, blocksRequested, blocksReceived uint64, duration time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.fetchesTotal++
	m.blocksRequested += blocksRequested
	m.blocksReceived += blocksReceived
	m.totalFetchTime += duration

	if success {
		m.fetchesSucceeded++
		m.peerFetches[pid]++
	} else {
		m.fetchesFailed++
		m.peerErrors[pid]++
	}

	// Keep last 1000 latencies
	if len(m.batchLatencies) >= 1000 {
		m.batchLatencies = m.batchLatencies[1:]
	}
	m.batchLatencies = append(m.batchLatencies, duration)
}

// SuccessRate returns the fetch success rate.
func (m *FetcherMetrics) SuccessRate() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.fetchesTotal == 0 {
		return 0
	}
	return float64(m.fetchesSucceeded) / float64(m.fetchesTotal)
}

// AverageBatchLatency returns the average batch fetch latency.
func (m *FetcherMetrics) AverageBatchLatency() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.batchLatencies) == 0 {
		return 0
	}

	var total time.Duration
	for _, l := range m.batchLatencies {
		total += l
	}
	return total / time.Duration(len(m.batchLatencies))
}

// BlocksPerSecond returns the average blocks fetched per second.
func (m *FetcherMetrics) BlocksPerSecond() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.totalFetchTime == 0 {
		return 0
	}
	return float64(m.blocksReceived) / m.totalFetchTime.Seconds()
}

// LogStats logs the current fetcher metrics.
func (m *FetcherMetrics) LogStats() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Info("Fetcher metrics",
		"fetches_total", m.fetchesTotal,
		"fetches_succeeded", m.fetchesSucceeded,
		"fetches_failed", m.fetchesFailed,
		"success_rate", fmt.Sprintf("%.2f%%", m.SuccessRate()*100),
		"blocks_requested", m.blocksRequested,
		"blocks_received", m.blocksReceived,
		"avg_batch_latency", m.AverageBatchLatency(),
		"blocks_per_second", fmt.Sprintf("%.2f", m.BlocksPerSecond()),
	)
}

// =============================================================================
// Fetcher Configuration
// =============================================================================

// FetcherConfig holds configuration for the block fetcher.
type FetcherConfig struct {
	// MaxPendingRequests limits concurrent fetch requests.
	MaxPendingRequests int

	// BlockBatchSize is the number of blocks to fetch per request.
	BlockBatchSize uint64

	// RequestTimeout is the timeout for a single fetch request.
	RequestTimeout time.Duration

	// RetryCount is the number of retries for failed requests.
	RetryCount int

	// RetryDelay is the delay between retries.
	RetryDelay time.Duration

	// PeersPerRequest caps percentage of peers to use per request.
	PeersPerRequest float64

	// MinPeers is the minimum number of peers required to start fetching.
	MinPeers int
}

// DefaultFetcherConfig returns the default fetcher configuration.
func DefaultFetcherConfig() *FetcherConfig {
	return &FetcherConfig{
		MaxPendingRequests: 64,
		BlockBatchSize:     128,
		RequestTimeout:     30 * time.Second,
		RetryCount:         3,
		RetryDelay:         1 * time.Second,
		PeersPerRequest:    0.75,
		MinPeers:           3,
	}
}

// =============================================================================
// Basic Fetcher Implementation
// =============================================================================

// BasicFetcher is a simple implementation of the BlockFetcher interface.
// It wraps the existing P2P layer and provides metrics collection.
type BasicFetcher struct {
	p2p        p2p.P2P
	blockchain common.IBlockChain
	config     *FetcherConfig
	metrics    *FetcherMetrics

	ctx    context.Context
	cancel context.CancelFunc

	running int32 // atomic
	mu      sync.RWMutex
}

// NewBasicFetcher creates a new BasicFetcher.
func NewBasicFetcher(
	ctx context.Context,
	p2p p2p.P2P,
	blockchain common.IBlockChain,
	config *FetcherConfig,
) *BasicFetcher {
	if config == nil {
		config = DefaultFetcherConfig()
	}

	ctx, cancel := context.WithCancel(ctx)

	return &BasicFetcher{
		p2p:        p2p,
		blockchain: blockchain,
		config:     config,
		metrics:    NewFetcherMetrics(),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start starts the fetcher.
func (f *BasicFetcher) Start() error {
	if !atomic.CompareAndSwapInt32(&f.running, 0, 1) {
		return fmt.Errorf("fetcher already running")
	}
	log.Info("Block fetcher started")
	return nil
}

// Stop stops the fetcher.
func (f *BasicFetcher) Stop() error {
	if !atomic.CompareAndSwapInt32(&f.running, 1, 0) {
		return fmt.Errorf("fetcher not running")
	}
	f.cancel()
	log.Info("Block fetcher stopped")
	return nil
}

// FetchBlocks fetches a range of blocks.
func (f *BasicFetcher) FetchBlocks(ctx context.Context, start *uint256.Int, count uint64) (*FetchResult, error) {
	if atomic.LoadInt32(&f.running) == 0 {
		return nil, fmt.Errorf("fetcher not running")
	}

	startTime := time.Now()

	// Get best peers
	_, peers := f.p2p.Peers().BestPeers(f.config.MinPeers, f.blockchain.CurrentBlock().Number64())
	if len(peers) == 0 {
		f.metrics.RecordFetch("", count, 0, time.Since(startTime), false)
		return nil, fmt.Errorf("no peers available")
	}

	// Try each peer until one succeeds
	var lastErr error
	for _, pid := range peers {
		blocks, err := f.fetchFromPeer(ctx, pid, start, count)
		if err != nil {
			lastErr = err
			f.metrics.RecordFetch(pid, count, 0, time.Since(startTime), false)
			continue
		}

		duration := time.Since(startTime)
		f.metrics.RecordFetch(pid, count, uint64(len(blocks)), duration, true)

		return &FetchResult{
			Blocks:   blocks,
			PeerID:   pid,
			Start:    start,
			Count:    count,
			Duration: duration,
		}, nil
	}

	return nil, fmt.Errorf("failed to fetch blocks from any peer: %w", lastErr)
}

// FetchBlocksByHash fetches blocks by hash.
func (f *BasicFetcher) FetchBlocksByHash(ctx context.Context, hashes [][]byte) (*FetchResult, error) {
	// TODO: Implement block-by-hash fetching
	return nil, fmt.Errorf("FetchBlocksByHash not implemented")
}

// fetchFromPeer fetches blocks from a specific peer.
func (f *BasicFetcher) fetchFromPeer(ctx context.Context, pid peer.ID, start *uint256.Int, count uint64) ([][]byte, error) {
	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, f.config.RequestTimeout)
	defer cancel()

	// Use existing P2P infrastructure to request blocks
	// This delegates to the actual implementation
	log.Debug("Fetching blocks from peer",
		"peer", pid.String(),
		"start", start.Uint64(),
		"count", count,
	)

	// Note: In a real implementation, this would call the actual P2P block request method
	// For now, we return a placeholder to show the interface works
	// The actual implementation would use SendBodiesByRangeRequest or similar

	return nil, fmt.Errorf("fetch from peer not fully implemented - use existing initialsync")
}

// Metrics returns the fetcher metrics.
func (f *BasicFetcher) Metrics() *FetcherMetrics {
	return f.metrics
}

// =============================================================================
// Instrumented Fetcher (Wrapper with metrics)
// =============================================================================

// InstrumentedFetcher wraps a BlockFetcher with additional instrumentation.
type InstrumentedFetcher struct {
	inner   BlockFetcher
	enabled bool
	metrics *FetcherMetrics
}

// NewInstrumentedFetcher creates a new InstrumentedFetcher.
func NewInstrumentedFetcher(inner BlockFetcher, enabled bool) *InstrumentedFetcher {
	return &InstrumentedFetcher{
		inner:   inner,
		enabled: enabled,
		metrics: NewFetcherMetrics(),
	}
}

// FetchBlocks fetches blocks with instrumentation.
func (f *InstrumentedFetcher) FetchBlocks(ctx context.Context, start *uint256.Int, count uint64) (*FetchResult, error) {
	if !f.enabled {
		return f.inner.FetchBlocks(ctx, start, count)
	}

	startTime := time.Now()
	result, err := f.inner.FetchBlocks(ctx, start, count)
	duration := time.Since(startTime)

	if err != nil {
		f.metrics.RecordFetch("", count, 0, duration, false)
		log.Debug("Block fetch failed",
			"start", start.Uint64(),
			"count", count,
			"duration", duration,
			"error", err,
		)
	} else {
		f.metrics.RecordFetch(result.PeerID, count, uint64(len(result.Blocks)), duration, true)
		log.Debug("Block fetch succeeded",
			"start", start.Uint64(),
			"count", count,
			"blocks_received", len(result.Blocks),
			"duration", duration,
			"peer", result.PeerID,
		)
	}

	return result, err
}

// FetchBlocksByHash fetches blocks by hash with instrumentation.
func (f *InstrumentedFetcher) FetchBlocksByHash(ctx context.Context, hashes [][]byte) (*FetchResult, error) {
	return f.inner.FetchBlocksByHash(ctx, hashes)
}

// Start starts the fetcher.
func (f *InstrumentedFetcher) Start() error {
	return f.inner.Start()
}

// Stop stops the fetcher.
func (f *InstrumentedFetcher) Stop() error {
	return f.inner.Stop()
}

// Metrics returns the combined metrics.
func (f *InstrumentedFetcher) Metrics() *FetcherMetrics {
	return f.metrics
}

// LogStats logs the collected statistics.
func (f *InstrumentedFetcher) LogStats() {
	f.metrics.LogStats()
}

// =============================================================================
// Compile-time interface checks
// =============================================================================

var (
	_ BlockFetcher = (*BasicFetcher)(nil)
	_ BlockFetcher = (*InstrumentedFetcher)(nil)
)

// SyncFetcher is an alias for BlockFetcher for clarity in sync contexts.
// This allows sync.SyncFetcher to be used interchangeably with sync.BlockFetcher.
type SyncFetcher = BlockFetcher
