package prometheus

import (
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// UsePrometheusClient controls whether to use the native Prometheus client
// or VictoriaMetrics for metric storage.
const UsePrometheusClient = false

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
	if UsePrometheusClient {
		gauge := defaultSet.GetOrCreateGauge(s)
		return intCounter{gauge}
	}

	counter := vm.GetOrCreateCounter(s, isGauge...)
	DefaultRegistry.Register(s, counter)
	vm.GetDefaultSet().UnregisterMetric(s)
	return counter
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
	if UsePrometheusClient {
		sm := defaultSet.GetOrCreateSummary(s)
		return summary{sm}
	}

	sm := vm.GetOrCreateSummary(s)
	DefaultRegistry.Register(s, sm)
	vm.GetDefaultSet().UnregisterMetric(s)
	return sm
}

// GetOrCreateHistogram returns a histogram registered under the given name.
func GetOrCreateHistogram(s string) prometheus.Histogram {
	return defaultSet.GetOrCreateHistogram(s)
}
