package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
)

func copyRestoreFixture(t *testing.T, source string, mode os.FileMode) (string, fileObservation) {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "mdbx.dat")
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr, os.Chmod(dst, mode)); err != nil {
		t.Fatal(err)
	}
	observed, err := observeData(dst, mode)
	if err != nil {
		t.Fatal(err)
	}
	return dir, observed
}

func originalRestoreFixture(t *testing.T) (string, *auditedGenesis, auditReport, candidateRestoreResult, []fileObservation) {
	t.Helper()
	dir, g, proof := restoreFixture(t)
	original, err := observeData(filepath.Join(dir, "mdbx.dat"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, backup := copyRestoreFixture(t, original.Path, 0400)
	_, audit := copyRestoreFixture(t, original.Path, 0400)
	candidateDir, before := copyRestoreFixture(t, original.Path, 0600)
	candidate, err := restoreCandidate(t.Context(), candidateDir, before, proof, g, "fixture-audit-sha", filepath.Join(t.TempDir(), "restore-intent.json"), []fileObservation{original, backup, audit})
	if err != nil {
		t.Fatal(err)
	}
	candidate.SourcesAfter = append([]fileObservation(nil), candidate.SourcesBefore...)
	candidate.Passed = true
	return dir, g, proof, candidate, []fileObservation{backup, audit, candidate.After}
}

func TestOriginalRestoreKeepsInodeModeAndPhysicallyReopens(t *testing.T) {
	dir, g, proof, candidate, protected := originalRestoreFixture(t)
	before := candidate.SourcesBefore[0]
	intent := filepath.Join(t.TempDir(), "original-intent.json")
	out, err := restoreOriginalFile(t.Context(), dir, before, proof, candidate, g, "fixture-audit-sha", "fixture-candidate-sha", intent, protected)
	if err != nil {
		t.Fatal(err)
	}
	if out.Passed || !out.OriginalOpened || out.BackupOpened || out.AuditCopyOpened || out.CandidateOpened || !out.Audit.Passed || len(out.Audit.Forest.MissingGenesis) != 0 || out.Audit.Applied != proof.Applied || out.Audit.Forest.Root != proof.Forest.Root || out.After.Inode != before.Inode || out.After.Device != before.Device || out.After.Mode != 0600 || out.After.SHA256 == before.SHA256 || !sameOtherRows(out.BeforeTables, out.AfterTables, true) {
		t.Fatalf("incomplete original physical proof: %+v", out)
	}
	for _, want := range protected {
		got, err := observeData(want.Path, 0400)
		if err != nil || got != want {
			t.Fatal("protected fixture changed", err)
		}
	}
	if _, err := os.Stat(intent); err != nil {
		t.Fatal("missing durable original intent", err)
	}
}

func TestOriginalRejectsProofAndCompleteIdentityChangesBeforeOpen(t *testing.T) {
	dir, g, proof, candidate, protected := originalRestoreFixture(t)
	before := candidate.SourcesBefore[0]
	for _, tc := range []struct {
		name   string
		change func(*fileObservation, *candidateRestoreResult)
	}{
		{"inode", func(b *fileObservation, _ *candidateRestoreResult) { b.Inode++ }},
		{"ctime", func(b *fileObservation, _ *candidateRestoreResult) { b.Changed++ }},
		{"file-sha", func(b *fileObservation, _ *candidateRestoreResult) { b.SHA256 = "wrong" }},
		{"audit-link", func(_ *fileObservation, p *candidateRestoreResult) { p.ProofSHA256 = "wrong" }},
		{"not-promoted", func(_ *fileObservation, p *candidateRestoreResult) { p.Passed = false }},
		{"source-changed", func(_ *fileObservation, p *candidateRestoreResult) { p.SourcesAfter = nil }},
		{"table-change", func(_ *fileObservation, p *candidateRestoreResult) {
			p.AfterTables = append([]tableFingerprint(nil), p.AfterTables...)
			p.AfterTables[0].SHA256 = "wrong"
		}},
		{"reopen-root", func(_ *fileObservation, p *candidateRestoreResult) { p.Audit.Applied.Root[0] ^= 1 }},
	} {
		b, p := before, candidate
		tc.change(&b, &p)
		opens := 0
		opener := func(context.Context, string, bool) (kv.RwDB, error) {
			opens++
			return nil, errors.New("unexpected open")
		}
		intent := filepath.Join(t.TempDir(), "original-intent.json")
		if _, err := restoreOriginalWithOpen(t.Context(), dir, b, proof, p, g, "fixture-audit-sha", "fixture-candidate-sha", intent, protected, opener); err == nil || opens != 0 {
			t.Fatalf("%s did not reject before any MDBX open", tc.name)
		}
		if _, err := os.Lstat(intent); !os.IsNotExist(err) {
			t.Fatalf("%s wrote intent", tc.name)
		}
	}
	got, err := observeData(before.Path, 0600)
	if err != nil || got != before {
		t.Fatal("rejected requests changed original", err)
	}
}

func TestOriginalCommittedReopenFailureRetainsRowsAndIntent(t *testing.T) {
	dir, g, proof, candidate, protected := originalRestoreFixture(t)
	before := candidate.SourcesBefore[0]
	intent := filepath.Join(t.TempDir(), "original-intent.json")
	opens := 0
	opener := func(ctx context.Context, path string, readonly bool) (kv.RwDB, error) {
		opens++
		if opens == 3 {
			if !readonly {
				t.Fatal("third open must be physically new readonly environment")
			}
			return nil, errors.New("injected post-commit physical reopen failure")
		}
		return openCandidateDB(ctx, path, readonly)
	}
	out, err := restoreOriginalWithOpen(t.Context(), dir, before, proof, candidate, g, "fixture-audit-sha", "fixture-candidate-sha", intent, protected, opener)
	if err == nil || out.Passed || opens != 3 {
		t.Fatal("post-commit failure was retried or marked passed", err, opens)
	}
	if _, err := os.Stat(intent); err != nil {
		t.Fatal("lost original intent after commit", err)
	}
	db, err := openCandidateDB(t.Context(), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		for id := range uint64(2) {
			has, err := tx.Has(qmdb.LeavesTable, position(id))
			if err != nil {
				return err
			}
			if !has {
				t.Fatal("committed leaf was automatically removed")
			}
		}
		_, err := auditBoundSnapshot(tx, g)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := observeData(before.Path, 0600)
	if err != nil || got.Inode != before.Inode || got.SHA256 == before.SHA256 {
		t.Fatal("committed original was replaced or rolled back", err)
	}
}

func TestOriginalWritablePathMustEqualProvedRuntime(t *testing.T) {
	expected := fileObservation{Path: "/data/blockchain/gov5-performance-5m/runtime/s192r1/e/chaindata/mdbx.dat"}
	for _, path := range []string{"/data/blockchain/gov5-performance-5m/runtime/other/e/chaindata", "/data/blockchain/gov5-work/validation/x/restore-candidate/chaindata", "/home/n42/src/n42/N42-gov5/n42data/chaindata", "relative/e/chaindata"} {
		if _, err := originalDataFile(path, expected); err == nil {
			t.Fatalf("accepted unproved original path %q", path)
		}
	}
}
