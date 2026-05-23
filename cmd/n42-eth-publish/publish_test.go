package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
	"github.com/n42blockchain/N42/cmd/n42-eth-snapshot/snapshot"
)

// TestIT4_PublishAndPrune: publish 6 releases + 12 deltas, then
// prune to keep last 2 releases + 3 deltas per mode. Assert
// surviving dirs match the retention policy.
func TestIT4_PublishAndPrune(t *testing.T) {
	root := t.TempDir()
	network := "testnet"

	// === Stage 1: publish 6 fake releases (heights 1M..6M, mode minimal) ===
	for h := uint64(1); h <= 6; h++ {
		archDir := makeArchive(t, h*1_000_000)
		publishReleaseFromArch(t, archDir, root, network, "minimal", h*1_000_000)
	}

	// === Stage 2: publish 5 deltas (1M→2M, 2M→3M, ..., 5M→6M) ===
	for h := uint64(1); h <= 5; h++ {
		fromH := h * 1_000_000
		toH := (h + 1) * 1_000_000
		deltaDir := makeDeltaDir(t, fromH, toH)
		publishDeltaFromTree(t, deltaDir, root, network, "minimal", fromH, toH)
	}

	// Sanity: index should have 6 releases + 5 deltas.
	idxPath := filepath.Join(root, network, "releases.json")
	idx := readIndexJSON(t, idxPath)
	if len(idx.Releases) != 6 {
		t.Errorf("expected 6 releases, got %d", len(idx.Releases))
	}
	if len(idx.Deltas) != 5 {
		t.Errorf("expected 5 deltas, got %d", len(idx.Deltas))
	}

	// === Stage 3: run prune (in-process) — keep 2 releases + 3 deltas ===
	pruneInProcess(t, root, network, 2, 3, false)

	// === Stage 4: assert ===
	// Surviving releases: heights 5M and 6M (latest 2).
	for _, h := range []uint64{5_000_000, 6_000_000} {
		p := filepath.Join(root, network, fmt.Sprintf("%d", h), "minimal")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected release %d to survive: %v", h, err)
		}
	}
	for _, h := range []uint64{1_000_000, 2_000_000, 3_000_000, 4_000_000} {
		p := filepath.Join(root, network, fmt.Sprintf("%d", h), "minimal")
		if _, err := os.Stat(p); err == nil {
			t.Errorf("expected release %d to be pruned, but still exists", h)
		}
	}

	// Surviving deltas: 3M→4M, 4M→5M, 5M→6M (top 3 by to_height).
	for _, name := range []string{"3000000-4000000", "4000000-5000000", "5000000-6000000"} {
		p := filepath.Join(root, network, "deltas", name, "minimal")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected delta %s to survive: %v", name, err)
		}
	}
	for _, name := range []string{"1000000-2000000", "2000000-3000000"} {
		p := filepath.Join(root, network, "deltas", name, "minimal")
		if _, err := os.Stat(p); err == nil {
			t.Errorf("expected delta %s to be pruned, but still exists", name)
		}
	}

	// Index reflects pruning.
	idx2 := readIndexJSON(t, idxPath)
	if len(idx2.Releases) != 2 {
		t.Errorf("post-prune index has %d releases, want 2", len(idx2.Releases))
	}
	if len(idx2.Deltas) != 3 {
		t.Errorf("post-prune index has %d deltas, want 3", len(idx2.Deltas))
	}
}

// TestIT4_PruneDryRunDoesNothing: prune with dry-run=true must
// leave all files and the index untouched.
func TestIT4_PruneDryRunDoesNothing(t *testing.T) {
	root := t.TempDir()
	network := "testnet"
	for h := uint64(1); h <= 4; h++ {
		archDir := makeArchive(t, h*1_000_000)
		publishReleaseFromArch(t, archDir, root, network, "minimal", h*1_000_000)
	}
	beforeIdx := readIndexJSON(t, filepath.Join(root, network, "releases.json"))
	beforeFiles := listFiles(t, root)

	pruneInProcess(t, root, network, 1, 1, true) // dry-run

	afterIdx := readIndexJSON(t, filepath.Join(root, network, "releases.json"))
	afterFiles := listFiles(t, root)

	if len(beforeIdx.Releases) != len(afterIdx.Releases) {
		t.Errorf("dry-run mutated index: %d → %d releases",
			len(beforeIdx.Releases), len(afterIdx.Releases))
	}
	if len(beforeFiles) != len(afterFiles) {
		t.Errorf("dry-run mutated filesystem: %d → %d files",
			len(beforeFiles), len(afterFiles))
	}
}

// --- helpers ---

func makeArchive(t *testing.T, height uint64) string {
	t.Helper()
	dir := t.TempDir()
	paths := []string{
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
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		// Include height in content so different releases have
		// distinct hashes.
		content := fmt.Sprintf("stub-%s@h=%d", p, height)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	if err := writeFakeManifestForTest(dir, "minimal", height); err != nil {
		t.Fatalf("writeFakeManifest: %v", err)
	}
	return dir
}

func makeDeltaDir(t *testing.T, fromH, toH uint64) string {
	t.Helper()
	dir := t.TempDir()
	// Tiny delta with 1 file.
	_ = os.MkdirAll(filepath.Join(dir, "chain", "freezer"), 0o755)
	_ = os.WriteFile(
		filepath.Join(dir, "chain", "freezer", "headerc.cidx"),
		[]byte(fmt.Sprintf("delta-%d-to-%d", fromH, toH)), 0o644)
	dm := snapshot.DeltaManifest{
		Network:           "testnet",
		FromHeight:        fromH,
		ToHeight:          toH,
		Mode:              "minimal",
		BasedOnManifestID: fmt.Sprintf("baseline-%d", fromH),
		ManifestID:        fmt.Sprintf("delta-%d-%d", fromH, toH),
		Files: []*manifest.FileEntry{{
			Path:       "chain/freezer/headerc.cidx",
			Section:    "headers",
			Size:       int64(len(fmt.Sprintf("delta-%d-to-%d", fromH, toH))),
			Blake2b256: fmt.Sprintf("hash-%d-%d", fromH, toH),
		}},
	}
	f, _ := os.Create(filepath.Join(dir, "delta-manifest-minimal.json"))
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(&dm)
	f.Close()
	return dir
}

// publishReleaseFromArch in-process port of `n42-eth-publish release`.
func publishReleaseFromArch(t *testing.T, src, root, network, mode string, height uint64) {
	t.Helper()
	man, err := snapshot.ManifestFor(src, mode)
	if err != nil {
		t.Fatalf("ManifestFor: %v", err)
	}
	dstDir := filepath.Join(root, network, fmt.Sprintf("%d", height), mode)
	_ = os.MkdirAll(dstDir, 0o755)
	for _, f := range man.Files {
		if err := transferFile(filepath.Join(src, f.Path), filepath.Join(dstDir, f.Path), false); err != nil {
			t.Fatalf("transfer %s: %v", f.Path, err)
		}
	}
	manRel := fmt.Sprintf("manifest-%s.json", mode)
	_ = transferFile(filepath.Join(src, manRel), filepath.Join(dstDir, manRel), false)
	updateIndex(root, network, func(idx *PublishedIndex) {
		setRelease(idx, mode, height, man.ManifestID, man.CreatedAt)
	})
}

// publishDeltaFromTree in-process port of `n42-eth-publish delta`.
func publishDeltaFromTree(t *testing.T, src, root, network, mode string, from, to uint64) {
	t.Helper()
	deltaRel := fmt.Sprintf("delta-manifest-%s.json", mode)
	f, err := os.Open(filepath.Join(src, deltaRel))
	if err != nil {
		t.Fatalf("open delta: %v", err)
	}
	var dm snapshot.DeltaManifest
	_ = json.NewDecoder(f).Decode(&dm)
	f.Close()
	dstDir := filepath.Join(root, network, "deltas",
		fmt.Sprintf("%d-%d", from, to), mode)
	_ = os.MkdirAll(dstDir, 0o755)
	for _, fe := range dm.Files {
		_ = transferFile(filepath.Join(src, fe.Path), filepath.Join(dstDir, fe.Path), false)
	}
	_ = transferFile(filepath.Join(src, deltaRel), filepath.Join(dstDir, deltaRel), false)
	updateIndex(root, network, func(idx *PublishedIndex) {
		appendDelta(idx, mode, from, to, dm.ManifestID, dm.CreatedAt)
	})
}

// pruneInProcess re-implements the runPrune subcommand body
// without spawning a subprocess.
func pruneInProcess(t *testing.T, root, network string, keepReleases, keepDeltas int, dryRun bool) {
	t.Helper()
	idx, err := readIndex(root, network)
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}

	keepRelease := make(map[string]map[uint64]struct{}, 3)
	for _, mode := range []string{"minimal", "full", "archive"} {
		var heights []uint64
		for _, r := range idx.Releases {
			if _, ok := r.Manifests[mode]; ok {
				heights = append(heights, r.Height)
			}
		}
		sort.Slice(heights, func(i, j int) bool { return heights[i] > heights[j] })
		set := make(map[uint64]struct{})
		for i := 0; i < keepReleases && i < len(heights); i++ {
			set[heights[i]] = struct{}{}
		}
		keepRelease[mode] = set
	}
	keepDelta := make(map[string]map[string]struct{}, 3)
	for _, mode := range []string{"minimal", "full", "archive"} {
		var deltas []*DeltaRef
		for _, d := range idx.Deltas {
			if d.Mode == mode {
				deltas = append(deltas, d)
			}
		}
		sort.Slice(deltas, func(i, j int) bool { return deltas[i].ToHeight > deltas[j].ToHeight })
		set := make(map[string]struct{})
		for i := 0; i < keepDeltas && i < len(deltas); i++ {
			set[fmt.Sprintf("%d-%d", deltas[i].FromHeight, deltas[i].ToHeight)] = struct{}{}
		}
		keepDelta[mode] = set
	}

	netRoot := filepath.Join(root, network)
	entries, _ := os.ReadDir(netRoot)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "deltas" {
			continue
		}
		h := parseUint(e.Name())
		if h == 0 {
			continue
		}
		modes, _ := os.ReadDir(filepath.Join(netRoot, e.Name()))
		for _, md := range modes {
			if !md.IsDir() {
				continue
			}
			mode := md.Name()
			if _, ok := keepRelease[mode][h]; ok {
				continue
			}
			if !dryRun {
				_ = os.RemoveAll(filepath.Join(netRoot, e.Name(), mode))
			}
		}
	}
	deltaRoot := filepath.Join(netRoot, "deltas")
	dentries, _ := os.ReadDir(deltaRoot)
	for _, d := range dentries {
		if !d.IsDir() {
			continue
		}
		modes, _ := os.ReadDir(filepath.Join(deltaRoot, d.Name()))
		for _, md := range modes {
			if !md.IsDir() {
				continue
			}
			mode := md.Name()
			if _, ok := keepDelta[mode][d.Name()]; ok {
				continue
			}
			if !dryRun {
				_ = os.RemoveAll(filepath.Join(deltaRoot, d.Name(), mode))
			}
		}
	}
	if !dryRun {
		updateIndex(root, network, func(idx *PublishedIndex) {
			var keptR []*ReleaseRef
			for _, r := range idx.Releases {
				for mode := range r.Manifests {
					if _, ok := keepRelease[mode][r.Height]; ok {
						keptR = append(keptR, r)
						break
					}
				}
			}
			idx.Releases = keptR
			var keptD []*DeltaRef
			for _, d := range idx.Deltas {
				if _, ok := keepDelta[d.Mode][fmt.Sprintf("%d-%d", d.FromHeight, d.ToHeight)]; ok {
					keptD = append(keptD, d)
				}
			}
			idx.Deltas = keptD
		})
	}
}

func readIndexJSON(t *testing.T, path string) *PublishedIndex {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("readIndexJSON: %v", err)
	}
	defer f.Close()
	var idx PublishedIndex
	if err := json.NewDecoder(f).Decode(&idx); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &idx
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// writeFakeManifestForTest inlines manifest writing (cannot import the
// internal test helper from cmd/n42-eth-snapshot/snapshot).
func writeFakeManifestForTest(dir, mode string, height uint64) error {
	sel, err := manifest.SelectorFor(mode)
	if err != nil {
		return err
	}
	files, err := manifest.WalkFiles(dir, sel)
	if err != nil {
		return err
	}
	for _, f := range files {
		h, err := snapshot.HashFile(filepath.Join(dir, f.Path))
		if err != nil {
			return err
		}
		f.Blake2b256 = h
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\t%d\t%s\n", f.Path, f.Size, f.Blake2b256)
	}
	man := manifest.Manifest{
		Network:    "testnet",
		Height:     height,
		Mode:       mode,
		ManifestID: hex.EncodeToString(h.Sum(nil)),
		Files:      files,
	}
	out := filepath.Join(dir, fmt.Sprintf("manifest-%s.json", mode))
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(&man)
}

// runtime import nudge so Go doesn't complain about unused
// when test file conditions edit out helpers.
var _ = runtime.GOOS
