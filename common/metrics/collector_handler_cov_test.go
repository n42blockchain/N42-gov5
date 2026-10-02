package prometheus

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"
	clientprometheus "github.com/prometheus/client_golang/prometheus"
)

// TestHandlerServesAllVMMetricTypes drives Handler's registry.Each loop over
// every *metrics2.* type it switches on, exercising collector.go's
// writeCounter/writeGauge/writeFloatCounter/writeHistogram/writeTimer (and
// their stripLabels/splitLabels helpers) end to end via a real HTTP response.
func TestHandlerServesAllVMMetricTypes(t *testing.T) {
	oldRegisterer := clientprometheus.DefaultRegisterer
	oldGatherer := clientprometheus.DefaultGatherer
	registry := clientprometheus.NewRegistry()
	clientprometheus.DefaultRegisterer = registry
	clientprometheus.DefaultGatherer = registry
	defer func() {
		clientprometheus.DefaultRegisterer = oldRegisterer
		clientprometheus.DefaultGatherer = oldGatherer
	}()
	registerDefaultSetOnce = sync.Once{}

	reg := NewRegistry()

	counter := vm.NewCounter("g41_test_counter")
	counter.Add(5)
	if err := reg.Register("g41_test_counter", counter); err != nil {
		t.Fatalf("Register counter: %v", err)
	}

	gaugeCounter := vm.NewCounter("g41_test_gauge_counter", true)
	gaugeCounter.Set(7)
	if err := reg.Register("g41_test_gauge_counter", gaugeCounter); err != nil {
		t.Fatalf("Register gauge-mode counter: %v", err)
	}

	gauge := vm.NewGauge("g41_test_gauge{label=\"x\"}", func() float64 { return 3.5 })
	if err := reg.Register("g41_test_gauge{label=\"x\"}", gauge); err != nil {
		t.Fatalf("Register gauge: %v", err)
	}

	floatCounter := vm.NewFloatCounter("g41_test_float_counter")
	floatCounter.Add(1.5)
	if err := reg.Register("g41_test_float_counter", floatCounter); err != nil {
		t.Fatalf("Register float counter: %v", err)
	}

	histogram := vm.NewHistogram("g41_test_histogram")
	histogram.Update(10)
	histogram.Update(20)
	if err := reg.Register("g41_test_histogram", histogram); err != nil {
		t.Fatalf("Register histogram: %v", err)
	}

	summary := vm.NewSummary("g41_test_summary")
	summary.Update(0.1)
	summary.Update(0.2)
	if err := reg.Register("g41_test_summary", summary); err != nil {
		t.Fatalf("Register summary: %v", err)
	}

	// An unknown metric type should hit the default branch (logged, skipped)
	// rather than panicking the handler.
	if err := reg.Register("g41_test_unknown", "not-a-metric"); err != nil {
		t.Fatalf("Register unknown type: %v", err)
	}

	h := Handler(reg)
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("ServeHTTP status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"g41_test_counter",
		"g41_test_gauge_counter",
		"g41_test_gauge",
		"g41_test_float_counter",
		"g41_test_histogram",
		"g41_test_summary",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected handler output to contain %q; got:\n%s", want, body)
		}
	}
}

func TestStripAndSplitLabels(t *testing.T) {
	if got := stripLabels("foo"); got != "foo" {
		t.Errorf("stripLabels(foo) = %q, want foo", got)
	}
	if got := stripLabels(`foo{bar="baz"}`); got != "foo" {
		t.Errorf("stripLabels with labels = %q, want foo", got)
	}

	name, labels := splitLabels("foo")
	if name != "foo" || labels != "" {
		t.Errorf("splitLabels(foo) = (%q, %q), want (foo, \"\")", name, labels)
	}
	name, labels = splitLabels(`foo{bar="baz"}`)
	if name != "foo" || labels != `{bar="baz"}` {
		t.Errorf("splitLabels with labels = (%q, %q)", name, labels)
	}
}
