package commitment

// cmT_metrics_test.go drives Metrics' levelled-metrics paths directly on an
// instance (not via the global dbg.KVReadLevelledMetrics flag): RLock/RUnlock
// on MetricValues, CollectFileDepthStats, logMetrics, StartUnfolding/
// StartFolding/TotalProcessingTimeInc and the CSV write/read round trip via
// UnmarshallMetricValuesCsv.

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMetrics_RLockRUnlock(t *testing.T) {
	// mu == nil: both must be safe no-ops.
	var mv MetricValues
	mv.RLock()
	mv.RUnlock()

	// mu present: real RWMutex lock/unlock.
	m := NewMetrics()
	mv2 := m.AsValues()
	mv2.RLock()
	mv2.RUnlock()
}

func TestMetrics_CollectFileDepthStats(t *testing.T) {
	m := NewMetrics()
	m.EnableCsvMetrics(filepath.Join(t.TempDir(), "cmT-metrics"))

	stats := map[uint64]skipStat{
		100: {accLoaded: 1, storLoaded: 2},
		200: {accLoaded: 3, storLoaded: 4},
		50:  {accLoaded: 5, storLoaded: 6},
	}
	m.CollectFileDepthStats(stats)

	// Highest endTxNum (200) sorts first; loadDepths[0:2] = its acc/stor loads.
	if m.loadDepths[0] != 3 || m.loadDepths[1] != 4 {
		t.Errorf("loadDepths[0:2]: got %d/%d, want 3/4", m.loadDepths[0], m.loadDepths[1])
	}
	if m.loadDepths[2] != 1 || m.loadDepths[3] != 2 {
		t.Errorf("loadDepths[2:4]: got %d/%d, want 1/2", m.loadDepths[2], m.loadDepths[3])
	}

	// writeCommitmentMetrics disabled: no-op, state unchanged.
	m2 := NewMetrics()
	m2.CollectFileDepthStats(stats)
	for i, v := range m2.loadDepths {
		if v != 0 {
			t.Errorf("loadDepths[%d] on disabled metrics: got %d, want 0", i, v)
		}
	}
}

func TestMetrics_LogMetrics(t *testing.T) {
	m := NewMetrics()
	m.addressKeys.Store(1)
	m.storageKeys.Store(2)
	m.loadBranch.Store(3)
	fields := m.logMetrics()
	if len(fields) == 0 || len(fields)%2 != 0 {
		t.Fatalf("logMetrics: got %d fields, want a non-zero even count of key/value pairs", len(fields))
	}
}

func TestMetrics_StartFoldingUnfolding_And_TotalProcessingTime(t *testing.T) {
	// Disabled (collectCommitmentMetrics=false): both return a no-op closer
	// and never touch the duration fields.
	m := NewMetrics()
	doneU := m.StartUnfolding([]byte("k"))
	doneF := m.StartFolding([]byte("k"))
	doneU()
	doneF()
	if m.spentUnfolding != 0 || m.spentFolding != 0 {
		t.Errorf("disabled metrics should not accumulate duration: unfolding=%v folding=%v", m.spentUnfolding, m.spentFolding)
	}
	m.TotalProcessingTimeInc(time.Now().Add(-time.Millisecond))
	if m.spentProcessing != 0 {
		t.Errorf("disabled TotalProcessingTimeInc should be a no-op, got %v", m.spentProcessing)
	}

	// Enabled: closers and TotalProcessingTimeInc actually accumulate time.
	m2 := NewMetrics()
	m2.EnableCsvMetrics(filepath.Join(t.TempDir(), "cmT-metrics2"))
	doneU2 := m2.StartUnfolding([]byte("addr-key-000000000000000000x"))
	doneU2()
	doneF2 := m2.StartFolding([]byte("addr-key-000000000000000000x"))
	doneF2()
	m2.TotalProcessingTimeInc(time.Now().Add(-time.Millisecond))
	if m2.spentProcessing <= 0 {
		t.Error("enabled TotalProcessingTimeInc should accumulate a positive duration")
	}
}

func TestMetrics_WriteToCSV_And_UnmarshallMetricValuesCsv(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "cmT-metrics-csv")
	m := NewMetrics()
	m.EnableCsvMetrics(prefix)
	m.updates.Store(5)
	m.addressKeys.Store(2)
	m.storageKeys.Store(3)
	m.WriteToCSV()

	loaded, err := UnmarshallMetricsCsv(prefix + "_process.csv")
	if err != nil {
		t.Fatalf("UnmarshallMetricsCsv: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("UnmarshallMetricsCsv: got %d rows, want 1", len(loaded))
	}
	if loaded[0].updates.Load() != 5 {
		t.Errorf("round-trip updates: got %d, want 5", loaded[0].updates.Load())
	}

	// UnmarshallMetricValuesCsv reads the same per-process CSV through the
	// MetricValues-shaped decoder (0% covered otherwise).
	vals, err := UnmarshallMetricValuesCsv(prefix)
	if err != nil {
		t.Fatalf("UnmarshallMetricValuesCsv: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("UnmarshallMetricValuesCsv: got %d rows, want 1", len(vals))
	}
	if vals[0].Updates != 5 {
		t.Errorf("UnmarshallMetricValuesCsv Updates: got %d, want 5", vals[0].Updates)
	}
}
