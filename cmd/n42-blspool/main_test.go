package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateOutputTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.md")
	if err := os.WriteFile(path, []byte("old secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openPrivateOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("private output mode = %o, want 600", got)
	}
}
