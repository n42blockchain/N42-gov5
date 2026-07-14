package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenPrivateOutputTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits: os.Chmod only toggles the
		// read-only attribute and a writable file always reports mode 0666.
		// The permission-tightening is a no-op there, so the POSIX assertion
		// is not meaningful.
		t.Skip("Unix file permissions are not enforced on Windows")
	}
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
