package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportPathsRejectSourceAndAliases(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "db")
	if err := os.Mkdir(db, 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "new", "state.jsonl")
	if err := validateExportPaths(db, output, output+".header", output+".manifest"); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{
		{output, output},
		{output, filepath.Join(root, "new", ".", "state.jsonl")},
		{filepath.Join(db, "mdbx.dat")},
		{filepath.Join(db, "new", "state.jsonl")},
		{db},
		{""},
	} {
		if err := validateExportPaths(db, paths...); err == nil {
			t.Fatalf("accepted unsafe outputs %q", paths)
		}
	}
	link := filepath.Join(root, "db-link")
	if err := os.Symlink(db, link); err != nil {
		t.Fatal(err)
	}
	if err := validateExportPaths(db, filepath.Join(link, "new", "state.jsonl")); err == nil {
		t.Fatal("accepted output through a symlink into the source")
	}
	if err := validateExportPaths(db, output, filepath.Join(root, "db", "..", "new", "state.jsonl")); err == nil {
		t.Fatal("accepted two names for the same destination")
	}
}

func TestRunRejectsSourceOutputBeforeOpeningDatabase(t *testing.T) {
	db := t.TempDir()
	source := filepath.Join(db, "mdbx.dat")
	want := []byte("source database must remain untouched")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(db, source, source+".header", source+".manifest", 94, 0); err == nil {
		t.Fatal("accepted source as an output")
	}
	got, err := os.ReadFile(source)
	if err != nil || string(got) != string(want) {
		t.Fatalf("source changed: %q, %v", got, err)
	}
}
