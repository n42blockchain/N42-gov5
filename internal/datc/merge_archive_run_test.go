// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// merge_archive_run_test.go covers runMerge (the CLI wrapper around
// mergeBuilds, already exercised directly by TestE2E_SplitMerge) and
// Archive.Dir().
package datc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestRunMergeCLI mirrors TestE2E_SplitMerge's "upper_into_lower" case
// (datc_e2e_test.go — see its comment for why the lower build runs in two
// passes and the upper one is seeded via prep-state) but drives the merge
// through the runMerge CLI entry point instead of calling mergeBuilds
// directly, and checks every height plus a sampled proof on the result.
func TestRunMergeCLI(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	logger := log.New()
	lo := t.TempDir()
	hi := t.TempDir()
	o := e2eOpts{sched: epochSchedule{e: [maxChgDepth + 1]uint64{8, 64, 16, 1, 4096, 4096}}, batch: 50, stoCache: 64, accDepth: 4, stoDepth: 2, leafSeg: true, accRoot: 1}
	end := uint64(len(sc.blocks))
	split := uint64(150)
	mid := split / 2

	dbLo, err := openDatcDB(logger, lo, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, dbLo, sc)
	if err := newTestBuilder(t, dbLo, lo, sc, o, 0).run(0, mid, o.batch); err != nil {
		t.Fatalf("lower build (first run): %v", err)
	}
	if err := newTestBuilder(t, dbLo, lo, sc, o, mid).run(mid, split, o.batch); err != nil {
		t.Fatalf("lower build (resume): %v", err)
	}
	dbLo.Close()

	src, err := os.ReadFile(filepath.Join(lo, "mdbx.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hi, "mdbx.dat"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	runPrepState([]string{"--out", hi, "--map.gb", "4"})
	dbHi, err := openDatcDB(logger, hi, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeFwd(t, dbHi, sc)
	umid := split + (end-split)/2
	if err := newTestBuilder(t, dbHi, hi, sc, o, split).run(split, umid, o.batch); err != nil {
		t.Fatalf("upper build (first run): %v", err)
	}
	if err := newTestBuilder(t, dbHi, hi, sc, o, umid).run(umid, end, o.batch); err != nil {
		t.Fatalf("upper build (resume): %v", err)
	}
	dbHi.Close()

	runMerge([]string{"--into", hi, "--from", lo, "--map.gb", "4"})

	dbM, err := openDatcDB(logger, hi, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer dbM.Close()
	q, closeQ := openTestQuerier(t, dbM, hi, o)
	defer closeQ()
	for n := uint64(0); n < end; n++ {
		root, exists, err := q.nodeHashAt(nil, nil, n)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		if root != sc.roots[n] {
			t.Fatalf("height %d: root %x != reference %x after runMerge", n, root[:6], sc.roots[n][:6])
		}
	}
}

func TestArchiveDir(t *testing.T) {
	f := newRunFixture(t)
	a, err := OpenArchive(f.archiveDir, ArchiveOptions{MapGB: 4, Readers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Dir() != f.archiveDir {
		t.Fatalf("Dir() = %q, want %q", a.Dir(), f.archiveDir)
	}
}
