package sharedexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionSocketRefusesExistingPublicAndSymlinkPaths(t *testing.T) {
	server := &Server{}
	dir := t.TempDir()
	path := filepath.Join(dir, "execute.sock")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.ListenUnix(path); err == nil {
		t.Fatal("existing socket path replaced")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "preserve" {
		t.Fatal("existing path changed")
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.ListenUnix(filepath.Join(dir, "public.sock")); err == nil {
		t.Fatal("public socket directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.ListenUnix(filepath.Join(link, "symlink.sock")); err == nil {
		t.Fatal("symlink socket directory accepted")
	}
}
