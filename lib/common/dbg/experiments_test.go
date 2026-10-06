package dbg

import (
	"context"
	"runtime"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func TestDoMemStat(t *testing.T) {
	// DoMemStat reflects the package-level doMemstat flag, computed from
	// whether NO_MEMSTAT was set at process start; just assert it returns
	// a bool without panicking and is consistent across calls.
	a := DoMemStat()
	b := DoMemStat()
	if a != b {
		t.Fatal("DoMemStat() should be stable across calls")
	}
}

func TestReadMemStats(t *testing.T) {
	var m runtime.MemStats
	ReadMemStats(&m)
	// No assertion on contents: if doMemstat is false this is a no-op,
	// otherwise runtime.ReadMemStats populates m. Either way it must not panic.
}

func TestSaveHeapOptions(t *testing.T) {
	var m runtime.MemStats
	opt := SaveHeapWithMemStats(&m)
	var o saveHeapOptions
	opt(&o)
	if o.memStats != &m {
		t.Fatal("SaveHeapWithMemStats did not set memStats")
	}

	logger := log.New()
	optLogger := SaveHeapWithLogger(&logger)
	var o2 saveHeapOptions
	optLogger(&o2)
	if o2.logger != &logger {
		t.Fatal("SaveHeapWithLogger did not set logger")
	}
}

func TestSaveHeapProfileNearOOMDisabled(t *testing.T) {
	// saveHeapProfile defaults to false (SAVE_HEAP_PROFILE unset in this
	// test process), so this must return immediately without creating files.
	SaveHeapProfileNearOOM()
}

func TestSaveHeapProfileNearOOMPeriodicallyDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// saveHeapProfile is false, so the function returns before the select
	// loop even starts; passing an already-cancelled context keeps this
	// safe regardless.
	SaveHeapProfileNearOOMPeriodically(ctx)
}
