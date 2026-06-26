package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

// touchFakeArchive creates a small fake archive datadir suitable for
// running the manifest writer on, then returns its path. Optional
// extras can be supplied for tier-specific tests.
func touchFakeArchive(t *testing.T, extras ...string) string {
	t.Helper()
	dir := t.TempDir()
	base := []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/headerc.0000.cdat",
		"chain/freezer/codes.cidx",
		"chain/freezer/codes.0000.cdat",
		"snapshot/accounts.0-25099999.idx",
		"snapshot/accounts.0-25099999.ef",
		"snapshot/accounts.0-25099999.val.zst",
		"snapshot/storage.0-25099999.idx",
		"snapshot/storage.0-25099999.ef",
		"snapshot/storage.0-25099999.val.zst",
	}
	for _, p := range append(base, extras...) {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("stub-"+p), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return dir
}

func TestDetectMode_Minimal(t *testing.T) {
	dir := touchFakeArchive(t)
	d, err := DetectMode(dir)
	if err != nil {
		t.Fatalf("DetectMode: %v", err)
	}
	if d.Mode != "minimal" {
		t.Errorf("detected mode = %s, want minimal", d.Mode)
	}
	if !d.Intact {
		t.Errorf("minimal should be intact")
	}
}

func TestDetectMode_Archive(t *testing.T) {
	dir := touchFakeArchive(t,
		"chain/freezer/bodyc.cidx",
		"chain/freezer/bodyc.0000.cdat",
		"chain/freezer/txindex.cidx",
		"chain/freezer/witness.cidx",
		"chain/freezer/witness.0000.cdat",
		"chain/freezer/anchorc.cidx",
		"chain/freezer/anchorc.0000.cdat",
		"chain/freezer/anchorc.blocks",
	)
	d, err := DetectMode(dir)
	if err != nil {
		t.Fatalf("DetectMode: %v", err)
	}
	if d.Mode != "archive" {
		t.Errorf("detected mode = %s, want archive", d.Mode)
	}
}

func TestDetectMode_Partial(t *testing.T) {
	dir := t.TempDir()
	// Only headers, no code / no state — not even minimal.
	_ = os.MkdirAll(filepath.Join(dir, "chain", "freezer"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "chain/freezer/headerc.cidx"), []byte("x"), 0o644)
	d, _ := DetectMode(dir)
	if d.Mode != "" {
		t.Errorf("partial archive should report mode='', got %s", d.Mode)
	}
	if d.Intact {
		t.Errorf("partial archive should not be intact")
	}
	if len(d.MissingSections) == 0 {
		t.Errorf("expected MissingSections to be populated")
	}
}

// E2E pipeline: build a manifest, copy archive to a sibling dst dir
// via Fetch using a LocalSource, then Verify it.
func TestFetchAndVerifyRoundTrip(t *testing.T) {
	// 1) Build a fake src archive.
	src := touchFakeArchive(t)

	// 2) Write a manifest for it using the manifest package.
	if err := writeFakeManifest(t, src, "minimal"); err != nil {
		t.Fatalf("writeFakeManifest: %v", err)
	}

	// 3) Fetch into a fresh dst dir.
	dst := t.TempDir()
	rep, err := Fetch("file://"+src, dst, "minimal", false, false, 2)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !rep.OK {
		t.Errorf("Fetch reported FAIL: %+v", rep)
	}
	if rep.Downloaded != rep.TotalFiles {
		t.Errorf("expected to download all %d files, got %d", rep.TotalFiles, rep.Downloaded)
	}

	// 4) Verify dst against its now-present manifest.
	vrep, err := Verify(dst, "", 2)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vrep.OK {
		t.Errorf("Verify reported FAIL:\n  missing: %v\n  mismatches: %v",
			vrep.MissingFiles, vrep.Mismatches)
	}
	if vrep.Mode != "minimal" {
		t.Errorf("Verify mode = %s, want minimal", vrep.Mode)
	}
}

// TestFetch_SkipsExisting verifies that running Fetch twice
// reports the second run as fully-skipped.
func TestFetch_SkipsExisting(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifest(t, src, "minimal"); err != nil {
		t.Fatalf("writeFakeManifest: %v", err)
	}
	dst := t.TempDir()
	_, _ = Fetch("file://"+src, dst, "minimal", false, false, 2)
	rep, _ := Fetch("file://"+src, dst, "minimal", false, false, 2)
	if rep.Downloaded != 0 {
		t.Errorf("second Fetch downloaded %d files; expected 0", rep.Downloaded)
	}
	if rep.AlreadyOK != rep.TotalFiles {
		t.Errorf("expected all %d files marked already-ok, got %d",
			rep.TotalFiles, rep.AlreadyOK)
	}
}

// TestVerify_DetectsCorruption: flip a byte and Verify should fail.
func TestVerify_DetectsCorruption(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifest(t, src, "minimal"); err != nil {
		t.Fatalf("writeFakeManifest: %v", err)
	}
	dst := t.TempDir()
	_, _ = Fetch("file://"+src, dst, "minimal", false, false, 2)

	// Corrupt one file that is actually part of the minimal tier (the minimal
	// snapshot ships state, not headers — headers are fetched live).
	target := filepath.Join(dst, "snapshot", "accounts.0-25099999.idx")
	if err := os.WriteFile(target, []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	rep, _ := Verify(dst, "", 2)
	if rep.OK {
		t.Errorf("Verify should have FAILED on corrupted file")
	}
	found := false
	for _, p := range rep.Mismatches {
		if filepath.ToSlash(p) == "snapshot/accounts.0-25099999.idx — blake2b mismatch" ||
			(len(p) > 0 && p[0] == 's') { // path-prefix check
			found = true
			break
		}
	}
	// Either Mismatches or WrongSize will catch it.
	if !found && len(rep.WrongSize) == 0 {
		t.Errorf("expected Mismatches or WrongSize entry for corrupted file; got\n  mismatches=%v\n  wrongsize=%v",
			rep.Mismatches, rep.WrongSize)
	}
}

// TestDowngrade_IdentifiesRedundant: archive datadir, downgrade to
// minimal — bodies/witness/etc. should be flagged for removal.
func TestDowngrade_IdentifiesRedundant(t *testing.T) {
	src := touchFakeArchive(t,
		"chain/freezer/bodyc.cidx",
		"chain/freezer/witness.cidx",
		"chain/freezer/witness.0000.cdat",
	)
	if err := writeFakeManifest(t, src, "archive"); err != nil {
		t.Fatalf("writeFakeManifest archive: %v", err)
	}
	if err := writeFakeManifest(t, src, "minimal"); err != nil {
		t.Fatalf("writeFakeManifest minimal: %v", err)
	}

	rep, err := Downgrade(src, "minimal", false)
	if err != nil {
		t.Fatalf("Downgrade: %v", err)
	}
	if len(rep.Removed) == 0 {
		t.Errorf("expected files to remove, got 0")
	}
	// dry-run leaves files intact.
	for _, p := range rep.Removed {
		full := filepath.Join(src, filepath.FromSlash(p))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			t.Errorf("dry-run removed %s", p)
		}
	}
}
