package prometheus

import (
	"sync"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const UsePrometheusClient = false

// per-metric one-time registration. Without this cache, repeated calls to
// GetOrCreateSummary/Counter for the same name would:
//
//  1. Re-fire vm.GetOrCreateXxx — but the previous call's
//     UnregisterMetric removed it from VM's set, so VM treats every
//     call as a fresh metric. Creating a new Summary spawns a new
//     summariesSwapCron goroutine (summary.go:219) — one leak per
//     hot-path call. At ~6 RPC req/s we observed 1300 goroutines/s
//     locally, locking up in 3 minutes.
//
//  2. DefaultRegistry.Register returns DuplicateMetric on the second
//     call (registry.go:120-122) but the caller ignores the error — so
//     the registry keeps the FIRST Summary while updates go to a NEW
//     one, producing stale /metrics output.
//
// Caching with sync.Once per name fixes both: register exactly once,
// always return the same instance.
type summaryEntry struct {
	once sync.Once
	sm   Summary
}

type counterEntry struct {
	once sync.Once
	c    Counter
}

var (
	summaryCache sync.Map // string → *summaryEntry
	counterCache sync.Map // string → *counterEntry
)

type Summary interface {
	UpdateDuration(time.Time)
}

type Counter interface {
	Inc()
	Dec()
	Add(n int)
	Set(n uint64)
	Get() uint64
}

type intCounter struct {
	prometheus.Gauge
}

func (c intCounter) Add(n int) {
	c.Gauge.Add(float64(n))
}

func (c intCounter) Set(n uint64) {
	c.Gauge.Set(float64(n))
}

func (c intCounter) Get() uint64 {
	var m dto.Metric
	c.Gauge.Write(&m)
	return uint64(m.GetGauge().GetValue())
}

func GetOrCreateCounter(s string, isGauge ...bool) Counter {
	e, _ := counterCache.LoadOrStore(s, &counterEntry{})
	entry := e.(*counterEntry)
	entry.once.Do(func() {
		if UsePrometheusClient {
			counter := defaultSet.GetOrCreateGauge(s)
			entry.c = intCounter{counter}
			return
		}
		counter := vm.GetOrCreateCounter(s, isGauge...)
		DefaultRegistry.Register(s, counter)
		vm.GetDefaultSet().UnregisterMetric(s)
		entry.c = counter
	})
	return entry.c
}

func GetOrCreateGaugeFunc(s string, f func() float64) prometheus.GaugeFunc {
	return defaultSet.GetOrCreateGaugeFunc(s, f)
}

type summary struct {
	prometheus.Summary
}

func (sm summary) UpdateDuration(startTime time.Time) {
	sm.Observe(time.Since(startTime).Seconds())
}

func GetOrCreateSummary(s string) Summary {
	e, _ := summaryCache.LoadOrStore(s, &summaryEntry{})
	entry := e.(*summaryEntry)
	entry.once.Do(func() {
		if UsePrometheusClient {
			sm := defaultSet.GetOrCreateSummary(s)
			entry.sm = summary{sm}
			return
		}
		sm := vm.GetOrCreateSummary(s)
		DefaultRegistry.Register(s, sm)
		vm.GetDefaultSet().UnregisterMetric(s)
		entry.sm = sm
	})
	return entry.sm
}

func GetOrCreateHistogram(s string) prometheus.Histogram {
	return defaultSet.GetOrCreateHistogram(s)
}
