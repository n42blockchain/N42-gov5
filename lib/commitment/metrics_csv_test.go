// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// metrics.go's CSV persistence path (WriteToCSV / UnmarshallMetricsCsv and the
// per-sub-metric Account/Branch CSVs) was entirely untested. This drives one
// real round trip: build a Metrics with non-zero counters, enable CSV output,
// write it, then read every one of the three CSV files back and check the
// values survive. That single round trip exercises writeMetricsToCSV,
// unmarshallCsvMetrics, readMetricsFromCSV, validateMetricsHeader, isRowEmpty,
// and the mustParse*/parse* cell helpers transitively.

package commitment

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMetricsWriteAndUnmarshallCSVRoundTrip(t *testing.T) {
	m := NewMetrics()
	prefix := filepath.Join(t.TempDir(), "run")
	m.EnableCsvMetrics(prefix)

	m.updates.Store(10)
	m.addressKeys.Store(20)
	m.storageKeys.Store(30)
	m.loadBranch.Store(40)
	m.loadAccount.Store(50)
	m.loadStorage.Store(60)
	m.updateBranch.Store(70)
	m.cacheBranch.Store(1)
	m.cacheAccount.Store(2)
	m.cacheStorage.Store(3)
	m.unfolds.Store(5)
	m.spentUnfolding = 12 * time.Millisecond
	m.spentFolding = 34 * time.Millisecond
	m.spentProcessing = 56 * time.Millisecond

	m.Accounts.collect([]byte("addr-account-key-000"), func(as *AccountStats) {
		as.StorageUpates = 3
		as.LoadAccount = 1
		as.SpentUnfolding = 7 * time.Microsecond
	})
	m.Branches.collect([]byte{0xaa, 0xbb}, func(bs *BranchStats) {
		bs.LoadBranch = 9
	})

	m.WriteToCSV()

	gotMetrics, err := UnmarshallMetricsCsv(prefix + "_process.csv")
	if err != nil {
		t.Fatalf("UnmarshallMetricsCsv: %v", err)
	}
	if len(gotMetrics) != 1 {
		t.Fatalf("expected 1 metrics row, got %d", len(gotMetrics))
	}
	got := gotMetrics[0]
	if got.updates.Load() != 10 || got.addressKeys.Load() != 20 || got.storageKeys.Load() != 30 {
		t.Fatalf("process csv round trip mismatch: updates=%d addressKeys=%d storageKeys=%d",
			got.updates.Load(), got.addressKeys.Load(), got.storageKeys.Load())
	}
	if got.spentFolding != 34*time.Millisecond {
		t.Fatalf("spentFolding = %v, want 34ms", got.spentFolding)
	}

	gotAccounts, err := UnmarshallAccountMetricsCsv(prefix + "_account.csv")
	if err != nil {
		t.Fatalf("UnmarshallAccountMetricsCsv: %v", err)
	}
	if len(gotAccounts) != 1 {
		t.Fatalf("expected 1 account-metrics batch, got %d", len(gotAccounts))
	}
	foundAccount := false
	for _, st := range gotAccounts[0].AccountStats {
		if st.StorageUpates == 3 && st.LoadAccount == 1 {
			foundAccount = true
		}
	}
	if !foundAccount {
		t.Fatalf("expected the recorded account stats to round-trip, got %+v", gotAccounts[0].AccountStats)
	}

	gotBranches, err := UnmarshallBranchMetricsCsv(prefix + "_branches.csv")
	if err != nil {
		t.Fatalf("UnmarshallBranchMetricsCsv: %v", err)
	}
	if len(gotBranches) != 1 {
		t.Fatalf("expected 1 branch-metrics batch, got %d", len(gotBranches))
	}
}

func TestMetricsResetAndAsValues(t *testing.T) {
	m := NewMetrics()
	m.EnableCsvMetrics(filepath.Join(t.TempDir(), "r"))
	m.updates.Store(5)
	m.unfolds.Store(9)
	m.spentFolding = time.Second

	vals := m.AsValues()
	if vals.Updates != 5 || vals.Unfolds != 9 {
		t.Fatalf("AsValues() = %+v, want Updates=5 Unfolds=9", vals)
	}

	m.Reset()
	if m.updates.Load() != 0 || m.unfolds.Load() != 0 {
		t.Fatalf("Reset() left updates=%d unfolds=%d, want 0,0", m.updates.Load(), m.unfolds.Load())
	}
	if m.spentFolding != 0 {
		t.Fatalf("Reset() left spentFolding=%v, want 0 (collectCommitmentMetrics was enabled)", m.spentFolding)
	}
}
