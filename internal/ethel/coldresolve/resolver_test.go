package coldresolve

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

func writeCold(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestManifestResolverFetchVerifyCache(t *testing.T) {
	cold := t.TempDir()
	content := []byte("fake cold cdat segment content")
	sum := writeCold(t, cold, "bodyc.0097.cdat", content)

	m := &torrentsync.Manifest{
		ChainID: 1,
		Segments: []torrentsync.SegmentInfo{
			{FromBlock: 15_515_648, ToBlock: 15_580_000, FileName: "bodyc.0097.cdat", Size: int64(len(content)), SHA256: sum},
		},
		UpdatedAt: time.Unix(0, 0),
	}
	r := New(m, LocalDirFetcher{ColdDir: cold}, true)

	// A block inside the cold segment resolves + verifies.
	p, err := r.Resolve(15_540_000)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != filepath.Join(cold, "bodyc.0097.cdat") {
		t.Fatalf("unexpected path %s", p)
	}
	if r.Misses != 1 || r.Hits != 0 {
		t.Fatalf("want 1 miss 0 hit, got %d/%d", r.Misses, r.Hits)
	}
	// Second resolve in the same segment is a cache hit (fetched at most once).
	if _, err := r.Resolve(15_550_000); err != nil {
		t.Fatalf("resolve2: %v", err)
	}
	if r.Hits != 1 {
		t.Fatalf("want 1 hit, got %d", r.Hits)
	}
}

func TestManifestResolverMissAndCorrupt(t *testing.T) {
	cold := t.TempDir()
	content := []byte("content")
	sum := writeCold(t, cold, "bodyc.0097.cdat", content)
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FromBlock: 100, ToBlock: 200, FileName: "bodyc.0097.cdat", SHA256: sum},
	}}

	// Block outside any segment → error, no fetch.
	r := New(m, LocalDirFetcher{ColdDir: cold}, true)
	if _, err := r.Resolve(999_999); err == nil {
		t.Error("expected error for uncovered block")
	}

	// SHA256 mismatch → rejected.
	bad := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{
		{FromBlock: 100, ToBlock: 200, FileName: "bodyc.0097.cdat", SHA256: "deadbeef"},
	}}
	r2 := New(bad, LocalDirFetcher{ColdDir: cold}, true)
	if _, err := r2.Resolve(150); err == nil {
		t.Error("expected sha256 mismatch error")
	}
}
