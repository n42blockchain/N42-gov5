package coldresolve

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// fakeFetcher serves named files from a dir (stands in for torrent/local fetch).
type fakeFetcher struct {
	dir   string
	calls int
}

func (f *fakeFetcher) Fetch(seg torrentsync.SegmentInfo) (string, error) {
	f.calls++
	p := filepath.Join(f.dir, seg.FileName)
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

func TestFreezerResolverByName(t *testing.T) {
	dir := t.TempDir()
	content := []byte("cold receipts segment 0042")
	if err := os.WriteFile(filepath.Join(dir, "receipts.0042.cdat"), content, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		// receipts entries have no block range (FromBlock/ToBlock 0) — name-keyed only.
		{FileName: "receipts.0042.cdat", SHA256: hex.EncodeToString(sum[:])},
	}}
	ff := &fakeFetcher{dir: dir}
	r := NewFreezerResolver(m, ff, true)

	p, err := r.ResolveDataFile("receipts.0042.cdat")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != filepath.Join(dir, "receipts.0042.cdat") {
		t.Fatalf("unexpected path %s", p)
	}
	// Second call cached (no extra fetch).
	if _, err := r.ResolveDataFile("receipts.0042.cdat"); err != nil {
		t.Fatal(err)
	}
	if ff.calls != 1 {
		t.Fatalf("want 1 fetch (cached), got %d", ff.calls)
	}
	// Unknown file → error.
	if _, err := r.ResolveDataFile("receipts.9999.cdat"); err == nil {
		t.Error("expected error for unknown file")
	}
}

func TestFreezerResolverSha256Mismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "receipts.0001.cdat"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FileName: "receipts.0001.cdat", SHA256: "deadbeef"},
	}}
	r := NewFreezerResolver(m, &fakeFetcher{dir: dir}, true)
	if _, err := r.ResolveDataFile("receipts.0001.cdat"); err == nil {
		t.Error("expected sha256 mismatch error")
	}
}
