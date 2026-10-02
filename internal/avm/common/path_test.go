package common

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMakeName(t *testing.T) {
	name := MakeName("n42", "1.0.0")
	if !strings.HasPrefix(name, "n42/v1.0.0/") {
		t.Errorf("MakeName() = %q, want prefix %q", name, "n42/v1.0.0/")
	}
	if !strings.Contains(name, runtime.GOOS) {
		t.Errorf("MakeName() = %q, want it to contain GOOS %q", name, runtime.GOOS)
	}
	if !strings.Contains(name, runtime.Version()) {
		t.Errorf("MakeName() = %q, want it to contain the Go version", name)
	}
}

func TestFileExist(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists.txt")
	if FileExist(file) {
		t.Error("FileExist() should be false before the file is created")
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	if !FileExist(file) {
		t.Error("FileExist() should be true after the file is created")
	}
}

func TestAbsolutePath(t *testing.T) {
	if got := AbsolutePath("/data", "/already/absolute"); got != "/already/absolute" {
		t.Errorf("AbsolutePath() = %q, want unchanged absolute path", got)
	}
	got := AbsolutePath("/data", "relative/file.txt")
	want := filepath.Join("/data", "relative/file.txt")
	if got != want {
		t.Errorf("AbsolutePath() = %q, want %q", got, want)
	}
}
