package metrics

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
)

type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) Printf(format string, v ...interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (c *captureLogger) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// TestLogScaled drives every metric kind through LogScaled's switch so each
// branch (counter/gauge/gaugefloat64/healthcheck/histogram/meter/timer) is
// exercised, then checks the logger received output for each one. The
// registry is driven with a short tick so the test finishes well under the
// 100ms sleep ceiling; LogScaled runs its own goroutine and is intentionally
// left running (time.Tick never stops) since it has no shutdown hook.
func TestLogScaled(t *testing.T) {
	r := metrics.NewRegistry()

	counter := metrics.NewCounter()
	counter.Inc(5)
	_ = r.Register("test.counter", counter)

	gauge := metrics.NewGauge()
	gauge.Update(7)
	_ = r.Register("test.gauge", gauge)

	gaugeF := metrics.NewGaugeFloat64()
	gaugeF.Update(1.5)
	_ = r.Register("test.gaugefloat64", gaugeF)

	hc := metrics.NewHealthcheck(func(h metrics.Healthcheck) { h.Healthy() })
	_ = r.Register("test.healthcheck", hc)

	hist := metrics.NewHistogram(metrics.NewUniformSample(100))
	hist.Update(42)
	_ = r.Register("test.histogram", hist)

	meter := metrics.NewMeter()
	meter.Mark(10)
	_ = r.Register("test.meter", meter)

	timer := metrics.NewTimer()
	timer.Update(3 * time.Millisecond)
	_ = r.Register("test.timer", timer)

	logger := &captureLogger{}
	go LogScaled(r, 5*time.Millisecond, time.Millisecond, logger)

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		lines := logger.snapshot()
		if len(lines) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	lines := logger.snapshot()
	if len(lines) == 0 {
		t.Fatal("LogScaled produced no output")
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"counter test.counter", "gauge test.gauge", "gauge test.gaugefloat64", "healthcheck test.healthcheck", "histogram test.histogram", "meter test.meter", "timer test.timer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, joined)
		}
	}
}

// TestLog verifies the Log convenience wrapper delegates to LogScaled with
// nanosecond scale.
func TestLog(t *testing.T) {
	r := metrics.NewRegistry()
	counter := metrics.NewCounter()
	counter.Inc(1)
	_ = r.Register("test.counter2", counter)

	logger := &captureLogger{}
	go Log(r, 5*time.Millisecond, logger)

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(logger.snapshot()) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(logger.snapshot()) == 0 {
		t.Fatal("Log produced no output")
	}
}
