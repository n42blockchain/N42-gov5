package metrics

import (
	"testing"
	"time"
)

func TestNewAndGetOrCreateCounter(t *testing.T) {
	c := NewCounter("test_reg_counter_1")
	c.AddInt(3)
	c.AddUint64(2)
	if got := c.GetValue(); got != 5 {
		t.Fatalf("GetValue() = %v, want 5", got)
	}
	if got := c.GetValueUint64(); got != 5 {
		t.Fatalf("GetValueUint64() = %v, want 5", got)
	}

	c2 := GetOrCreateCounter("test_reg_counter_2")
	c2.Inc()
	c3 := GetOrCreateCounter("test_reg_counter_2")
	c3.Inc()
	if got := c2.GetValue(); got != 2 {
		t.Fatalf("GetOrCreateCounter should return the same counter: got %v, want 2", got)
	}
}

func TestNewCounterPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid counter name")
		}
	}()
	NewCounter("1invalid")
}

func TestNewAndGetOrCreateGauge(t *testing.T) {
	g := NewGauge("test_reg_gauge_1")
	g.SetInt(7)
	if got := g.GetValue(); got != 7 {
		t.Fatalf("GetValue() = %v, want 7", got)
	}
	g.SetUint32(8)
	if got := g.GetValueUint64(); got != 8 {
		t.Fatalf("GetValueUint64() = %v, want 8", got)
	}
	g.SetUint64(9)
	if got := g.GetValue(); got != 9 {
		t.Fatalf("GetValue() = %v, want 9", got)
	}

	g2 := GetOrCreateGauge("test_reg_gauge_2")
	g2.SetInt(1)
	g3 := GetOrCreateGauge("test_reg_gauge_2")
	if got := g3.GetValue(); got != 1 {
		t.Fatalf("GetOrCreateGauge should return same gauge, got %v", got)
	}
}

func TestNewGaugePanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid gauge name")
		}
	}()
	NewGauge("1invalid")
}

func TestGetOrCreateGaugeVec(t *testing.T) {
	gv := GetOrCreateGaugeVec("test_reg_gaugevec_1", []string{"label1"})
	gv.WithLabelValues("a").Set(42)

	gv2 := GetOrCreateGaugeVec("test_reg_gaugevec_1", []string{"label1"})
	if gv2.WithLabelValues("a") == nil {
		t.Fatal("expected to retrieve the same gauge vec")
	}
}

func TestGetOrCreateGaugeVecPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid gaugevec name")
		}
	}()
	GetOrCreateGaugeVec("1invalid", []string{"a"})
}

func TestNewAndGetOrCreateSummary(t *testing.T) {
	s := NewSummary("test_reg_summary_1")
	s.Observe(1.5)
	s.ObserveDuration(time.Now())

	s2 := GetOrCreateSummary("test_reg_summary_2")
	s3 := GetOrCreateSummary("test_reg_summary_2")
	if s2 == nil || s3 == nil {
		t.Fatal("expected non-nil summaries")
	}
}

func TestNewSummaryPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid summary name")
		}
	}()
	NewSummary("1invalid")
}

func TestNewAndGetOrCreateHistogram(t *testing.T) {
	h := NewHistogram("test_reg_histogram_1")
	h.Observe(0.1)
	h.ObserveDuration(time.Now())

	h2 := GetOrCreateHistogram("test_reg_histogram_2")
	h3 := GetOrCreateHistogram("test_reg_histogram_2")
	if h2 == nil || h3 == nil {
		t.Fatal("expected non-nil histograms")
	}
}

func TestNewHistogramPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid histogram name")
		}
	}()
	NewHistogram("1invalid")
}
