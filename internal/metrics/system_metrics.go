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
// Process-level system metrics. Collects runtime memory stats, GC
// pause histograms, goroutine counts and CPU load via Go's runtime
// package, exposing them as Prometheus gauges the node publishes on
// the /debug/metrics endpoint.

package metrics

import (
	"runtime"
	"runtime/debug"
	"sync"

	prometheus "github.com/n42blockchain/N42/common/metrics"
)

var registerOnce sync.Once

// RegisterSystemMetrics registers Go runtime and system-level metrics.
// Safe to call multiple times; only the first call registers metrics.
func RegisterSystemMetrics() {
	registerOnce.Do(func() {
		registerRuntimeMetrics()
	})
}

func registerRuntimeMetrics() {
	// Goroutine count.
	prometheus.GetOrCreateGaugeFunc("go_goroutines", func() float64 {
		return float64(runtime.NumGoroutine())
	})

	// Thread count.
	prometheus.GetOrCreateGaugeFunc("go_threads", func() float64 {
		n, _ := runtime.ThreadCreateProfile(nil)
		return float64(n)
	})

	// Memory statistics.
	prometheus.GetOrCreateGaugeFunc("go_memstats_alloc_bytes", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Alloc)
	})
	prometheus.GetOrCreateGaugeFunc("go_memstats_sys_bytes", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Sys)
	})
	prometheus.GetOrCreateGaugeFunc("go_memstats_heap_alloc_bytes", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc)
	})
	prometheus.GetOrCreateGaugeFunc("go_memstats_heap_inuse_bytes", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapInuse)
	})
	prometheus.GetOrCreateGaugeFunc("go_memstats_heap_objects", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapObjects)
	})
	prometheus.GetOrCreateGaugeFunc("go_memstats_stack_inuse_bytes", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.StackInuse)
	})

	// GC statistics.
	prometheus.GetOrCreateGaugeFunc("go_gc_pause_seconds_last", func() float64 {
		var stats debug.GCStats
		debug.ReadGCStats(&stats)
		if len(stats.Pause) == 0 {
			return 0
		}
		return stats.Pause[0].Seconds()
	})
	prometheus.GetOrCreateGaugeFunc("go_gc_num_gc", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.NumGC)
	})
	prometheus.GetOrCreateGaugeFunc("go_gc_num_forced_gc", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.NumForcedGC)
	})
}
