package common

import (
	"strings"
	"testing"
	"time"
)

func TestPrettyDurationString(t *testing.T) {
	d := PrettyDuration(1500 * time.Millisecond)
	s := d.String()
	if s == "" {
		t.Fatal("PrettyDuration.String() should not be empty")
	}

	// A duration whose fractional part is long should be truncated to 4
	// characters after the decimal point by the regexp replace.
	d2 := PrettyDuration(1234567 * time.Nanosecond)
	s2 := d2.String()
	if idx := strings.Index(s2, "."); idx >= 0 {
		frac := s2[idx:]
		// Stop at the first non-digit after the dot (unit suffix).
		end := 1
		for end < len(frac) && frac[end] >= '0' && frac[end] <= '9' {
			end++
		}
		if end-1 > 4 {
			t.Errorf("PrettyDuration fractional part too long: %q", s2)
		}
	}
}

func TestPrettyAgeString(t *testing.T) {
	// Zero / very recent time: should report "0".
	recent := PrettyAge(time.Now())
	if got := recent.String(); got != "0" {
		t.Errorf("PrettyAge(now).String() = %q, want %q", got, "0")
	}

	// An age of a few days should mention "d".
	daysAgo := PrettyAge(time.Now().Add(-3 * 24 * time.Hour))
	if got := daysAgo.String(); !strings.Contains(got, "d") {
		t.Errorf("PrettyAge(3 days ago).String() = %q, want it to contain 'd'", got)
	}

	// An age of multiple years should mention "y".
	yearsAgo := PrettyAge(time.Now().Add(-3 * 365 * 24 * time.Hour))
	if got := yearsAgo.String(); !strings.Contains(got, "y") {
		t.Errorf("PrettyAge(3 years ago).String() = %q, want it to contain 'y'", got)
	}
}
