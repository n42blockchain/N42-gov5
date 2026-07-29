package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidateApplyFlags(t *testing.T) {
	if err := validateApplyFlags(false, false, ""); err != nil {
		t.Fatalf("dry run rejected: %v", err)
	}
	if err := validateApplyFlags(true, false, "backup.bin"); err == nil {
		t.Fatal("apply without force accepted")
	}
	if err := validateApplyFlags(true, true, ""); err == nil {
		t.Fatal("apply without backup accepted")
	}
	if err := validateApplyFlags(true, true, "backup.bin"); err != nil {
		t.Fatalf("fully acknowledged apply rejected: %v", err)
	}
}

func TestWriteExclusiveBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hotstuff-state.bin")
	want := []byte{0x01, 0x02, 0x03}
	if err := writeExclusiveBackup(path, want); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("backup bytes = %x, want %x", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits: os.OpenFile(..., 0600) reports 0666
	// back, so assert the mode only where it is actually enforced. The write
	// itself still asks for 0600 on every platform.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}
	if err := writeExclusiveBackup(path, []byte{0xFF}); err == nil {
		t.Fatal("existing backup was overwritten")
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("existing backup changed: %x", got)
	}
}
