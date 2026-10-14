package main

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitOwnerFilePrivateAndExplicit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner")
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := loadCommitOwner(path)
	if err != nil || owner == ([32]byte{}) {
		t.Fatalf("private owner: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCommitOwner(path); err == nil {
		t.Fatal("public owner accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCommitOwner(alias); err == nil {
		t.Fatal("symlink accepted")
	}
	for _, invalid := range [][]byte{nil, make([]byte, 32), append(data, 1)} {
		if err := os.WriteFile(path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCommitOwner(path); err == nil {
			t.Fatal("invalid owner bytes accepted")
		}
	}
}
