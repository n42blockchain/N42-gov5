package sync

import (
	"testing"
	"time"
)

func TestParseBlockDecodeReusePool(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "on"}
	for _, v := range truthy {
		if !parseBlockDecodeReusePool(v) {
			t.Fatalf("parseBlockDecodeReusePool(%q) = false, want true", v)
		}
	}
	falsy := []string{"", "0", "false", "no", "off", "garbage"}
	for _, v := range falsy {
		if parseBlockDecodeReusePool(v) {
			t.Fatalf("parseBlockDecodeReusePool(%q) = true, want false", v)
		}
	}
}

func TestParseDeferredCheckConcurrent(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "on"}
	for _, v := range truthy {
		if !parseDeferredCheckConcurrent(v) {
			t.Fatalf("parseDeferredCheckConcurrent(%q) = false, want true", v)
		}
	}
	falsy := []string{"", "0", "false", "no", "off", "garbage"}
	for _, v := range falsy {
		if parseDeferredCheckConcurrent(v) {
			t.Fatalf("parseDeferredCheckConcurrent(%q) = true, want false", v)
		}
	}
}

func TestBlockDecodeReusePoolOnIsMemoized(t *testing.T) {
	// The Once-guarded getters read the environment once per process; just
	// verify calling them repeatedly returns a stable, well-defined bool
	// without panicking, exercising the exported wrapper itself.
	first := BlockDecodeReusePoolOn()
	second := BlockDecodeReusePoolOn()
	if first != second {
		t.Fatalf("expected memoized stable result, got %v then %v", first, second)
	}
}

func TestDeferredCheckConcurrentOnIsMemoized(t *testing.T) {
	first := DeferredCheckConcurrentOn()
	second := DeferredCheckConcurrentOn()
	if first != second {
		t.Fatalf("expected memoized stable result, got %v then %v", first, second)
	}
}

func TestTimeNowMs(t *testing.T) {
	before := time.Now().UnixMilli()
	got := timeNowMs()
	after := time.Now().UnixMilli()
	if got < before || got > after {
		t.Fatalf("timeNowMs() = %d, want between %d and %d", got, before, after)
	}
}
