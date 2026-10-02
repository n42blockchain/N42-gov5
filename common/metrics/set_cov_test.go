package prometheus

import (
	"testing"

	clientprometheus "github.com/prometheus/client_golang/prometheus"
)

func TestSetNewAndGetOrCreateCounter(t *testing.T) {
	s := NewSet()
	c, err := s.NewCounter("g41_set_counter")
	if err != nil {
		t.Fatalf("NewCounter error: %v", err)
	}
	c.Inc()

	got := s.GetOrCreateCounter("g41_set_counter")
	if got != c {
		t.Fatal("GetOrCreateCounter should return the already-registered counter")
	}

	fresh := s.GetOrCreateCounter("g41_set_counter_fresh")
	if fresh == nil {
		t.Fatal("GetOrCreateCounter should create a new counter when missing")
	}
}

func TestSetNewAndGetOrCreateGauge(t *testing.T) {
	s := NewSet()
	g, err := s.NewGauge("g41_set_gauge")
	if err != nil {
		t.Fatalf("NewGauge error: %v", err)
	}
	g.Set(1.0)

	got := s.GetOrCreateGauge("g41_set_gauge")
	if got != g {
		t.Fatal("GetOrCreateGauge should return the already-registered gauge")
	}
}

func TestSetNewAndGetOrCreateGaugeFunc(t *testing.T) {
	s := NewSet()
	gf, err := s.NewGaugeFunc("g41_set_gaugefunc", func() float64 { return 2.0 })
	if err != nil {
		t.Fatalf("NewGaugeFunc error: %v", err)
	}
	if gf == nil {
		t.Fatal("NewGaugeFunc returned nil")
	}

	got := s.GetOrCreateGaugeFunc("g41_set_gaugefunc", func() float64 { return 2.0 })
	if got != gf {
		t.Fatal("GetOrCreateGaugeFunc should return the already-registered gauge")
	}

	fresh := s.GetOrCreateGaugeFunc("g41_set_gaugefunc_fresh", func() float64 { return 3.0 })
	if fresh == nil {
		t.Fatal("GetOrCreateGaugeFunc should create when missing")
	}
}

func TestSetNewAndGetOrCreateHistogram(t *testing.T) {
	s := NewSet()
	h, err := s.NewHistogram("g41_set_histogram")
	if err != nil {
		t.Fatalf("NewHistogram error: %v", err)
	}
	h.Observe(1.5)

	got := s.GetOrCreateHistogram("g41_set_histogram")
	if got != h {
		t.Fatal("GetOrCreateHistogram should return the already-registered histogram")
	}
}

func TestSetNewAndGetOrCreateSummary(t *testing.T) {
	s := NewSet()
	sm, err := s.NewSummary("g41_set_summary")
	if err != nil {
		t.Fatalf("NewSummary error: %v", err)
	}
	sm.Observe(0.5)

	got := s.GetOrCreateSummary("g41_set_summary")
	if got != sm {
		t.Fatal("GetOrCreateSummary should return the already-registered summary")
	}

	got2 := s.GetOrCreateSummaryExt("g41_set_summary", defaultSummaryWindow, defaultSummaryQuantiles)
	if got2 != sm {
		t.Fatal("GetOrCreateSummaryExt should return the already-registered summary")
	}
}

func TestSetInvalidNamePanicsOnGetOrCreate(t *testing.T) {
	s := NewSet()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid metric name")
		}
	}()
	s.GetOrCreateCounter("")
}

func TestSetRegisterMetricDuplicatePanics(t *testing.T) {
	s := NewSet()
	if _, err := s.NewCounter("g41_set_dup"); err != nil {
		t.Fatalf("NewCounter error: %v", err)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic registering a duplicate metric name")
		}
	}()
	_, _ = s.NewCounter("g41_set_dup")
}

func TestSetUnregisterMetric(t *testing.T) {
	s := NewSet()
	if _, err := s.NewCounter("g41_set_unreg"); err != nil {
		t.Fatalf("NewCounter error: %v", err)
	}
	if !s.UnregisterMetric("g41_set_unreg") {
		t.Fatal("expected UnregisterMetric to find and remove the metric")
	}
	if s.UnregisterMetric("g41_set_unreg") {
		t.Fatal("expected a second UnregisterMetric to report not found")
	}
}

func TestSetUnregisterAllMetricsAndListNames(t *testing.T) {
	s := NewSet()
	if _, err := s.NewCounter("g41_set_all_a"); err != nil {
		t.Fatalf("NewCounter error: %v", err)
	}
	if _, err := s.NewGauge("g41_set_all_b"); err != nil {
		t.Fatalf("NewGauge error: %v", err)
	}

	names := s.ListMetricNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 metric names, got %d: %v", len(names), names)
	}

	s.UnregisterAllMetrics()
	if names := s.ListMetricNames(); len(names) != 0 {
		t.Fatalf("expected no metrics after UnregisterAllMetrics, got %v", names)
	}
}

func TestSetDescribeAndCollect(t *testing.T) {
	s := NewSet()
	if _, err := s.NewCounter("g41_set_describe"); err != nil {
		t.Fatalf("NewCounter error: %v", err)
	}

	descCh := make(chan *clientprometheus.Desc, 4)
	go func() {
		s.Describe(descCh)
		close(descCh)
	}()
	count := 0
	for range descCh {
		count++
	}
	if count == 0 {
		t.Fatal("expected at least one description")
	}

	metricCh := make(chan clientprometheus.Metric, 4)
	go func() {
		s.Collect(metricCh)
		close(metricCh)
	}()
	count = 0
	for range metricCh {
		count++
	}
	if count == 0 {
		t.Fatal("expected at least one collected metric")
	}
}
