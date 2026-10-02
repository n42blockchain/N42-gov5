// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// querier_internals_run_test.go covers querier methods and standalone
// scanners that the CLI workers call internally, exercised directly against
// the fixture archive rather than through a full diag/segexport CLI run.
package datc

import (
	"context"
	"path/filepath"
	"testing"
)

// TestFoldAtTracedMatchesFoldAt checks foldAtTraced (verify.go, used by
// runFoldDiff's structural trace) returns the same root as the untraced
// foldAt, and that it actually captured branch records along the way.
func TestFoldAtTracedMatchesFoldAt(t *testing.T) {
	f := newRunFixture(t)
	q, _, closeQ := f.openQuerier(t)
	defer closeQ()

	const n = uint64(200)
	want, wantOK, err := q.foldAt(nil, nil, n)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	got, gotOK, err := q.foldAtTraced(nil, nil, n, out)
	if err != nil {
		t.Fatal(err)
	}
	if gotOK != wantOK || got != want {
		t.Fatalf("foldAtTraced = (%x,%v), foldAt = (%x,%v)", got, gotOK, want, wantOK)
	}
	if len(out) == 0 {
		t.Fatal("expected at least one traced branch emission")
	}
}

// TestSubtreeLeaves checks subtreeLeaves (proof.go) returns one mleaf per
// as-of leaf under a big contract's storage domain, with non-empty items.
func TestSubtreeLeaves(t *testing.T) {
	f := newRunFixture(t)
	q, _, closeQ := f.openQuerier(t)
	defer closeQ()

	addr := f.sc.big[0]
	ah := keccak(addr[:])
	const n = uint64(200)
	leaves, err := q.subtreeLeaves(ah[:], nil, n)
	if err != nil {
		t.Fatal(err)
	}
	want := len(f.sc.storageAt(addr, n))
	if len(leaves) != want {
		t.Fatalf("subtreeLeaves: got %d leaves, scenario has %d live slots", len(leaves), want)
	}
	for _, lf := range leaves {
		if len(lf.item) == 0 {
			t.Fatal("leaf item must not be empty")
		}
	}
}

// TestCensusBucket runs censusBucket (deriveplan.go) over one of the
// fixture's storage leaf segment files.
func TestCensusBucket(t *testing.T) {
	f := newRunFixture(t)
	matches, err := filepath.Glob(filepath.Join(f.archiveDir, leafSegDir, "s.*.seg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Skip("fixture archive has no storage leaf segment files")
	}
	var total int
	for _, m := range matches {
		census, err := censusBucket(m, 4, 0)
		if err != nil {
			t.Fatalf("censusBucket(%s): %v", m, err)
		}
		total += len(census)
	}
	if total == 0 {
		t.Fatal("expected at least one contract in the census")
	}
}

// TestExportTable runs exportTable (segexport.go) over the account node
// table of a plain (non-leaf-seg) build, where node records live in MDBX.
func TestExportTable(t *testing.T) {
	sc, out := buildTinyArchive(t)
	_ = sc
	db, err := openArchiveDB(out, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	st, err := exportTable(tx, tDatcAccNode, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if st.rows == 0 {
		t.Fatal("expected account node rows in a non-leaf-seg build")
	}
	if st.zstdMax == 0 {
		t.Fatal("expected a non-zero compressed size")
	}
}
