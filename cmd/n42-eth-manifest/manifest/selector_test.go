package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// touchTree builds a fake archive datadir under t.TempDir() and
// touches the given relative paths.
func touchTree(t *testing.T, paths []string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdirall: %v", err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

func TestSelectorFor_UnknownModeErrors(t *testing.T) {
	if _, err := SelectorFor("turbo"); err == nil {
		t.Errorf("expected error for unknown mode")
	}
}

func TestWalkFiles_MinimalSelectsOnlyMinimal(t *testing.T) {
	root := touchTree(t, []string{
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
		// These belong to full/archive — must NOT show up.
		"chain/freezer/bodyc.cidx",
		"chain/freezer/bodyc.0000.cdat",
		"chain/freezer/witness.cidx",
		"chain/freezer/witness.0000.cdat",
		"chain/freezer/senders.cidx",
	})

	sel, _ := SelectorFor("minimal")
	files, err := WalkFiles(root, sel)
	if err != nil {
		t.Fatalf("WalkFiles: %v", err)
	}
	got := make([]string, len(files))
	for i, f := range files {
		got[i] = f.Path
	}
	sort.Strings(got)

	// minimal is now snapshot-ONLY; headers/codes/bodies are catch-up (runtime),
	// not bundled.
	want := []string{
		"snapshot/accounts.0-25099999.ef",
		"snapshot/accounts.0-25099999.idx",
		"snapshot/accounts.0-25099999.val.zst",
		"snapshot/storage.0-25099999.ef",
		"snapshot/storage.0-25099999.idx",
		"snapshot/storage.0-25099999.val.zst",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("minimal file set mismatch\n  got:  %v\n  want: %v", got, want)
	}
}

func TestWalkFiles_FullExcludesSendersAndWitness(t *testing.T) {
	root := touchTree(t, []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/bodyc.cidx",
		"chain/freezer/bodyc.0000.cdat",
		"chain/freezer/receipts.cidx",
		"chain/freezer/accthist.cidx",
		"chain/freezer/storhist.cidx",
		"chain/freezer/txindex.cidx",
		"chain/freezer/senders.cidx",          // must NOT be included
		"chain/freezer/senders.0000.cdat",     // must NOT be included
		"chain/freezer/witness.cidx",          // must NOT be included
		"chain/freezer/witness.0000.cdat",     // must NOT be included
	})
	sel, _ := SelectorFor("full")
	files, err := WalkFiles(root, sel)
	if err != nil {
		t.Fatalf("WalkFiles: %v", err)
	}
	for _, f := range files {
		if f.Path == "chain/freezer/senders.cidx" ||
			f.Path == "chain/freezer/senders.0000.cdat" {
			t.Errorf("full selector must not include senders: %s", f.Path)
		}
		if f.Path == "chain/freezer/witness.cidx" ||
			f.Path == "chain/freezer/witness.0000.cdat" {
			t.Errorf("full selector must not include witness: %s", f.Path)
		}
	}
}

func TestWithSenders_AddsSendersSection(t *testing.T) {
	root := touchTree(t, []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/senders.cidx",
		"chain/freezer/senders.0000.cdat",
	})
	sel := WithSenders(minimalSelector())
	files, err := WalkFiles(root, sel)
	if err != nil {
		t.Fatalf("WalkFiles: %v", err)
	}
	var sawSenders bool
	for _, f := range files {
		if f.Section == "senders" {
			sawSenders = true
		}
	}
	if !sawSenders {
		t.Errorf("WithSenders did not pick up senders files")
	}
	// Idempotency check.
	sel2 := WithSenders(sel)
	if len(sel2.Sections) != len(sel.Sections) {
		t.Errorf("WithSenders is not idempotent: %d vs %d sections",
			len(sel2.Sections), len(sel.Sections))
	}
}

// archive and full are NOT super/subset under the corrected bundles: full ships
// the state snapshot (and no witness/anchors); archive ships witness + anchors
// (and rebuilds state from them, so no snapshot). Both share headers/bodies/
// codes/txindex.
func TestWalkFiles_ArchiveVsFull(t *testing.T) {
	root := touchTree(t, []string{
		"chain/freezer/headerc.cidx",
		"chain/freezer/bodyc.cidx",
		"chain/freezer/codes.cidx",
		"chain/freezer/txindex.cidx",
		"chain/freezer/witness.cidx",
		"chain/freezer/witness.0000.cdat",
		"chain/freezer/anchorc.cidx",
		"chain/freezer/anchorc.0000.cdat",
		"snapshot/accounts.0-25099999.idx",
		"snapshot/accounts.0-25099999.ef",
		"snapshot/accounts.0-25099999.val.zst",
		"snapshot/storage.0-25099999.idx",
		"snapshot/storage.0-25099999.ef",
		"snapshot/storage.0-25099999.val.zst",
	})
	fullSel, _ := SelectorFor("full")
	archSel, _ := SelectorFor("archive")
	fullFiles, _ := WalkFiles(root, fullSel)
	archFiles, _ := WalkFiles(root, archSel)

	sect := func(files []*FileEntry) map[string]bool {
		m := map[string]bool{}
		for _, f := range files {
			m[f.Section] = true
		}
		return m
	}
	fs, as := sect(fullFiles), sect(archFiles)

	// full has snapshot, NOT witness/anchors.
	if !fs["state-accounts"] || !fs["state-storage"] {
		t.Errorf("full must ship the state snapshot")
	}
	if fs["witness"] || fs["anchors"] {
		t.Errorf("full must NOT ship witness/anchors")
	}
	// archive has witness + anchors, NOT snapshot.
	if !as["witness"] || !as["anchors"] {
		t.Errorf("archive must ship witness + anchors")
	}
	if as["state-accounts"] || as["state-storage"] {
		t.Errorf("archive must NOT ship the snapshot (rebuilds state from witness)")
	}
	// shared: headers/bodies/code/tx-index in both.
	for _, s := range []string{"headers", "bodies", "code", "tx-index"} {
		if !fs[s] || !as[s] {
			t.Errorf("both full and archive must ship %q (full=%v archive=%v)", s, fs[s], as[s])
		}
	}
}

func TestMissingSections_ReportsGaps(t *testing.T) {
	root := touchTree(t, []string{
		"chain/freezer/headerc.cidx",
		// All snapshot/state files missing.
	})
	sel, _ := SelectorFor("minimal")
	missing, err := MissingSections(root, sel)
	if err != nil {
		t.Fatalf("MissingSections: %v", err)
	}
	// minimal = snapshot + caplin checkpoint seed; both state sections and the
	// beacon-checkpoint seed are missing here (only headerc present).
	wantMissing := map[string]bool{"state-accounts": true, "state-storage": true, "beacon-checkpoint": true}
	for _, m := range missing {
		delete(wantMissing, m)
	}
	if len(wantMissing) > 0 {
		t.Errorf("expected sections missing but not reported: %v", wantMissing)
	}
}
