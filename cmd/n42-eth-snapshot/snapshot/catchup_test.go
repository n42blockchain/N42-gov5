package snapshot

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestCatchUp_NoOpWhenCurrent: client already at publisher latest,
// catch-up returns with Iterations=0.
func TestCatchUp_NoOpWhenCurrent(t *testing.T) {
	src := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, src, "minimal", 25000); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	m, _ := ManifestFor(src, "minimal")
	mirror := t.TempDir()
	publishFakeMirror(t, src, mirror, "simnet", "minimal", m)

	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "25000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := CatchUp(client, "file://"+filepath.Join(mirror, "simnet"), "minimal", 0)
	if err != nil {
		t.Fatalf("CatchUp: %v", err)
	}
	if rep.Iterations != 0 {
		t.Errorf("Iterations=%d want 0", rep.Iterations)
	}
	if rep.FinalHeight != 25000 {
		t.Errorf("FinalHeight=%d want 25000", rep.FinalHeight)
	}
	if !rep.UpToDate {
		t.Errorf("UpToDate=false want true")
	}
}

// TestCatchUp_AppliesSingleDelta: publisher at H+1000, client at H.
// catch-up applies one delta and converges.
func TestCatchUp_AppliesSingleDelta(t *testing.T) {
	srcA := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, srcA, "minimal", 24000); err != nil {
		t.Fatalf("manifest A: %v", err)
	}
	mA, _ := ManifestFor(srcA, "minimal")

	srcB := touchFakeArchive(t)
	_ = os.WriteFile(filepath.Join(srcB, "chain/freezer/headerc.cidx"),
		[]byte("updated-for-25000"), 0o644)
	if err := writeFakeManifestWithHeight(t, srcB, "minimal", 25000); err != nil {
		t.Fatalf("manifest B: %v", err)
	}
	mB, _ := ManifestFor(srcB, "minimal")

	mirror := t.TempDir()
	publishFakeMirror(t, srcA, mirror, "simnet", "minimal", mA)
	publishFakeMirror(t, srcB, mirror, "simnet", "minimal", mB)
	// Build delta 24000→25000 and write it to the mirror's deltas/
	// subdir alongside a copy of mB's manifest (CatchUp needs both).
	deltaSrc := t.TempDir()
	if err := buildDeltaTree(t, srcA, srcB, "minimal", deltaSrc); err != nil {
		t.Fatalf("buildDeltaTree: %v", err)
	}
	deltaDst := filepath.Join(mirror, "simnet", "deltas", "24000-25000", "minimal")
	if err := os.MkdirAll(deltaDst, 0o755); err != nil {
		t.Fatalf("mkdir delta: %v", err)
	}
	if err := copyTree(deltaSrc, deltaDst); err != nil {
		t.Fatalf("copy delta: %v", err)
	}
	// Stage the target manifest in the delta dir (ApplyDelta needs it).
	if err := copyTestFile(
		filepath.Join(srcB, "manifest-minimal.json"),
		filepath.Join(deltaDst, "manifest-minimal.json")); err != nil {
		t.Fatalf("stage target manifest: %v", err)
	}
	// Append delta to releases.json so CatchUp can discover it.
	appendDeltaToMirror(t, mirror, "simnet", "minimal", 24000, 25000, mB.ManifestID)

	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "24000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := CatchUp(client, "file://"+filepath.Join(mirror, "simnet"), "minimal", 5)
	if err != nil {
		t.Fatalf("CatchUp: %v\n%+v", err, rep)
	}
	if rep.Iterations != 1 {
		t.Errorf("Iterations=%d want 1", rep.Iterations)
	}
	if rep.FinalHeight != 25000 {
		t.Errorf("FinalHeight=%d want 25000", rep.FinalHeight)
	}
	if !rep.UpToDate {
		t.Errorf("UpToDate=false")
	}

	// Verify the client now reads at the new manifest_id.
	m, _ := ManifestFor(client, "minimal")
	if m.ManifestID != mB.ManifestID {
		t.Errorf("client manifest_id=%s want %s", m.ManifestID, mB.ManifestID)
	}
}

// TestCatchUp_ChainsTwoDeltas: H → H+1000 → H+2000 via two
// successive deltas. catch-up iterates 2 times.
func TestCatchUp_ChainsTwoDeltas(t *testing.T) {
	stages := []struct {
		h   uint64
		bump string
	}{
		{23000, "v1"},
		{24000, "v2"},
		{25000, "v3"},
	}
	srcs := make([]string, len(stages))
	mans := make([]string, len(stages))

	for i, s := range stages {
		dir := touchFakeArchive(t)
		_ = os.WriteFile(filepath.Join(dir, "chain/freezer/headerc.cidx"),
			[]byte(s.bump), 0o644)
		if err := writeFakeManifestWithHeight(t, dir, "minimal", s.h); err != nil {
			t.Fatalf("manifest %d: %v", s.h, err)
		}
		m, _ := ManifestFor(dir, "minimal")
		srcs[i] = dir
		mans[i] = m.ManifestID
	}

	mirror := t.TempDir()
	for i, s := range stages {
		m, _ := ManifestFor(srcs[i], "minimal")
		publishFakeMirror(t, srcs[i], mirror, "simnet", "minimal", m)
		if i > 0 {
			d := t.TempDir()
			if err := buildDeltaTree(t, srcs[i-1], srcs[i], "minimal", d); err != nil {
				t.Fatalf("buildDeltaTree %d→%d: %v", stages[i-1].h, s.h, err)
			}
			dst := filepath.Join(mirror, "simnet", "deltas",
				formatRange(stages[i-1].h, s.h), "minimal")
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := copyTree(d, dst); err != nil {
				t.Fatalf("copy: %v", err)
			}
			if err := copyTestFile(
				filepath.Join(srcs[i], "manifest-minimal.json"),
				filepath.Join(dst, "manifest-minimal.json")); err != nil {
				t.Fatalf("stage tgt manifest: %v", err)
			}
			appendDeltaToMirror(t, mirror, "simnet", "minimal",
				stages[i-1].h, s.h, mans[i])
		}
	}

	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "23000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := CatchUp(client, "file://"+filepath.Join(mirror, "simnet"), "minimal", 5)
	if err != nil {
		t.Fatalf("CatchUp: %v\n%+v", err, rep)
	}
	if rep.Iterations != 2 {
		t.Errorf("Iterations=%d want 2", rep.Iterations)
	}
	if rep.FinalHeight != 25000 {
		t.Errorf("FinalHeight=%d want 25000", rep.FinalHeight)
	}
	if !rep.UpToDate {
		t.Errorf("UpToDate=false")
	}
}

// TestCatchUp_RespectsMaxIterations: a longer chain with a low
// --max-iterations stops early without error and reports incomplete.
func TestCatchUp_RespectsMaxIterations(t *testing.T) {
	// Two-step chain but max=1.
	srcA := touchFakeArchive(t)
	if err := writeFakeManifestWithHeight(t, srcA, "minimal", 1000); err != nil {
		t.Fatalf(": %v", err)
	}
	mA, _ := ManifestFor(srcA, "minimal")
	srcB := touchFakeArchive(t)
	_ = os.WriteFile(filepath.Join(srcB, "chain/freezer/headerc.cidx"), []byte("B"), 0o644)
	if err := writeFakeManifestWithHeight(t, srcB, "minimal", 2000); err != nil {
		t.Fatalf(": %v", err)
	}
	mB, _ := ManifestFor(srcB, "minimal")
	srcC := touchFakeArchive(t)
	_ = os.WriteFile(filepath.Join(srcC, "chain/freezer/headerc.cidx"), []byte("C"), 0o644)
	if err := writeFakeManifestWithHeight(t, srcC, "minimal", 3000); err != nil {
		t.Fatalf(": %v", err)
	}
	mC, _ := ManifestFor(srcC, "minimal")

	mirror := t.TempDir()
	publishFakeMirror(t, srcA, mirror, "simnet", "minimal", mA)
	publishFakeMirror(t, srcB, mirror, "simnet", "minimal", mB)
	publishFakeMirror(t, srcC, mirror, "simnet", "minimal", mC)

	for _, pair := range [][3]uint64{{1000, 2000}, {2000, 3000}} {
		fromIdx, toIdx, targetH := pair[0], pair[1], pair[1]
		from, to := stageBySim(srcA, srcB, srcC, fromIdx), stageBySim(srcA, srcB, srcC, toIdx)
		mid := mB.ManifestID
		if targetH == 3000 {
			mid = mC.ManifestID
		}
		d := t.TempDir()
		if err := buildDeltaTree(t, from, to, "minimal", d); err != nil {
			t.Fatalf("buildDeltaTree: %v", err)
		}
		dst := filepath.Join(mirror, "simnet", "deltas", formatRange(fromIdx, toIdx), "minimal")
		_ = os.MkdirAll(dst, 0o755)
		if err := copyTree(d, dst); err != nil {
			t.Fatalf(": %v", err)
		}
		_ = copyTestFile(filepath.Join(to, "manifest-minimal.json"),
			filepath.Join(dst, "manifest-minimal.json"))
		appendDeltaToMirror(t, mirror, "simnet", "minimal", fromIdx, toIdx, mid)
	}

	client := t.TempDir()
	_, _ = Fetch("file://"+filepath.Join(mirror, "simnet", "1000", "minimal"),
		client, "minimal", false, false, 2)

	rep, err := CatchUp(client, "file://"+filepath.Join(mirror, "simnet"), "minimal", 1)
	if err != nil {
		t.Fatalf("CatchUp: %v\n%+v", err, rep)
	}
	if rep.Iterations != 1 {
		t.Errorf("Iterations=%d want 1 (max)", rep.Iterations)
	}
	if rep.FinalHeight != 2000 {
		t.Errorf("FinalHeight=%d want 2000 (one delta applied)", rep.FinalHeight)
	}
	if rep.UpToDate {
		t.Errorf("UpToDate=true but max-iterations should have stopped early")
	}
}

// --- helpers ---

func stageBySim(a, b, c string, h uint64) string {
	switch h {
	case 1000:
		return a
	case 2000:
		return b
	case 3000:
		return c
	}
	return ""
}

func formatRange(from, to uint64) string {
	return fmtHeight(from) + "-" + fmtHeight(to)
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		d := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(d, 0o755)
		}
		return copyTestFile(p, d)
	})
}

// appendDeltaToMirror appends a DeltaRef entry into the mirror's
// releases.json so CatchUp can find it.
func appendDeltaToMirror(t *testing.T, mirror, network, mode string, from, to uint64, manID string) {
	t.Helper()
	type deltaRef struct {
		FromHeight uint64 `json:"from_height"`
		ToHeight   uint64 `json:"to_height"`
		Mode       string `json:"mode"`
		ManifestID string `json:"manifest_id"`
		CreatedAt  string `json:"created_at"`
	}
	idxPath := filepath.Join(mirror, network, "releases.json")
	var idx struct {
		Network  string                       `json:"network"`
		Latest   map[string]*publishedRel     `json:"latest"`
		Releases []*publishedRel              `json:"releases"`
		Deltas   []*deltaRef                  `json:"deltas"`
		Updated  string                       `json:"updated_at"`
	}
	if data, err := os.ReadFile(idxPath); err == nil {
		_ = jsonUnmarshal(data, &idx)
	}
	if idx.Network == "" {
		idx.Network = network
	}
	idx.Deltas = append(idx.Deltas, &deltaRef{
		FromHeight: from, ToHeight: to, Mode: mode, ManifestID: manID,
	})
	f, _ := os.Create(idxPath)
	defer f.Close()
	_ = jsonEncode(f, &idx)
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
func jsonEncode(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
