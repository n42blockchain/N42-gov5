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
//
// RPC method metrics collection: calls, errors, latency percentiles.
// MethodStat records per-method call and error counts. RPCMetrics
// protects concurrent writers with an RWMutex and maintains per-method
// latency histories, last-call timestamps, and total counters for
// dashboards that expose hot and cold methods across namespaces.

package api

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/log"
)

// MethodStat holds statistics for a single RPC method.
type MethodStat struct {
	Method string
	Calls  uint64
	Errors uint64
}

// RPCMetrics collects metrics for RPC method calls, including
// per-method call counts, error counts, and latency percentiles.
type RPCMetrics struct {
	mu sync.RWMutex

	methodCalls   map[string]uint64
	methodErrors  map[string]uint64
	methodLatency map[string][]time.Duration
	lastCallTime  map[string]time.Time

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

const maxLatencySamples = 1000

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

	if m.methodLatency[method] == nil {
		m.methodLatency[method] = make([]time.Duration, 0, maxLatencySamples)
	}
	if len(m.methodLatency[method]) >= maxLatencySamples {
		m.methodLatency[method] = m.methodLatency[method][1:]
	}
	m.methodLatency[method] = append(m.methodLatency[method], latency)

	if latency > 100*time.Millisecond {
		log.Debug("Slow RPC method",
			"method", method,
			"latency", latency,
			"success", success,
		)
	}
}

// sortedLatencies returns a sorted copy of the latency samples for the given method.
// Caller must hold at least a read lock on m.mu.
func (m *RPCMetrics) sortedLatencies(method string) []time.Duration {
	latencies := m.methodLatency[method]
	if len(latencies) == 0 {
		return nil
	}
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

// percentiles returns p50 and p95 from a pre-sorted slice of durations.
func percentiles(sorted []time.Duration) (p50, p95 time.Duration) {
	if len(sorted) == 0 {
		return 0, 0
	}
	p50 = sorted[len(sorted)/2]
	p95Index := len(sorted) * 95 / 100
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	p95 = sorted[p95Index]
	return p50, p95
}

// MethodStats returns statistics for a specific method.
func (m *RPCMetrics) MethodStats(method string) (calls uint64, errors uint64, p50, p95 time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	calls = m.methodCalls[method]
	errors = m.methodErrors[method]
	p50, p95 = percentiles(m.sortedLatencies(method))
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
	return m.topMethodsLocked(n)
}

// topMethodsLocked returns the top N most called methods.
// Caller must hold at least a read lock on m.mu.
func (m *RPCMetrics) topMethodsLocked(n int) []MethodStat {
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

	uptime := time.Since(m.startTime)
	errorRate := float64(0)
	if m.totalCalls > 0 {
		errorRate = float64(m.totalErrors) / float64(m.totalCalls) * 100
	}

	log.Info("RPC metrics summary",
		"total_calls", m.totalCalls,
		"total_errors", m.totalErrors,
		"error_rate", fmt.Sprintf("%.2f%%", errorRate),
		"uptime", uptime,
	)

	for i, stat := range m.topMethodsLocked(5) {
		p50, p95 := percentiles(m.sortedLatencies(stat.Method))
		log.Info(fmt.Sprintf("RPC method #%d", i+1),
			"method", stat.Method,
			"calls", stat.Calls,
			"errors", stat.Errors,
			"p50", p50,
			"p95", p95,
		)
	}
}
