// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"testing"
	"time"
)

func TestHistoryBackfillIntervalEnv(t *testing.T) {
	t.Setenv("N42_HISTORY_INDEX_INTERVAL", "")
	if got := historyBackfillInterval(); got != 2*time.Second {
		t.Fatalf("default: %v, want 2s", got)
	}
	t.Setenv("N42_HISTORY_INDEX_INTERVAL", "20s")
	if got := historyBackfillInterval(); got != 20*time.Second {
		t.Fatalf("20s: %v", got)
	}
	t.Setenv("N42_HISTORY_INDEX_INTERVAL", "junk")
	if got := historyBackfillInterval(); got != 2*time.Second {
		t.Fatalf("junk: %v, want the default", got)
	}
	t.Setenv("N42_HISTORY_INDEX_INTERVAL", "-5s")
	if got := historyBackfillInterval(); got != 2*time.Second {
		t.Fatalf("negative: %v, want the default", got)
	}
}
