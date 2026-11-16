// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"runtime/metrics"
	"sync"
)

// Per-block GC stamps (r94a, always on): the runtime's GC counters since the
// previous "blockimport phases" line, so GC cycles, assists and pauses can
// be tied to single blocks (run 80: GC mark was ~6% of node0's CPU with
// RSS at GOMEMLIMIT, and nothing per block).
//
//	gcCycles    GC cycles completed
//	gcAssistUs  mark assist CPU (mutator goroutines drafted into marking)
//	gcDedUs     dedicated mark worker CPU
//	gcIdleUs    idle mark worker CPU
//	gcPauseUs   stop-the-world pause CPU (pause wall x GOMAXPROCS)
//	heapLiveMB  heap marked live by the last cycle; heapGoalMB its goal
//
// The CPU classes are runtime estimates that are brought up to date at GC
// cycle ends, so a cycle's assist lands on the block where it ended.
// metrics.Read of these seven samples takes a few microseconds.

var gcStampNames = []string{
	"/gc/cycles/total:gc-cycles",
	"/cpu/classes/gc/mark/assist:cpu-seconds",
	"/cpu/classes/gc/mark/dedicated:cpu-seconds",
	"/cpu/classes/gc/mark/idle:cpu-seconds",
	"/cpu/classes/gc/pause:cpu-seconds",
	"/gc/heap/live:bytes",
	"/gc/heap/goal:bytes",
}

var gcStamp struct {
	mu      sync.Mutex
	samples []metrics.Sample
	prev    [5]float64
	primed  bool
}

func gcSampleValue(s metrics.Sample) float64 {
	switch s.Value.Kind() {
	case metrics.KindUint64:
		return float64(s.Value.Uint64())
	case metrics.KindFloat64:
		return s.Value.Float64()
	}
	return 0
}

// gcBlockFields returns the GC stamps since the previous call.
func gcBlockFields() []interface{} {
	gcStamp.mu.Lock()
	defer gcStamp.mu.Unlock()
	if gcStamp.samples == nil {
		gcStamp.samples = make([]metrics.Sample, len(gcStampNames))
		for i, n := range gcStampNames {
			gcStamp.samples[i].Name = n
		}
	}
	metrics.Read(gcStamp.samples)
	var cur [5]float64
	for i := range cur {
		cur[i] = gcSampleValue(gcStamp.samples[i])
	}
	var d [5]float64
	if gcStamp.primed {
		for i := range d {
			d[i] = cur[i] - gcStamp.prev[i]
		}
	}
	gcStamp.prev, gcStamp.primed = cur, true
	return []interface{}{"gcCycles", int64(d[0]), "gcAssistUs", int64(d[1] * 1e6), "gcDedUs", int64(d[2] * 1e6),
		"gcIdleUs", int64(d[3] * 1e6), "gcPauseUs", int64(d[4] * 1e6),
		"heapLiveMB", int64(gcSampleValue(gcStamp.samples[5])) >> 20, "heapGoalMB", int64(gcSampleValue(gcStamp.samples[6])) >> 20}
}
