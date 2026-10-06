package state

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func newG65DomainStats() DomainStats {
	return DomainStats{
		HistoryQueries: &atomic.Uint64{},
		TotalQueries:   &atomic.Uint64{},
	}
}

func TestDomainStatsAccumulate(t *testing.T) {
	t.Parallel()

	a := newG65DomainStats()
	a.HistoryQueries.Store(3)
	a.TotalQueries.Store(5)
	a.EfSearchTime = 10
	a.IndexSize = 100
	a.DataSize = 200
	a.FilesCount = 2

	b := newG65DomainStats()
	b.HistoryQueries.Store(1)
	b.TotalQueries.Store(2)
	b.EfSearchTime = 4
	b.IndexSize = 10
	b.DataSize = 20
	b.FilesCount = 1

	a.Accumulate(b)

	if a.HistoryQueries.Load() != 4 {
		t.Fatalf("HistoryQueries = %d, want 4", a.HistoryQueries.Load())
	}
	if a.TotalQueries.Load() != 7 {
		t.Fatalf("TotalQueries = %d, want 7", a.TotalQueries.Load())
	}
	if a.EfSearchTime != 14 {
		t.Fatalf("EfSearchTime = %v, want 14", a.EfSearchTime)
	}
	if a.IndexSize != 110 {
		t.Fatalf("IndexSize = %d, want 110", a.IndexSize)
	}
	if a.DataSize != 220 {
		t.Fatalf("DataSize = %d, want 220", a.DataSize)
	}
	if a.FilesCount != 3 {
		t.Fatalf("FilesCount = %d, want 3", a.FilesCount)
	}
}

func TestDomainCollectFilesStatsEmpty(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	datsz, idxsz, files := d.collectFilesStats()
	if datsz != 0 || idxsz != 0 || files != 0 {
		t.Fatalf("expected all-zero stats for a domain with no files, got %d/%d/%d", datsz, idxsz, files)
	}
}

func TestDomainGetAndResetStatsResetsCounters(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	d.stats.MergesCount = 7 // seed a stale counter from a prior round; FilesCount/DataSize/IndexSize are always recomputed by collectFilesStats

	r := d.GetAndResetStats()
	if r.MergesCount != 7 {
		t.Fatalf("returned stats.MergesCount = %d, want 7 (the pre-reset snapshot)", r.MergesCount)
	}

	after := d.GetAndResetStats()
	if after.MergesCount != 0 {
		t.Fatalf("expected GetAndResetStats to clear stats, got MergesCount=%d", after.MergesCount)
	}
}

func TestDomainDeleteGarbageFilesRemovesColdFile(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	// A 1-step file (far from StepsInBiggestFile=32) is "cold" and eligible
	// for garbage deletion; create the on-disk .kv/.kvi files it names so
	// deleteGarbageFiles has something real to remove.
	item := newFilesItem(0, d.aggregationStep, d.aggregationStep)
	d.garbageFiles = []*filesItem{item}

	kvPath := filepath.Join(d.dir, d.filenameBase+".0-1.kv")
	kviPath := filepath.Join(d.dir, d.filenameBase+".0-1.kvi")
	if err := os.WriteFile(kvPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to seed %s: %v", kvPath, err)
	}
	if err := os.WriteFile(kviPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to seed %s: %v", kviPath, err)
	}

	d.deleteGarbageFiles()

	if d.garbageFiles != nil {
		t.Fatalf("expected deleteGarbageFiles to clear the garbage list")
	}
	if _, err := os.Stat(kvPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", kvPath, err)
	}
	if _, err := os.Stat(kviPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", kviPath, err)
	}
}

func TestDomainDeleteGarbageFilesSkipsFrozenSizedFile(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	// A file spanning exactly StepsInBiggestFile steps is treated as
	// "frozen-sized" and must be left alone even while listed as garbage.
	frozenSpan := StepsInBiggestFile * d.aggregationStep
	item := newFilesItem(0, frozenSpan, d.aggregationStep)
	d.garbageFiles = []*filesItem{item}

	kvPath := filepath.Join(d.dir, d.filenameBase+".0-32.kv")
	if err := os.WriteFile(kvPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("failed to seed %s: %v", kvPath, err)
	}
	t.Cleanup(func() { os.Remove(kvPath) })

	d.deleteGarbageFiles()

	if _, err := os.Stat(kvPath); err != nil {
		t.Fatalf("expected the frozen-sized file to survive, stat err = %v", err)
	}
}

func TestDomainCleanAfterFreezeNoOpOnZero(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	// frozenTo == 0 must return immediately without touching dirtyFiles.
	d.cleanAfterFreeze(0)
}

func TestDomainCleanAfterFreezeMarksOldFilesDeletable(t *testing.T) {
	logger := log.New()
	_, _, d := testDbAndDomain(t, logger)

	item := newFilesItem(0, d.aggregationStep, d.aggregationStep)
	item.decompressor = nil
	d.dirtyFiles.Set(item)

	d.cleanAfterFreeze(d.aggregationStep * 2)

	if !item.canDelete.Load() {
		t.Fatalf("expected the old, non-frozen file to be marked canDelete")
	}
}
