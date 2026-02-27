package metrics

import "time"

type DurationObserver interface {
	ObserveDuration(start time.Time)
}

func secondsSince(start time.Time) float64 {
	return time.Since(start).Seconds()
}
