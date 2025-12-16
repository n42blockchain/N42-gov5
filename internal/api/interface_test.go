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

package api

import (
	"sync"
	"testing"
	"time"
)

// =============================================================================
// RPCMetrics Tests
// =============================================================================

func TestNewRPCMetrics(t *testing.T) {
	m := NewRPCMetrics()
	if m == nil {
		t.Fatal("NewRPCMetrics() returned nil")
	}
	if m.methodCalls == nil {
		t.Error("methodCalls not initialized")
	}
	if m.methodErrors == nil {
		t.Error("methodErrors not initialized")
	}
	if m.methodLatency == nil {
		t.Error("methodLatency not initialized")
	}
	if m.lastCallTime == nil {
		t.Error("lastCallTime not initialized")
	}
}

func TestRPCMetricsRecordMethod(t *testing.T) {
	m := NewRPCMetrics()

	// Record successful method
	m.RecordMethod("eth_blockNumber", 10*time.Millisecond, true)

	m.mu.RLock()
	if m.methodCalls["eth_blockNumber"] != 1 {
		t.Errorf("methodCalls = %d, want 1", m.methodCalls["eth_blockNumber"])
	}
	if m.methodErrors["eth_blockNumber"] != 0 {
		t.Errorf("methodErrors = %d, want 0", m.methodErrors["eth_blockNumber"])
	}
	if m.totalCalls != 1 {
		t.Errorf("totalCalls = %d, want 1", m.totalCalls)
	}
	m.mu.RUnlock()

	// Record failed method
	m.RecordMethod("eth_getBalance", 20*time.Millisecond, false)

	m.mu.RLock()
	if m.methodErrors["eth_getBalance"] != 1 {
		t.Errorf("methodErrors = %d, want 1", m.methodErrors["eth_getBalance"])
	}
	if m.totalErrors != 1 {
		t.Errorf("totalErrors = %d, want 1", m.totalErrors)
	}
	m.mu.RUnlock()
}

func TestRPCMetricsMethodStats(t *testing.T) {
	m := NewRPCMetrics()

	// Record multiple calls with varying latencies
	latencies := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
		60 * time.Millisecond,
		70 * time.Millisecond,
		80 * time.Millisecond,
		90 * time.Millisecond,
		100 * time.Millisecond,
	}

	for _, l := range latencies {
		m.RecordMethod("eth_call", l, true)
	}
	m.RecordMethod("eth_call", 5*time.Millisecond, false) // One error

	calls, errors, p50, p95 := m.MethodStats("eth_call")

	if calls != 11 {
		t.Errorf("calls = %d, want 11", calls)
	}
	if errors != 1 {
		t.Errorf("errors = %d, want 1", errors)
	}
	// P50 should be around 50ms
	if p50 < 40*time.Millisecond || p50 > 60*time.Millisecond {
		t.Errorf("p50 = %v, want ~50ms", p50)
	}
	// P95 should be around 100ms
	if p95 < 80*time.Millisecond || p95 > 110*time.Millisecond {
		t.Errorf("p95 = %v, want ~100ms", p95)
	}
}

func TestRPCMetricsGlobalStats(t *testing.T) {
	m := NewRPCMetrics()

	m.RecordMethod("eth_blockNumber", 10*time.Millisecond, true)
	m.RecordMethod("eth_getBalance", 20*time.Millisecond, true)
	m.RecordMethod("eth_call", 30*time.Millisecond, false)

	totalCalls, totalErrors, uptime := m.GlobalStats()

	if totalCalls != 3 {
		t.Errorf("totalCalls = %d, want 3", totalCalls)
	}
	if totalErrors != 1 {
		t.Errorf("totalErrors = %d, want 1", totalErrors)
	}
	if uptime < 0 {
		t.Errorf("uptime = %v, want > 0", uptime)
	}
}

func TestRPCMetricsTopMethods(t *testing.T) {
	m := NewRPCMetrics()

	// eth_blockNumber called 5 times
	for i := 0; i < 5; i++ {
		m.RecordMethod("eth_blockNumber", 10*time.Millisecond, true)
	}
	// eth_getBalance called 3 times
	for i := 0; i < 3; i++ {
		m.RecordMethod("eth_getBalance", 10*time.Millisecond, true)
	}
	// eth_call called 1 time
	m.RecordMethod("eth_call", 10*time.Millisecond, true)

	top := m.TopMethods(2)

	if len(top) != 2 {
		t.Fatalf("TopMethods(2) len = %d, want 2", len(top))
	}
	if top[0].Method != "eth_blockNumber" || top[0].Calls != 5 {
		t.Errorf("top[0] = %v, want eth_blockNumber with 5 calls", top[0])
	}
	if top[1].Method != "eth_getBalance" || top[1].Calls != 3 {
		t.Errorf("top[1] = %v, want eth_getBalance with 3 calls", top[1])
	}
}

func TestRPCMetricsConcurrency(t *testing.T) {
	m := NewRPCMetrics()
	var wg sync.WaitGroup

	// Concurrent method recording
	methods := []string{"eth_blockNumber", "eth_getBalance", "eth_call", "eth_sendRawTransaction", "eth_getLogs"}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			method := methods[i%len(methods)]
			m.RecordMethod(method, time.Duration(i)*time.Millisecond, i%3 != 0)
			_, _, _, _ = m.MethodStats(method)
			_, _, _ = m.GlobalStats()
			_ = m.TopMethods(3)
		}(i)
	}

	wg.Wait()

	totalCalls, _, _ := m.GlobalStats()
	if totalCalls != 100 {
		t.Errorf("totalCalls = %d, want 100", totalCalls)
	}
	t.Log("✓ RPCMetrics concurrent operations completed without race")
}

func TestRPCMetricsEmptyStats(t *testing.T) {
	m := NewRPCMetrics()

	// Stats for non-existent method
	calls, errors, p50, p95 := m.MethodStats("non_existent")

	if calls != 0 || errors != 0 || p50 != 0 || p95 != 0 {
		t.Errorf("empty stats should be 0, got calls=%d, errors=%d, p50=%v, p95=%v", calls, errors, p50, p95)
	}

	// TopMethods on empty metrics
	top := m.TopMethods(5)
	if len(top) != 0 {
		t.Errorf("TopMethods on empty = %d, want 0", len(top))
	}
}

// =============================================================================
// RouterConfig Tests
// =============================================================================

func TestDefaultRouterConfig(t *testing.T) {
	config := DefaultRouterConfig()

	if !config.EnableEth {
		t.Error("EnableEth should be true")
	}
	if !config.EnableN42 {
		t.Error("EnableN42 should be true")
	}
	if !config.EnableDebug {
		t.Error("EnableDebug should be true")
	}
	if !config.EnableNet {
		t.Error("EnableNet should be true")
	}
	if !config.EnableWeb3 {
		t.Error("EnableWeb3 should be true")
	}
	if !config.EnableTxPool {
		t.Error("EnableTxPool should be true")
	}
	if config.MetricsLogInterval != 60*time.Second {
		t.Errorf("MetricsLogInterval = %v, want 60s", config.MetricsLogInterval)
	}
}

// =============================================================================
// NamespaceConfig Tests
// =============================================================================

func TestNamespaceConfigToJSONRPCAPI(t *testing.T) {
	nc := &NamespaceConfig{
		Name:    "test",
		Version: "1.0",
		Service: struct{}{},
		Public:  true,
	}

	api := nc.ToJSONRPCAPI()

	if api.Namespace != "test" {
		t.Errorf("Namespace = %s, want test", api.Namespace)
	}
	if api.Service == nil {
		t.Error("Service should not be nil")
	}
}

// =============================================================================
// Golden Sample Tests
// =============================================================================

func TestGoldenSampleP50P95Calculation(t *testing.T) {
	m := NewRPCMetrics()

	// Record 100 latencies from 1ms to 100ms
	for i := 1; i <= 100; i++ {
		m.RecordMethod("test_method", time.Duration(i)*time.Millisecond, true)
	}

	_, _, p50, p95 := m.MethodStats("test_method")

	// P50 should be 50ms (50th percentile)
	expectedP50 := 50 * time.Millisecond
	if p50 < expectedP50-5*time.Millisecond || p50 > expectedP50+5*time.Millisecond {
		t.Errorf("p50 = %v, want ~%v", p50, expectedP50)
	}

	// P95 should be 95ms (95th percentile)
	expectedP95 := 95 * time.Millisecond
	if p95 < expectedP95-5*time.Millisecond || p95 > expectedP95+5*time.Millisecond {
		t.Errorf("p95 = %v, want ~%v", p95, expectedP95)
	}
}

