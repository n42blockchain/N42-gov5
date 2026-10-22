package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateOwnerProfilesDisabled(t *testing.T) {
	p, err := startPrivateOwnerProfiles("")
	if err != nil || p != nil || p.Close() != nil {
		t.Fatalf("disabled diagnostics: %v, %v", p, err)
	}
}

func TestPrivateOwnerProfilesRejectUnsafeOrExistingOutput(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrivateOwnerProfiles("relative"); err == nil {
		t.Fatal("accepted relative output")
	}
	link := filepath.Join(parent, "alias")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrivateOwnerProfiles(filepath.Join(link, "profiles")); err == nil {
		t.Fatal("accepted aliased parent")
	}
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "retained")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrivateOwnerProfiles(existing); err == nil {
		t.Fatal("reused existing output directory")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "unchanged" {
		t.Fatal("existing output modified")
	}
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := startPrivateOwnerProfiles(filepath.Join(parent, "public")); err == nil {
		t.Fatal("accepted public parent")
	}
}

func TestPrivateOwnerProfilesCloseWritesCompleteProfiles(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "profiles")
	p, err := startPrivateOwnerProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, "profile-session.json")); !os.IsNotExist(err) {
		t.Fatal("completion manifest published before stop")
	}
	<-time.After(5 * time.Millisecond)
	beforeStop := time.Now().UnixNano()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	afterStop := time.Now().UnixNano()
	if err := p.Close(); err != nil {
		t.Fatalf("close not idempotent: %v", err)
	}
	var manifest struct {
		Completed bool  `json:"completed"`
		Started   int64 `json:"started_unix_ns"`
		Finished  int64 `json:"finished_unix_ns"`
		Files     []struct {
			Path   string `json:"path"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "profile-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Completed || len(manifest.Files) != 3 || manifest.Started <= 0 || manifest.Started > manifest.Finished || manifest.Finished < beforeStop || manifest.Finished > afterStop {
		t.Fatalf("invalid completed capture: %s", raw)
	}
	for _, row := range manifest.Files {
		raw, err := os.ReadFile(row.Path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		if int64(len(raw)) != row.Bytes || hex.EncodeToString(digest[:]) != row.SHA256 {
			t.Fatal("profile hash/size mismatch")
		}
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		decoded, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(decoded) == 0 {
			t.Fatalf("invalid compressed profile: %v / %v", readErr, closeErr)
		}
		info, err := os.Lstat(row.Path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("profile must be a private regular file: %v", err)
		}
	}
}
