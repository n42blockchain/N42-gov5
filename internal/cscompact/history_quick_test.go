package cscompact

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHistoryFromChangesets builds history from changeset tables (fast path).
func TestHistoryFromChangesets(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()

	t0 := time.Now()
	builder := NewAccountHistoryBuilder(db, tmpDir)
	if err := builder.BuildFromChangesets(context.Background(), 0, 1_000_000); err != nil {
		t.Fatal(err)
	}
	buildTime := time.Since(t0)

	baseName := HistSegmentFileName("account_hist", 0, 1_000_000)
	idxPath := filepath.Join(tmpDir, baseName+".idx")
	datPath := filepath.Join(tmpDir, baseName+".dat")

	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)

	seg, err := OpenHistorySegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	t.Logf("From Changesets (0-1M blocks):")
	t.Logf("  Keys: %d", seg.KeyCount())
	t.Logf("  Idx: %d bytes (%.1f B/key)", idxFi.Size(), float64(idxFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Dat: %d bytes (%.1f B/key)", datFi.Size(), float64(datFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Total: %.1f B/key", float64(idxFi.Size()+datFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Build time: %v (vs 7min from history bitmaps)", buildTime)
}

// TestHistoryQuickBuild builds using old method (history bitmaps) for comparison.
func TestHistoryQuickBuild(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()

	// Account history has far fewer entries — fast to build.
	t0 := time.Now()
	builder := NewAccountHistoryBuilder(db, tmpDir)
	if err := builder.BuildRange(context.Background(), 0, 1_000_000); err != nil {
		t.Fatal(err)
	}
	buildTime := time.Since(t0)

	baseName := HistSegmentFileName("account_hist", 0, 1_000_000)
	idxPath := filepath.Join(tmpDir, baseName+".idx")
	datPath := filepath.Join(tmpDir, baseName+".dat")

	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)

	seg, err := OpenHistorySegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	t.Logf("Account History segment (0-1M blocks):")
	t.Logf("  Keys: %d", seg.KeyCount())
	t.Logf("  Idx: %d bytes (%.1f B/key)", idxFi.Size(), float64(idxFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Dat: %d bytes (%.1f B/key)", datFi.Size(), float64(datFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Total: %.1f B/key", float64(idxFi.Size()+datFi.Size())/float64(seg.KeyCount()+1))
	t.Logf("  Build time: %v", buildTime)

	// Quick lookup test.
	// Block 0 should have genesis accounts in history.
	// Try a few known addresses.
	found := 0
	for blockNum := uint64(100); blockNum < 1000; blockNum += 100 {
		// Lookup zero address.
		var zeroAddr [20]byte
		if b, ok := seg.Lookup(zeroAddr[:], blockNum); ok {
			found++
			_ = b
		}
	}
	t.Logf("  Zero-addr lookups: %d/9 found", found)
}
