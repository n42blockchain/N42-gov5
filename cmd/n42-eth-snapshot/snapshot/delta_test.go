package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

// IT-2: full delta round-trip
// Build archive A and B, write delta A→B, apply on client at A,
// verify client manifest_id ends up matching B's.
func TestIT2_DeltaApply_RoundTrip(t *testing.T) {
	// === Setup baseline archive A ===
	archA := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, archA, "minimal", 1000000); err != nil {
		t.Fatalf("write manifest A: %v", err)
	}
	mA, _ := ManifestFor(archA, "minimal")

	// === Setup target archive B ===
	archB := touchFakeArchive(t)
	// Mutate a file in B.
	if err := os.WriteFile(
		filepath.Join(archB, "chain", "freezer", "headerc.cidx"),
		[]byte("UPDATED-content-headerc.cidx"), 0o644); err != nil {
		t.Fatalf("mutate B: %v", err)
	}
	// Add a new file in B.
	if err := os.WriteFile(
		filepath.Join(archB, "chain", "freezer", "headerc.0001.cdat"),
		[]byte("new-rotated-cdat-segment"), 0o644); err != nil {
		t.Fatalf("add B: %v", err)
	}
	if err := writeFakeManifestWithHeight(t, archB, "minimal", 2000000); err != nil {
		t.Fatalf("write manifest B: %v", err)
	}
	mB, _ := ManifestFor(archB, "minimal")
	if mA.ManifestID == mB.ManifestID {
		t.Fatalf("test setup bug: A and B have same manifest_id")
	}

	// === Build the delta A→B as a publishing tree ===
	deltaDir := t.TempDir()
	if err := buildDeltaTree(t, archA, archB, "minimal", deltaDir); err != nil {
		t.Fatalf("buildDeltaTree: %v", err)
	}

	// === Client starts at A ===
	client := t.TempDir()
	rep, err := Fetch("file://"+archA, client, "minimal", false, false, 2)
	if err != nil || !rep.OK {
		t.Fatalf("Fetch baseline: err=%v rep=%+v", err, rep)
	}

	// === Plan delta — should be applicable ===
	plan, _, err := PlanDelta("file://"+deltaDir, client, "minimal")
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}
	if !plan.Applicable {
		t.Fatalf("delta should be applicable; reason: %s", plan.Reason)
	}
	if plan.FromHeight != 1000000 || plan.ToHeight != 2000000 {
		t.Errorf("plan from/to = %d/%d, want 1000000/2000000", plan.FromHeight, plan.ToHeight)
	}
	if plan.FilesToFetch != 2 {
		t.Errorf("plan files = %d, want 2 (1 mutated + 1 new)", plan.FilesToFetch)
	}

	// === Apply ===
	// The delta publisher also stages the full target manifest at
	// its root so apply can install it. Copy archB's manifest into
	// the delta tree before apply.
	srcMan := filepath.Join(archB, "manifest-minimal.json")
	dstMan := filepath.Join(deltaDir, "manifest-minimal.json")
	if err := copyTestFile(srcMan, dstMan); err != nil {
		t.Fatalf("stage target manifest: %v", err)
	}

	areport, err := ApplyDelta("file://"+deltaDir, client, "minimal", 2)
	if err != nil {
		t.Fatalf("ApplyDelta: %v\n%+v", err, areport)
	}
	if !areport.OK {
		t.Fatalf("apply not OK: %+v", areport)
	}
	if areport.Downloaded != 2 {
		t.Errorf("downloaded = %d, want 2", areport.Downloaded)
	}

	// === Verify client is now at B ===
	mFinal, err := ManifestFor(client, "minimal")
	if err != nil {
		t.Fatalf("read client manifest after apply: %v", err)
	}
	if mFinal.ManifestID != mB.ManifestID {
		t.Errorf("client manifest_id = %s, want %s (B)", mFinal.ManifestID, mB.ManifestID)
	}
	if mFinal.Height != 2000000 {
		t.Errorf("client height = %d, want 2000000", mFinal.Height)
	}
	vrep, _ := Verify(client, "", 2)
	if !vrep.OK {
		t.Errorf("post-apply Verify failed: %+v", vrep)
	}
}

// IT-3: delta refuses wrong baseline
func TestIT3_DeltaApply_WrongBaseline(t *testing.T) {
	archA := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, archA, "minimal", 1000000); err != nil {
		t.Fatalf("write A: %v", err)
	}
	archB := touchFakeArchive(t)
	if err := os.WriteFile(filepath.Join(archB, "chain/freezer/headerc.cidx"),
		[]byte("X"), 0o644); err != nil {
		t.Fatalf("mutate B: %v", err)
	}
	if err := writeFakeManifestWithHeight(t, archB, "minimal", 2000000); err != nil {
		t.Fatalf("write B: %v", err)
	}
	deltaDir := t.TempDir()
	if err := buildDeltaTree(t, archA, archB, "minimal", deltaDir); err != nil {
		t.Fatalf("buildDeltaTree: %v", err)
	}

	// Client has a DIFFERENT baseline (archC with its own manifest_id).
	archC := touchFakeArchive(t)
	if err := os.WriteFile(filepath.Join(archC, "chain/freezer/codes.cidx"),
		[]byte("totally-different-C"), 0o644); err != nil {
		t.Fatalf("mutate C: %v", err)
	}
	if err := writeFakeManifestWithHeight(t, archC, "minimal", 1500000); err != nil {
		t.Fatalf("write C: %v", err)
	}
	client := t.TempDir()
	_, _ = Fetch("file://"+archC, client, "minimal", false, false, 2)

	plan, _, err := PlanDelta("file://"+deltaDir, client, "minimal")
	if err != nil {
		t.Fatalf("PlanDelta: %v", err)
	}
	if plan.Applicable {
		t.Errorf("delta should NOT be applicable for wrong-baseline client")
	}
	if plan.Reason == "" {
		t.Errorf("plan.Reason should be populated when not applicable")
	}

	// Attempting apply should fail loud and not touch the manifest.
	mBefore, _ := ManifestFor(client, "minimal")
	_, err = ApplyDelta("file://"+deltaDir, client, "minimal", 2)
	if err == nil {
		t.Errorf("ApplyDelta should error on wrong baseline")
	}
	mAfter, _ := ManifestFor(client, "minimal")
	if mBefore.ManifestID != mAfter.ManifestID {
		t.Errorf("manifest was mutated despite failed apply")
	}
}

// IT-5: snapshot segmentation produces a small delta
// When most snapshot segments are identical between two archives,
// the delta should only contain the few that changed.
func TestIT5_SegmentedSnapshot_SmallDelta(t *testing.T) {
	// Build two archives that share most snapshot segments.
	common := []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/headerc.0000.cdat",
		"chain/freezer/codes.cidx",
		"chain/freezer/codes.0000.cdat",
		// Five identical snapshot segments + 1 that will differ in B.
		"snapshot/accounts.0-999999.idx",
		"snapshot/accounts.0-999999.ef",
		"snapshot/accounts.0-999999.val.zst",
		"snapshot/accounts.1000000-1999999.idx",
		"snapshot/accounts.1000000-1999999.ef",
		"snapshot/accounts.1000000-1999999.val.zst",
		"snapshot/accounts.2000000-2999999.idx",
		"snapshot/accounts.2000000-2999999.ef",
		"snapshot/accounts.2000000-2999999.val.zst",
		"snapshot/storage.0-999999.idx",
		"snapshot/storage.0-999999.ef",
		"snapshot/storage.0-999999.val.zst",
	}
	makeArch := func(extras []string) string {
		dir := t.TempDir()
		for _, p := range append(append([]string{}, common...), extras...) {
			full := filepath.Join(dir, filepath.FromSlash(p))
			_ = os.MkdirAll(filepath.Dir(full), 0o755)
			_ = os.WriteFile(full, []byte("stub-"+p), 0o644)
		}
		return dir
	}

	archA := makeArch(nil)
	if err := writeFakeManifestWithHeight(t, archA, "minimal", 2000000); err != nil {
		t.Fatalf("manifest A: %v", err)
	}
	mA, _ := ManifestFor(archA, "minimal")

	// B adds a brand-new tail segment + extends the previously
	// last one (mutates).
	archB := makeArch([]string{
		"snapshot/accounts.3000000-3099999.idx",
		"snapshot/accounts.3000000-3099999.ef",
		"snapshot/accounts.3000000-3099999.val.zst",
	})
	// Mutate the last existing segment in B.
	for _, suffix := range []string{"idx", "ef", "val.zst"} {
		p := filepath.Join(archB, "snapshot",
			"accounts.2000000-2999999."+suffix)
		if err := os.WriteFile(p, []byte("UPDATED-"+suffix), 0o644); err != nil {
			t.Fatalf("mutate tail seg: %v", err)
		}
	}
	if err := writeFakeManifestWithHeight(t, archB, "minimal", 3100000); err != nil {
		t.Fatalf("manifest B: %v", err)
	}
	mB, _ := ManifestFor(archB, "minimal")

	deltaDir := t.TempDir()
	if err := buildDeltaTree(t, archA, archB, "minimal", deltaDir); err != nil {
		t.Fatalf("buildDeltaTree: %v", err)
	}

	// Read delta manifest and inspect what's in it.
	dmF, err := os.Open(filepath.Join(deltaDir, "delta-manifest-minimal.json"))
	if err != nil {
		t.Fatalf("open delta manifest: %v", err)
	}
	var dm DeltaManifest
	if err := json.NewDecoder(dmF).Decode(&dm); err != nil {
		t.Fatalf("decode delta manifest: %v", err)
	}
	dmF.Close()

	// Delta should only carry:
	//   - 3 new segment files (3000000-3099999.{idx,ef,val.zst})
	//   - 3 mutated segment files (2000000-2999999.{idx,ef,val.zst})
	// = 6 files. The 8 unchanged segments and 4 non-snapshot
	// files must NOT appear.
	if len(dm.Files) != 6 {
		t.Errorf("delta should carry 6 files (3 new + 3 mutated); got %d",
			len(dm.Files))
		for _, f := range dm.Files {
			t.Logf("  delta file: %s", f.Path)
		}
	}

	// Verify no unchanged-segment files snuck in.
	for _, f := range dm.Files {
		if f.Path == "snapshot/accounts.0-999999.idx" ||
			f.Path == "snapshot/accounts.1000000-1999999.idx" ||
			f.Path == "snapshot/storage.0-999999.idx" {
			t.Errorf("delta should NOT include unchanged segment %s", f.Path)
		}
	}
	// Sanity check on the manifest baseline pointer.
	if dm.BasedOnManifestID != mA.ManifestID {
		t.Errorf("delta based_on = %s, want A's manifest_id %s",
			dm.BasedOnManifestID, mA.ManifestID)
	}
	if mB.ManifestID == "" {
		t.Errorf("manifest B id is empty")
	}
}

// --- test helpers ---

func writeFakeManifestWithHeight(t *testing.T, dir, mode string, height uint64) error {
	t.Helper()
	if err := writeFakeManifest(t, dir, mode); err != nil {
		return err
	}
	path := filepath.Join(dir, "manifest-"+mode+".json")
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var m manifest.Manifest
	_ = json.NewDecoder(f).Decode(&m)
	f.Close()
	m.Height = height
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(&m)
}

func copyTestFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// buildDeltaTree is a thin in-process port of cmd/n42-eth-delta-build
// so this test doesn't need to shell out.
func buildDeltaTree(t *testing.T, fromArch, toArch, mode, outDir string) error {
	t.Helper()
	fromMan, err := ManifestFor(fromArch, mode)
	if err != nil {
		return err
	}
	toMan, err := ManifestFor(toArch, mode)
	if err != nil {
		return err
	}
	fromIdx := make(map[string]string, len(fromMan.Files))
	for _, f := range fromMan.Files {
		fromIdx[f.Path] = f.Blake2b256
	}
	var deltaFiles []*manifest.FileEntry
	for _, f := range toMan.Files {
		if prev, ok := fromIdx[f.Path]; ok && prev == f.Blake2b256 {
			continue
		}
		deltaFiles = append(deltaFiles, f)
		src := filepath.Join(toArch, f.Path)
		dst := filepath.Join(outDir, f.Path)
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := copyTestFile(src, dst); err != nil {
			return err
		}
	}
	sort.Slice(deltaFiles, func(i, j int) bool {
		return deltaFiles[i].Path < deltaFiles[j].Path
	})
	dm := DeltaManifest{
		Network:            toMan.Network,
		FromHeight:         fromMan.Height,
		ToHeight:           toMan.Height,
		Mode:               mode,
		BasedOnManifestID:  fromMan.ManifestID,
		ManifestID:         toMan.ManifestID, // not strictly the delta's id, but unique enough
		Files:              deltaFiles,
	}
	f, err := os.Create(filepath.Join(outDir, "delta-manifest-"+mode+".json"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(&dm)
}
