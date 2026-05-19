// Copyright 2022-2026 The N42 Authors
package bundle

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// writeBundleFile creates rootDir/chain/freezer/name with given size.
// Uses deterministic pseudo-random content via reader so two test runs
// produce the same bytes (when seed=0 we use crypto/rand for varied
// content per run; harmless because we only compare hash within a test).
func writeBundleFile(t *testing.T, rootDir, name string, size int64) {
	t.Helper()
	dir := filepath.Join(rootDir, "chain", "freezer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	written := int64(0)
	for written < size {
		n := int64(len(buf))
		if n > size-written {
			n = size - written
		}
		rand.Read(buf[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
}

// TestRoundTripSmallFiles builds + verifies a manifest over a handful
// of small files (no per-segment hashes; all under SegmentThreshold).
func TestRoundTripSmallFiles(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)
	writeBundleFile(t, root, "bodyc.cidx", 2048)
	writeBundleFile(t, root, "codes.0000.cdat", 1<<20) // 1 MiB

	m, err := Build(root, BuildOptions{ChainID: 1, BlockRange: BlockRange{Start: 0, End: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(m.Files))
	}
	for _, f := range m.Files {
		if f.Segments != nil {
			t.Errorf("file %s under threshold should not have segments", f.Path)
		}
		if len(f.Hash) != 64 {
			t.Errorf("file %s has malformed hash %q", f.Path, f.Hash)
		}
	}

	results, err := Verify(root, m, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != StatusOK {
			t.Errorf("file %s: status=%v err=%v", r.Path, r.Status, r.Err)
		}
	}
}

// TestDefaultIsBLAKE3 pins the default Build algorithm to BLAKE3-256
// so accidental regressions to BLAKE2b are caught immediately.
func TestDefaultIsBLAKE3(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)
	m, err := Build(root, BuildOptions{ChainID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if m.Algorithm != AlgoBlake3_256 {
		t.Errorf("default algorithm: got %q, want %q", m.Algorithm, AlgoBlake3_256)
	}
}

// TestBLAKE2bBackCompat checks that a manifest explicitly built with
// the legacy BLAKE2b-256 algorithm round-trips through Verify. Without
// this, pre-2026-05 bundles would become unverifiable on upgrade.
func TestBLAKE2bBackCompat(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)
	writeBundleFile(t, root, "bodyc.0000.cdat", 4096)

	m, err := Build(root, BuildOptions{ChainID: 1, Algorithm: AlgoBlake2b256})
	if err != nil {
		t.Fatal(err)
	}
	if m.Algorithm != AlgoBlake2b256 {
		t.Fatalf("expected %q, got %q", AlgoBlake2b256, m.Algorithm)
	}
	results, err := Verify(root, m, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != StatusOK {
			t.Errorf("file %s: status=%v err=%v", r.Path, r.Status, r.Err)
		}
	}
}

// TestUnknownAlgorithmRejected confirms that a hand-edited manifest
// claiming a bogus algorithm fails to load (defense against alg-
// downgrade or attacker-supplied gibberish).
func TestUnknownAlgorithmRejected(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)
	if _, err := Build(root, BuildOptions{ChainID: 1, Algorithm: "md5"}); err == nil {
		t.Errorf("Build with bogus algorithm should fail")
	}
}

// TestRehashFlow exercises the same sequence n42-bundle-rehash runs:
// build a BLAKE2b manifest → rebuild with BLAKE3 keeping the same
// file set → verify the new manifest. Catches regressions in the
// hash-agility plumbing (Build's Algorithm override, Verify's
// per-manifest dispatch, file-set drift detection).
func TestRehashFlow(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)
	writeBundleFile(t, root, "bodyc.0000.cdat", 4096)
	writeBundleFile(t, root, "storcs.cidx", 8192)

	old, err := Build(root, BuildOptions{ChainID: 1, Algorithm: AlgoBlake2b256})
	if err != nil {
		t.Fatal(err)
	}
	if old.Algorithm != AlgoBlake2b256 {
		t.Fatalf("expected legacy algorithm, got %q", old.Algorithm)
	}

	fresh, err := Build(root, BuildOptions{
		ChainID:    old.ChainID,
		BlockRange: old.BlockRange,
		Algorithm:  AlgoBlake3_256,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Algorithm != AlgoBlake3_256 {
		t.Fatalf("expected BLAKE3 algorithm, got %q", fresh.Algorithm)
	}

	if len(fresh.Files) != len(old.Files) {
		t.Fatalf("file count drift: old=%d new=%d", len(old.Files), len(fresh.Files))
	}
	oldByPath := make(map[string]File, len(old.Files))
	for _, f := range old.Files {
		oldByPath[f.Path] = f
	}
	for _, f := range fresh.Files {
		o := oldByPath[f.Path]
		if o.Size != f.Size {
			t.Errorf("size drift for %s: old=%d new=%d", f.Path, o.Size, f.Size)
		}
		// Two pure hash funcs producing identical bytes for the same
		// input would mean swap was a no-op — pin against that.
		if o.Hash == f.Hash {
			t.Errorf("hash identical across algorithms for %s — suspicious", f.Path)
		}
	}

	results, err := Verify(root, fresh, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != StatusOK {
			t.Errorf("post-rehash verify failed on %s: status=%v err=%v",
				r.Path, r.Status, r.Err)
		}
	}
}

// TestCorruptionDetected ensures whole-file hash mismatch is flagged.
func TestCorruptionDetected(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 1024)

	m, err := Build(root, BuildOptions{ChainID: 1})
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt one byte.
	path := filepath.Join(root, "chain", "freezer", "headerc.cidx")
	data, _ := os.ReadFile(path)
	data[0] ^= 0xFF
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	results, _ := Verify(root, m, VerifyOptions{})
	if len(results) != 1 || results[0].Status != StatusHashMismatch {
		t.Fatalf("expected HASH_MISMATCH, got %+v", results)
	}
}

// TestSegmentedFile verifies per-segment hashing kicks in above the
// threshold and that a corrupt segment is identified by index.
func TestSegmentedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a >1 GiB file; skip in -short")
	}
	root := t.TempDir()
	// Just over SegmentThreshold (1 GiB + 64 MiB so we get 17 segments).
	size := SegmentThreshold + SegmentSize
	writeBundleFile(t, root, "bodyc.0000.cdat", size)

	m, err := Build(root, BuildOptions{ChainID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 {
		t.Fatal("expected 1 file")
	}
	gotSegs := len(m.Files[0].Segments)
	wantSegs := int((size + SegmentSize - 1) / SegmentSize)
	if gotSegs != wantSegs {
		t.Fatalf("expected %d segments, got %d", wantSegs, gotSegs)
	}

	// Corrupt one byte in segment 3.
	path := filepath.Join(root, "chain", "freezer", "bodyc.0000.cdat")
	f, _ := os.OpenFile(path, os.O_WRONLY, 0o644)
	f.WriteAt([]byte{0xFF}, 3*SegmentSize+100)
	f.Close()

	results, _ := Verify(root, m, VerifyOptions{})
	if results[0].Status != StatusPartialMismatch {
		t.Fatalf("expected PARTIAL_MISMATCH, got %v", results[0].Status)
	}
	if len(results[0].BadSegments) != 1 || results[0].BadSegments[0] != 3 {
		t.Fatalf("expected BadSegments=[3], got %v", results[0].BadSegments)
	}
}

// TestSaveLoadRoundTrip verifies manifest JSON encoding/decoding.
func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "headerc.cidx", 512)
	m, err := Build(root, BuildOptions{ChainID: 1, BlockRange: BlockRange{Start: 0, End: 100}})
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(root, "bundle.manifest.json")
	if err := m.Save(manifestPath); err != nil {
		t.Fatal(err)
	}

	m2, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m2.ChainID != m.ChainID || m2.BlockRange != m.BlockRange {
		t.Errorf("round-trip mismatch: %+v vs %+v", m, m2)
	}
	if len(m2.Files) != len(m.Files) || m2.Files[0].Hash != m.Files[0].Hash {
		t.Errorf("file hash round-trip mismatch")
	}
}

// TestDeterministicBuild — running Build twice on the same dir must
// produce manifests with identical file ordering and hashes (multi-
// server reproduction is the trustless anchor; two servers MUST yield
// byte-identical manifests modulo GeneratedAt).
func TestDeterministicBuild(t *testing.T) {
	root := t.TempDir()
	writeBundleFile(t, root, "a.cidx", 1024)
	writeBundleFile(t, root, "b.cidx", 2048)
	writeBundleFile(t, root, "c.cidx", 512)

	m1, err := Build(root, BuildOptions{ChainID: 1})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Build(root, BuildOptions{ChainID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(m1.Files) != len(m2.Files) {
		t.Fatal("file count differs")
	}
	for i := range m1.Files {
		if m1.Files[i].Path != m2.Files[i].Path {
			t.Errorf("file order differs at %d: %s vs %s", i, m1.Files[i].Path, m2.Files[i].Path)
		}
		if m1.Files[i].Hash != m2.Files[i].Hash {
			t.Errorf("file hash differs for %s", m1.Files[i].Path)
		}
	}
}

// Discard is a no-op writer used to ensure paths compile.
var _ = bytes.NewBuffer
