package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Histogram interface {
	prometheus.Histogram
	DurationObserver
}

type histogram struct {
	prometheus.Histogram
}

func (h *histogram) ObserveDuration(start time.Time) {
	h.Observe(secondsSince(start))
}
