package coldresolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// fakeSource is an in-memory TorrentSource keyed by infohash — stands in for a
// torrent.Bridge fetching from the swarm, so the test needs no network.
type fakeSource struct {
	blobs map[metainfo.Hash][]byte
	calls int
}

func (s *fakeSource) FetchByInfoHash(_ context.Context, ih metainfo.Hash) ([]byte, error) {
	s.calls++
	b, ok := s.blobs[ih]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

func TestTorrentFetcherFetchCacheVerify(t *testing.T) {
	content := []byte("cold cdat bytes fetched over bittorrent")
	var ih metainfo.Hash
	// arbitrary 20-byte infohash
	hb, _ := hex.DecodeString("0123456789abcdef0123456789abcdef01234567")
	copy(ih[:], hb)
	src := &fakeSource{blobs: map[metainfo.Hash][]byte{ih: content}}

	cache := t.TempDir()
	sum := sha256.Sum256(content)
	seg := torrentsync.SegmentInfo{
		FromBlock: 15_515_648, ToBlock: 15_580_000,
		FileName: "bodyc.0097.cdat", InfoHash: ih.HexString(), SHA256: hex.EncodeToString(sum[:]),
	}
	m := &torrentsync.Manifest{Segments: []torrentsync.SegmentInfo{seg}}
	r := New(m, TorrentFetcher{Source: src, CacheDir: cache}, true)

	// First resolve pulls from the (fake) swarm, writes to cache, sha256-verifies.
	p, err := r.Resolve(15_540_000)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p != filepath.Join(cache, "bodyc.0097.cdat") {
		t.Fatalf("unexpected path %s", p)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(content) {
		t.Fatal("cached content mismatch")
	}
	if src.calls != 1 {
		t.Fatalf("want 1 swarm fetch, got %d", src.calls)
	}
	// Second resolve in the same segment is served from the resolver cache (no
	// extra swarm fetch).
	if _, err := r.Resolve(15_550_000); err != nil {
		t.Fatalf("resolve2: %v", err)
	}
	if src.calls != 1 {
		t.Fatalf("want no extra fetch (resolver cache), got %d", src.calls)
	}
}

func TestTorrentFetcherDiskCacheAcrossResolvers(t *testing.T) {
	content := []byte("persisted cold segment")
	var ih metainfo.Hash
	hb, _ := hex.DecodeString("89abcdef0123456789abcdef0123456789abcdef")
	copy(ih[:], hb)
	cache := t.TempDir()
	seg := torrentsync.SegmentInfo{FromBlock: 100, ToBlock: 200, FileName: "bodyc.0001.cdat", InfoHash: ih.HexString()}

	// Pre-populate the disk cache (as if a prior process already pulled it).
	if err := os.WriteFile(filepath.Join(cache, seg.FileName), content, 0644); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{blobs: map[metainfo.Hash][]byte{}} // empty — must NOT be hit
	f := TorrentFetcher{Source: src, CacheDir: cache}
	p, err := f.Fetch(seg)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if p != filepath.Join(cache, seg.FileName) {
		t.Fatalf("unexpected path %s", p)
	}
	if src.calls != 0 {
		t.Fatalf("disk cache should avoid swarm fetch, got %d calls", src.calls)
	}
}

func TestTorrentFetcherMissingInfoHash(t *testing.T) {
	f := TorrentFetcher{Source: &fakeSource{}, CacheDir: t.TempDir()}
	if _, err := f.Fetch(torrentsync.SegmentInfo{FileName: "bodyc.0001.cdat"}); err == nil {
		t.Error("expected error when segment has no infohash")
	}
}
