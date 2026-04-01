// Package metrics provides stub implementations for Erigon's diagnostics/metrics.
package metrics

type Counter interface {
	Inc()
	Add(float64)
	AddInt(int)
}

type Summary interface {
	ObserveDuration(interface{}) func()
}

type noopCounter struct{}
func (noopCounter) Inc()      {}
func (noopCounter) Add(n float64) {}
func (noopCounter) AddInt(n int) {}

type noopSummary struct{}
func (noopSummary) ObserveDuration(interface{}) func() { return func() {} }

func GetOrCreateCounter(name string) Counter { return noopCounter{} }
func GetOrCreateSummary(name string) Summary { return noopSummary{} }
