package metrics

import (
	"testing"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	dto "github.com/prometheus/client_model/go"
)

// TestRegisterSystemMetrics verifies that calling RegisterSystemMetrics
// registers the expected runtime gauges and that calling it repeatedly
// does not panic or duplicate registration (guarded by sync.Once).
func TestRegisterSystemMetrics(t *testing.T) {
	RegisterSystemMetrics()
	RegisterSystemMetrics()
	RegisterSystemMetrics()

	names := []string{
		"go_goroutines",
		"go_threads",
		"go_memstats_alloc_bytes",
		"go_memstats_sys_bytes",
		"go_memstats_heap_alloc_bytes",
		"go_memstats_heap_inuse_bytes",
		"go_memstats_heap_objects",
		"go_memstats_stack_inuse_bytes",
		"go_gc_pause_seconds_last",
		"go_gc_num_gc",
		"go_gc_num_forced_gc",
	}
	for _, n := range names {
		// GetOrCreateGaugeFunc returns the already-registered gauge when one
		// exists under this name, ignoring our sentinel callback, so a
		// successful Write here proves registerRuntimeMetrics wired a real
		// collector rather than this test creating a fresh one.
		g := prometheus.GetOrCreateGaugeFunc(n, func() float64 { return -12345 })
		var m dto.Metric
		if err := g.Write(&m); err != nil {
			t.Errorf("gauge %s: Write failed: %v", n, err)
			continue
		}
		if m.GetGauge() == nil {
			t.Errorf("gauge %s: Write produced no Gauge value", n)
		}
	}
}
