package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTestStatsStartsEmpty(t *testing.T) {
	t.Parallel()

	stats := NewTestStats()
	if stats == nil {
		t.Fatal("NewTestStats returned nil")
	}
	if stats.ByCategory == nil || stats.ByFork == nil {
		t.Fatal("expected initialized maps")
	}
	if len(stats.FailedTests) != 0 {
		t.Fatalf("expected no failed tests, got %d", len(stats.FailedTests))
	}
}

func TestTestStatsAddResultTracksBuckets(t *testing.T) {
	t.Parallel()

	stats := NewTestStats()
	stats.AddResult("stExample", "example.json", "ok", "Cancun", true, false, "")
	stats.AddResult("stExample", "example.json", "skip", "Cancun", false, true, "known issue")
	stats.AddResult("stExample", "example.json", "fail", "Prague", false, false, "boom")

	if stats.TotalTests != 3 {
		t.Fatalf("TotalTests=%d, want 3", stats.TotalTests)
	}
	if stats.Passed != 1 || stats.Skipped != 1 || stats.Failed != 1 {
		t.Fatalf("unexpected counters: passed=%d skipped=%d failed=%d", stats.Passed, stats.Skipped, stats.Failed)
	}

	cancun := stats.ByFork["Cancun"]
	if cancun == nil || cancun.Total != 2 || cancun.Passed != 1 || cancun.Skipped != 1 {
		t.Fatalf("unexpected Cancun stats: %+v", cancun)
	}
	prague := stats.ByFork["Prague"]
	if prague == nil || prague.Total != 1 || prague.Failed != 1 {
		t.Fatalf("unexpected Prague stats: %+v", prague)
	}

	category := stats.ByCategory["stExample"]
	if category == nil || category.Total != 3 || category.Passed != 1 || category.Skipped != 1 || category.Failed != 1 {
		t.Fatalf("unexpected category stats: %+v", category)
	}

	if len(stats.FailedTests) != 1 {
		t.Fatalf("expected 1 failed test entry, got %d", len(stats.FailedTests))
	}
}

func TestTestStatsPrintReportIncludesSummary(t *testing.T) {
	t.Parallel()

	stats := NewTestStats()
	stats.AddResult("stCallCodes", "callcodes.json", "pass", "Cancun", true, false, "")
	stats.AddResult("stCallCodes", "callcodes.json", "fail", "Cancun", false, false, "balance mismatch")

	report := stats.PrintReport()
	for _, needle := range []string{
		"ETHEREUM EXECUTION LAYER TEST RESULTS",
		"RESULTS BY FORK",
		"RESULTS BY CATEGORY",
		"FAILED TEST SUMMARY",
		"stCallCodes/fail [Cancun]: balance mismatch",
	} {
		if !strings.Contains(report, needle) {
			t.Fatalf("report missing %q\n%s", needle, report)
		}
	}
}

func TestSaveFailedTestsJSON(t *testing.T) {
	t.Parallel()

	stats := NewTestStats()
	stats.AddResult("stExample", "example.json", "fail", "Cancun", false, false, "boom")

	path := filepath.Join(t.TempDir(), "failed.json")
	if err := stats.SaveFailedTestsJSON(path); err != nil {
		t.Fatalf("SaveFailedTestsJSON returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}

	var failed []FailedTest
	if err := json.Unmarshal(data, &failed); err != nil {
		t.Fatalf("failed to decode output: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 failed test, got %d", len(failed))
	}
	if failed[0].Message != "boom" {
		t.Fatalf("unexpected failed test payload: %+v", failed[0])
	}
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	if got := truncateString("short", 10); got != "short" {
		t.Fatalf("truncateString returned %q, want %q", got, "short")
	}
	if got := truncateString("abcdefghijk", 5); got != "abcde..." {
		t.Fatalf("truncateString returned %q, want %q", got, "abcde...")
	}
}
