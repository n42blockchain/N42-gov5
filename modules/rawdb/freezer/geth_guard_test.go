// Copyright 2022-2026 The N42 Authors
package freezer

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsLikelyGethAncient exercises the heuristic used to auto-downgrade
// freezer.New to read-only when given a geth-managed ancient directory.
func TestIsLikelyGethAncient(t *testing.T) {
	tmp := t.TempDir()

	// Case 1: random dir with no geth marker → not detected.
	random := filepath.Join(tmp, "n42only")
	if err := os.MkdirAll(random, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsLikelyGethAncient(random) {
		t.Errorf("plain N42 dir wrongly detected as geth ancient")
	}

	// Case 2: dir with `geth` in path but no hashes.ridx → not detected.
	gethPath := filepath.Join(tmp, "geth", "chaindata", "ancient", "chain")
	if err := os.MkdirAll(gethPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsLikelyGethAncient(gethPath) {
		t.Errorf("geth-named dir without hashes.ridx wrongly detected")
	}

	// Case 3: geth path AND hashes.ridx present → detected.
	if err := os.WriteFile(filepath.Join(gethPath, "hashes.ridx"), []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsLikelyGethAncient(gethPath) {
		t.Errorf("real geth ancient layout NOT detected")
	}

	// Case 4: hashes.ridx present but no `geth` in path → not detected
	// (some N42 layouts also write hashes.ridx; the path component is
	// the disambiguator).
	notGeth := filepath.Join(tmp, "n42archive")
	if err := os.MkdirAll(notGeth, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notGeth, "hashes.ridx"), []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsLikelyGethAncient(notGeth) {
		t.Errorf("N42 dir with hashes.ridx wrongly detected as geth ancient")
	}
}

// TestNewAutoDowngradesGethAncient verifies that a freezer.New on a
// geth-style directory comes back in read-only mode. The smoke is
// implicit: if we successfully open + Append-attempt it, Append should
// error out cleanly rather than touching the file.
func TestNewAutoDowngradesGethAncient(t *testing.T) {
	tmp := t.TempDir()
	gethPath := filepath.Join(tmp, "geth", "chaindata", "ancient", "chain")
	if err := os.MkdirAll(gethPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant the geth marker.
	if err := os.WriteFile(filepath.Join(gethPath, "hashes.ridx"), []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := New(gethPath, 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()
	if !f.IsReadOnly() {
		t.Errorf("New should auto-downgrade geth ancient to read-only")
	}
}
