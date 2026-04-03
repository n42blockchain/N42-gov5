package ethel

import (
	"os"
	"testing"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func TestGenerateSegments(t *testing.T) {
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Empty freezer → no segments.
	m := GenerateSegments(f, 500000)
	if len(m.Segments) != 0 {
		t.Errorf("empty freezer: got %d segments, want 0", len(m.Segments))
	}

	// Add 1M blocks.
	for i := uint64(0); i < 10; i++ {
		data := &freezer.FreezeData{
			Headers:    [][]byte{{}},
			Bodies:     [][]byte{{}},
			Receipts:   [][]byte{{}},
			Hashes:     [][]byte{make([]byte, 32)},
			Difficulty: [][]byte{{}},
		}
		if err := f.Freeze(i, data); err != nil {
			t.Fatal(err)
		}
	}

	m2 := GenerateSegments(f, 3)
	if len(m2.Segments) != 4 { // ceil(10/3) = 4
		t.Errorf("10 blocks / 3 seg: got %d segments, want 4", len(m2.Segments))
	}
	if m2.Segments[0].Start != 0 || m2.Segments[0].End != 2 {
		t.Errorf("seg0: %d-%d, want 0-2", m2.Segments[0].Start, m2.Segments[0].End)
	}
}

func TestManifestWriteRead(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{
		ChainID: 1,
		Segments: []SegmentInfo{
			{Start: 0, End: 499999, Tables: []string{"headers", "bodies"}},
		},
	}
	if err := WriteManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	m2, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.ChainID != 1 || len(m2.Segments) != 1 {
		t.Errorf("manifest mismatch: chainID=%d segs=%d", m2.ChainID, len(m2.Segments))
	}

	// Cleanup check
	if _, err := os.Stat(dir + "/manifest.json"); err != nil {
		t.Error("manifest.json not found")
	}
}
