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
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
)

// =============================================================================
// FetcherMetrics Tests
// =============================================================================

func TestNewFetcherMetrics(t *testing.T) {
	m := NewFetcherMetrics()
	if m == nil {
		t.Fatal("NewFetcherMetrics() returned nil")
	}
	if m.batchLatencies == nil {
		t.Error("batchLatencies not initialized")
	}
	if m.peerFetches == nil {
		t.Error("peerFetches not initialized")
	}
	if m.peerErrors == nil {
		t.Error("peerErrors not initialized")
	}
}

func TestFetcherMetricsRecordFetch(t *testing.T) {
	m := NewFetcherMetrics()
	pid := peer.ID("test-peer")

	// Record successful fetch
	m.RecordFetch(pid, 100, 100, 500*time.Millisecond, true)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.fetchesTotal != 1 {
		t.Errorf("fetchesTotal = %d, want 1", m.fetchesTotal)
	}
	if m.fetchesSucceeded != 1 {
		t.Errorf("fetchesSucceeded = %d, want 1", m.fetchesSucceeded)
	}
	if m.blocksRequested != 100 {
		t.Errorf("blocksRequested = %d, want 100", m.blocksRequested)
	}
	if m.blocksReceived != 100 {
		t.Errorf("blocksReceived = %d, want 100", m.blocksReceived)
	}
	if m.peerFetches[pid] != 1 {
		t.Errorf("peerFetches[%s] = %d, want 1", pid, m.peerFetches[pid])
	}
}

func TestFetcherMetricsRecordFailedFetch(t *testing.T) {
	m := NewFetcherMetrics()
	pid := peer.ID("test-peer")

	// Record failed fetch
	m.RecordFetch(pid, 100, 0, 500*time.Millisecond, false)

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.fetchesFailed != 1 {
		t.Errorf("fetchesFailed = %d, want 1", m.fetchesFailed)
	}
	if m.peerErrors[pid] != 1 {
		t.Errorf("peerErrors[%s] = %d, want 1", pid, m.peerErrors[pid])
	}
}

func TestFetcherMetricsSuccessRate(t *testing.T) {
	m := NewFetcherMetrics()
	pid := peer.ID("test-peer")

	// No fetches yet
	if rate := m.SuccessRate(); rate != 0 {
		t.Errorf("SuccessRate() = %v, want 0", rate)
	}

	// 80% success rate
	for i := 0; i < 80; i++ {
		m.RecordFetch(pid, 10, 10, 100*time.Millisecond, true)
	}
	for i := 0; i < 20; i++ {
		m.RecordFetch(pid, 10, 0, 100*time.Millisecond, false)
	}

	rate := m.SuccessRate()
	if rate < 0.79 || rate > 0.81 {
		t.Errorf("SuccessRate() = %v, want ~0.8", rate)
	}
}

func TestFetcherMetricsAverageBatchLatency(t *testing.T) {
	m := NewFetcherMetrics()
	pid := peer.ID("test-peer")

	// No latencies yet
	if latency := m.AverageBatchLatency(); latency != 0 {
		t.Errorf("AverageBatchLatency() = %v, want 0", latency)
	}

	// Add latencies
	m.RecordFetch(pid, 10, 10, 100*time.Millisecond, true)
	m.RecordFetch(pid, 10, 10, 200*time.Millisecond, true)
	m.RecordFetch(pid, 10, 10, 300*time.Millisecond, true)

	avgLatency := m.AverageBatchLatency()
	expected := 200 * time.Millisecond
	if avgLatency < 190*time.Millisecond || avgLatency > 210*time.Millisecond {
		t.Errorf("AverageBatchLatency() = %v, want ~%v", avgLatency, expected)
	}
}

func TestFetcherMetricsConcurrency(t *testing.T) {
	m := NewFetcherMetrics()
	var wg sync.WaitGroup

	// Concurrent operations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pid := peer.ID("peer-" + string(rune('0'+i%10)))
			m.RecordFetch(pid, 10, 10, 100*time.Millisecond, i%2 == 0)
			_ = m.SuccessRate()
			_ = m.AverageBatchLatency()
			_ = m.BlocksPerSecond()
		}(i)
	}

	wg.Wait()
	t.Log("✓ FetcherMetrics concurrent operations completed without race")
}

// =============================================================================
// FetcherConfig Tests
// =============================================================================

func TestDefaultFetcherConfig(t *testing.T) {
	config := DefaultFetcherConfig()

	if config.MaxPendingRequests != 64 {
		t.Errorf("MaxPendingRequests = %d, want 64", config.MaxPendingRequests)
	}
	if config.BlockBatchSize != 128 {
		t.Errorf("BlockBatchSize = %d, want 128", config.BlockBatchSize)
	}
	if config.RequestTimeout != 30*time.Second {
		t.Errorf("RequestTimeout = %v, want 30s", config.RequestTimeout)
	}
	if config.RetryCount != 3 {
		t.Errorf("RetryCount = %d, want 3", config.RetryCount)
	}
	if config.RetryDelay != 1*time.Second {
		t.Errorf("RetryDelay = %v, want 1s", config.RetryDelay)
	}
	if config.PeersPerRequest != 0.75 {
		t.Errorf("PeersPerRequest = %v, want 0.75", config.PeersPerRequest)
	}
	if config.MinPeers != 3 {
		t.Errorf("MinPeers = %d, want 3", config.MinPeers)
	}
}

// =============================================================================
// BasicFetcher Tests
// =============================================================================

func TestBasicFetcherStartStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := NewBasicFetcher(ctx, nil, nil, nil)

	// Start
	if err := f.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Double start should fail
	if err := f.Start(); err == nil {
		t.Error("Double Start() should return error")
	}

	// Stop
	if err := f.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Double stop should fail
	if err := f.Stop(); err == nil {
		t.Error("Double Stop() should return error")
	}
}

func TestBasicFetcherMetrics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := NewBasicFetcher(ctx, nil, nil, nil)

	metrics := f.Metrics()
	if metrics == nil {
		t.Error("Metrics() returned nil")
	}
}

func TestBasicFetcherNotRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := NewBasicFetcher(ctx, nil, nil, nil)

	// Fetch without starting should fail
	_, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err == nil {
		t.Error("FetchBlocks() should fail when fetcher not running")
	}
}

// =============================================================================
// InstrumentedFetcher Tests
// =============================================================================

type mockFetcher struct {
	fetchBlocksCalled  bool
	fetchByHashCalled  bool
	startCalled        bool
	stopCalled         bool
	shouldFail         bool
	blocksToReturn     [][]byte
}

func (m *mockFetcher) FetchBlocks(ctx context.Context, start *uint256.Int, count uint64) (*FetchResult, error) {
	m.fetchBlocksCalled = true
	if m.shouldFail {
		return nil, errNoPeersAvailable
	}
	return &FetchResult{
		Blocks:   m.blocksToReturn,
		PeerID:   "mock-peer",
		Start:    start,
		Count:    count,
		Duration: 100 * time.Millisecond,
	}, nil
}

func (m *mockFetcher) FetchBlocksByHash(ctx context.Context, hashes [][]byte) (*FetchResult, error) {
	m.fetchByHashCalled = true
	return nil, nil
}

func (m *mockFetcher) Start() error {
	m.startCalled = true
	return nil
}

func (m *mockFetcher) Stop() error {
	m.stopCalled = true
	return nil
}

func (m *mockFetcher) Metrics() *FetcherMetrics {
	return NewFetcherMetrics()
}

var errNoPeersAvailable = fmt.Errorf("no peers available")

func TestInstrumentedFetcherDelegates(t *testing.T) {
	mock := &mockFetcher{
		blocksToReturn: [][]byte{{1, 2, 3}},
	}
	f := NewInstrumentedFetcher(mock, true)

	ctx := context.Background()

	// Test FetchBlocks
	result, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err != nil {
		t.Errorf("FetchBlocks() error = %v", err)
	}
	if !mock.fetchBlocksCalled {
		t.Error("FetchBlocks() did not call inner")
	}
	if result == nil || len(result.Blocks) != 1 {
		t.Error("FetchBlocks() wrong result")
	}

	// Test FetchBlocksByHash
	_, _ = f.FetchBlocksByHash(ctx, nil)
	if !mock.fetchByHashCalled {
		t.Error("FetchBlocksByHash() did not call inner")
	}

	// Test Start/Stop
	_ = f.Start()
	if !mock.startCalled {
		t.Error("Start() did not call inner")
	}
	_ = f.Stop()
	if !mock.stopCalled {
		t.Error("Stop() did not call inner")
	}
}

func TestInstrumentedFetcherDisabled(t *testing.T) {
	mock := &mockFetcher{
		blocksToReturn: [][]byte{{1, 2, 3}},
	}
	f := NewInstrumentedFetcher(mock, false)

	ctx := context.Background()

	// Should still work when disabled
	result, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err != nil {
		t.Errorf("FetchBlocks() error = %v", err)
	}
	if result == nil {
		t.Error("FetchBlocks() returned nil result")
	}
}

func TestInstrumentedFetcherMetricsOnError(t *testing.T) {
	mock := &mockFetcher{
		shouldFail: true,
	}
	f := NewInstrumentedFetcher(mock, true)

	ctx := context.Background()

	_, err := f.FetchBlocks(ctx, uint256.NewInt(0), 10)
	if err == nil {
		t.Error("FetchBlocks() should return error")
	}

	// Metrics should record failure
	metrics := f.Metrics()
	if metrics.SuccessRate() != 0 {
		t.Errorf("SuccessRate() = %v, want 0", metrics.SuccessRate())
	}
}

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestBasicFetcherImplementsBlockFetcher(t *testing.T) {
	ctx := context.Background()
	var _ BlockFetcher = NewBasicFetcher(ctx, nil, nil, nil)
	t.Log("✓ BasicFetcher implements BlockFetcher interface")
}

func TestInstrumentedFetcherImplementsBlockFetcher(t *testing.T) {
	mock := &mockFetcher{}
	var _ BlockFetcher = NewInstrumentedFetcher(mock, true)
	t.Log("✓ InstrumentedFetcher implements BlockFetcher interface")
}

// =============================================================================
// FetchResult Tests
// =============================================================================

func TestFetchResult(t *testing.T) {
	result := &FetchResult{
		Blocks:   [][]byte{{1, 2, 3}, {4, 5, 6}},
		PeerID:   "test-peer",
		Start:    uint256.NewInt(100),
		Count:    2,
		Duration: 500 * time.Millisecond,
	}

	if len(result.Blocks) != 2 {
		t.Errorf("Blocks len = %d, want 2", len(result.Blocks))
	}
	if result.PeerID != "test-peer" {
		t.Errorf("PeerID = %s, want test-peer", result.PeerID)
	}
	if result.Start.Uint64() != 100 {
		t.Errorf("Start = %d, want 100", result.Start.Uint64())
	}
	if result.Count != 2 {
		t.Errorf("Count = %d, want 2", result.Count)
	}
	if result.Duration != 500*time.Millisecond {
		t.Errorf("Duration = %v, want 500ms", result.Duration)
	}
}

