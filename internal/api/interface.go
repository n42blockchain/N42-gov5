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
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/log"
)

// =============================================================================
// RPC Method Metrics
// =============================================================================

// RPCMetrics collects metrics for RPC method calls.
// This enables monitoring of RPC method latency and success rates.
type RPCMetrics struct {
	mu sync.RWMutex

	// Method-level metrics
	methodCalls   map[string]uint64
	methodErrors  map[string]uint64
	methodLatency map[string][]time.Duration
	lastCallTime  map[string]time.Time

	// Global counters
	totalCalls  uint64
	totalErrors uint64
	startTime   time.Time
}

// NewRPCMetrics creates a new RPCMetrics instance.
func NewRPCMetrics() *RPCMetrics {
	return &RPCMetrics{
		methodCalls:   make(map[string]uint64),
		methodErrors:  make(map[string]uint64),
		methodLatency: make(map[string][]time.Duration),
		lastCallTime:  make(map[string]time.Time),
		startTime:     time.Now(),
	}
}

// RecordMethod records a method call with its latency and success status.
func (m *RPCMetrics) RecordMethod(method string, latency time.Duration, success bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.methodCalls[method]++
	m.totalCalls++
	m.lastCallTime[method] = time.Now()

	if !success {
		m.methodErrors[method]++
		m.totalErrors++
	}

	// Keep last 1000 latencies per method for percentile calculation
	if m.methodLatency[method] == nil {
		m.methodLatency[method] = make([]time.Duration, 0, 1000)
	}
	if len(m.methodLatency[method]) >= 1000 {
		m.methodLatency[method] = m.methodLatency[method][1:]
	}
	m.methodLatency[method] = append(m.methodLatency[method], latency)

	// Log slow methods
	if latency > 100*time.Millisecond {
		log.Debug("Slow RPC method",
			"method", method,
			"latency", latency,
			"success", success,
		)
	}
}

// MethodStats returns statistics for a specific method.
func (m *RPCMetrics) MethodStats(method string) (calls uint64, errors uint64, p50, p95 time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	calls = m.methodCalls[method]
	errors = m.methodErrors[method]

	latencies := m.methodLatency[method]
	if len(latencies) == 0 {
		return
	}

	// Sort latencies for percentile calculation
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50Index := len(sorted) / 2
	p95Index := len(sorted) * 95 / 100
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}

	p50 = sorted[p50Index]
	p95 = sorted[p95Index]
	return
}

// GlobalStats returns global statistics.
func (m *RPCMetrics) GlobalStats() (totalCalls, totalErrors uint64, uptime time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalCalls, m.totalErrors, time.Since(m.startTime)
}

// TopMethods returns the top N most called methods.
func (m *RPCMetrics) TopMethods(n int) []MethodStat {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]MethodStat, 0, len(m.methodCalls))
	for method, calls := range m.methodCalls {
		stats = append(stats, MethodStat{
			Method: method,
			Calls:  calls,
			Errors: m.methodErrors[method],
		})
	}

	sort.Slice(stats, func(i, j int) bool { return stats[i].Calls > stats[j].Calls })

	if n > len(stats) {
		n = len(stats)
	}
	return stats[:n]
}

// LogStats logs all collected statistics.
func (m *RPCMetrics) LogStats() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	totalCalls, totalErrors, uptime := m.totalCalls, m.totalErrors, time.Since(m.startTime)

	errorRate := float64(0)
	if totalCalls > 0 {
		errorRate = float64(totalErrors) / float64(totalCalls) * 100
	}

	log.Info("RPC metrics summary",
		"total_calls", totalCalls,
		"total_errors", totalErrors,
		"error_rate", fmt.Sprintf("%.2f%%", errorRate),
		"uptime", uptime,
	)

	// Log top 5 methods
	for i, stat := range m.TopMethods(5) {
		calls, errors, p50, p95 := stat.Calls, stat.Errors, time.Duration(0), time.Duration(0)
		latencies := m.methodLatency[stat.Method]
		if len(latencies) > 0 {
			sorted := make([]time.Duration, len(latencies))
			copy(sorted, latencies)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			p50 = sorted[len(sorted)/2]
			p95Index := len(sorted) * 95 / 100
			if p95Index >= len(sorted) {
				p95Index = len(sorted) - 1
			}
			p95 = sorted[p95Index]
		}

		log.Info(fmt.Sprintf("RPC method #%d", i+1),
			"method", stat.Method,
			"calls", calls,
			"errors", errors,
			"p50", p50,
			"p95", p95,
		)
	}
}

// MethodStat holds statistics for a single method.
type MethodStat struct {
	Method string
	Calls  uint64
	Errors uint64
}

