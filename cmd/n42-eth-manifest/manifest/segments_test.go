package manifest

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestSelector_HandlesSegmentedSnapshot verifies that the
// `snapshot/accounts.*.idx` glob picks up per-segment files
// produced by the H.3 segmented writer.
//
// This is the H.3a guard: the selector must work both for the
// historical monolithic file AND for segmented files without
// any code change.
func TestSelector_HandlesSegmentedSnapshot(t *testing.T) {
	dir := t.TempDir()
	// Mix of monolithic AND segmented entries — the writer
	// migration emits segmented files but old archives may
	// still have monolithic ones.
	paths := []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/codes.cidx",
		// Monolithic (legacy):
		"snapshot/accounts.0-25099999.idx",
		"snapshot/accounts.0-25099999.ef",
		"snapshot/accounts.0-25099999.val.zst",
		// Segmented (H.3):
		"snapshot/accounts.0-999999.idx",
		"snapshot/accounts.0-999999.ef",
		"snapshot/accounts.0-999999.val.zst",
		"snapshot/accounts.1000000-1999999.idx",
		"snapshot/accounts.1000000-1999999.ef",
		"snapshot/accounts.1000000-1999999.val.zst",
		"snapshot/accounts.25000000-25199999.idx",
		"snapshot/accounts.25000000-25199999.ef",
		"snapshot/accounts.25000000-25199999.val.zst",
		// Storage analogues:
		"snapshot/storage.0-999999.idx",
		"snapshot/storage.0-999999.ef",
		"snapshot/storage.0-999999.val.zst",
	}
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	sel, _ := SelectorFor("minimal")
	files, err := WalkFiles(dir, sel)
	if err != nil {
		t.Fatalf("WalkFiles: %v", err)
	}

	got := make([]string, 0, len(files))
	for _, f := range files {
		got = append(got, f.Path)
	}
	sort.Strings(got)

	mustHave := []string{
		"snapshot/accounts.0-25099999.idx",
		"snapshot/accounts.0-999999.idx",
		"snapshot/accounts.1000000-1999999.idx",
		"snapshot/accounts.25000000-25199999.idx",
		"snapshot/storage.0-999999.idx",
	}
	for _, want := range mustHave {
		var found bool
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("selector should match segmented file %s; got %v", want, got)
		}
	}

	// Section grouping must still report state-accounts /
	// state-storage as present.
	present := make(map[string]struct{})
	for _, f := range files {
		present[f.Section] = struct{}{}
	}
	for _, s := range []string{"state-accounts", "state-storage"} {
		if _, ok := present[s]; !ok {
			t.Errorf("section %s missing after walking segmented snapshot", s)
		}
	}
}
