//go:build !windows && !openbsd

package main

import "testing"

func TestN42GetFreeDiskSpace(t *testing.T) {
	dir := t.TempDir()
	space, err := getFreeDiskSpace(dir)
	if err != nil {
		t.Fatalf("getFreeDiskSpace error: %v", err)
	}
	if space == 0 {
		t.Fatal("expected a non-zero free disk space on a real filesystem")
	}
}

func TestN42GetFreeDiskSpaceMissingPath(t *testing.T) {
	if _, err := getFreeDiskSpace("/this/path/does/not/exist/at/all"); err == nil {
		t.Fatal("expected an error for a nonexistent path")
	}
}
