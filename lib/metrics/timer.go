package metrics

import (
	"fmt"
	"strings"
	"time"
)

type HistTimer struct {
	Histogram
	start time.Time
	name  string
}

func NewHistTimer(name string) *HistTimer {
	rawName := strings.Split(name, "{")
	return &HistTimer{
		Histogram: GetOrCreateHistogram(name),
		start:     time.Now(),
		name:      rawName[0],
	}
}

func (h *HistTimer) PutSince() {
	h.Histogram.ObserveDuration(h.start)
}

func (h *HistTimer) Tag(pairs ...string) *HistTimer {
	if len(pairs)%2 != 0 {
		pairs = append(pairs, "UNEQUAL_KEY_VALUE_TAGS")
	}

	tags := ""
	if len(pairs) > 0 {
		parts := make([]string, 0, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, pairs[i], pairs[i+1]))
		}
		tags = "{" + strings.Join(parts, ",") + "}"
	}

	return &HistTimer{
		Histogram: GetOrCreateHistogram(h.name + tags),
		start:     time.Now(),
		name:      h.name,
	}
}

func (h *HistTimer) Child(suffix string) *HistTimer {
	suffix = strings.TrimPrefix(suffix, "_")
	return NewHistTimer(h.name + "_" + suffix)
}
