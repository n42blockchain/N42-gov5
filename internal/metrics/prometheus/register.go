package prometheus

import (
	"sync"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// UsePrometheusClient controls whether to use the native Prometheus client
// or VictoriaMetrics for metric storage.
const UsePrometheusClient = false

// per-metric one-time registration cache. See common/metrics/register.go
// for the goroutine-leak and stale-data analysis this prevents.
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

// Summary wraps a duration-tracking metric.
type Summary interface {
	UpdateDuration(time.Time)
}

// Counter provides integer counter operations compatible with both
// Prometheus and VictoriaMetrics backends.
type Counter interface {
	Inc()
	Dec()
	Add(n int)
	Set(n uint64)
	Get() uint64
}

// intCounter adapts a prometheus.Gauge to the Counter interface.
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

// GetOrCreateCounter returns a counter registered under the given name,
// creating it if necessary.
func GetOrCreateCounter(s string, isGauge ...bool) Counter {
	e, _ := counterCache.LoadOrStore(s, &counterEntry{})
	entry := e.(*counterEntry)
	entry.once.Do(func() {
		if UsePrometheusClient {
			gauge := defaultSet.GetOrCreateGauge(s)
			entry.c = intCounter{gauge}
			return
		}
		counter := vm.GetOrCreateCounter(s, isGauge...)
		DefaultRegistry.Register(s, counter)
		vm.GetDefaultSet().UnregisterMetric(s)
		entry.c = counter
	})
	return entry.c
}

// GetOrCreateGaugeFunc returns a gauge func registered under the given name.
func GetOrCreateGaugeFunc(s string, f func() float64) prometheus.GaugeFunc {
	return defaultSet.GetOrCreateGaugeFunc(s, f)
}

// summary adapts a prometheus.Summary to the Summary interface.
type summary struct {
	prometheus.Summary
}

func (sm summary) UpdateDuration(startTime time.Time) {
	sm.Observe(time.Since(startTime).Seconds())
}

// GetOrCreateSummary returns a summary registered under the given name,
// creating it if necessary.
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

// GetOrCreateHistogram returns a histogram registered under the given name.
func GetOrCreateHistogram(s string) prometheus.Histogram {
	return defaultSet.GetOrCreateHistogram(s)
}
