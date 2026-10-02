package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestN42ValidateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := validateDirectory(dir, "datadir"); err != nil {
		t.Fatalf("validateDirectory on a real dir: %v", err)
	}

	missing := filepath.Join(dir, "does-not-exist")
	if err := validateDirectory(missing, "datadir"); err == nil {
		t.Fatal("expected error for a missing path")
	}

	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := validateDirectory(file, "datadir"); err == nil {
		t.Fatal("expected error when path is a file, not a directory")
	}
}

func TestN42AllFlagsNonEmpty(t *testing.T) {
	flags := AllFlags()
	if len(flags) == 0 {
		t.Fatal("AllFlags returned no flags")
	}
	seen := map[string]bool{}
	for _, f := range flags {
		for _, name := range f.Names() {
			seen[name] = true
		}
	}
	for _, want := range []string{"datadir", "debug"} {
		if !seen[want] {
			t.Errorf("AllFlags missing expected flag %q", want)
		}
	}
}
