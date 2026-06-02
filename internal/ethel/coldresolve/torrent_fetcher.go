package coldresolve

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// TorrentSource fetches a content blob by its BitTorrent v1 infohash. It is the
// minimal surface of internal/distributed/storage/torrent.Bridge that the cold
// fetcher needs (*torrent.Bridge satisfies it via FetchByInfoHash), so this
// package does not pull in the whole torrent client stack and stays unit-testable.
type TorrentSource interface {
	FetchByInfoHash(ctx context.Context, ih metainfo.Hash) ([]byte, error)
}

// TorrentFetcher resolves cold segments over BitTorrent under a 1-of-N
// availability assumption: any archive/seeder node serving the segment's
// infohash satisfies the fetch. Downloaded segments are written into CacheDir
// (atomically) so a segment is pulled from the swarm at most once, then served
// locally. SHA256 verification is done by the ManifestResolver after Fetch.
type TorrentFetcher struct {
	Source   TorrentSource
	CacheDir string
	Timeout  time.Duration // per-segment download timeout; 0 = no timeout
}

func (f TorrentFetcher) Fetch(seg torrentsync.SegmentInfo) (string, error) {
	dest := filepath.Join(f.CacheDir, seg.FileName)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil // already pulled into the local cache
	}
	if seg.InfoHash == "" {
		return "", fmt.Errorf("coldresolve: segment %s has no infohash to fetch", seg.FileName)
	}
	b, err := hex.DecodeString(seg.InfoHash)
	if err != nil || len(b) != 20 {
		return "", fmt.Errorf("coldresolve: bad infohash %q for %s", seg.InfoHash, seg.FileName)
	}
	var ih metainfo.Hash
	copy(ih[:], b)

	ctx := context.Background()
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}
	data, err := f.Source.FetchByInfoHash(ctx, ih)
	if err != nil {
		return "", fmt.Errorf("coldresolve: torrent fetch %s (%s): %w", seg.FileName, seg.InfoHash, err)
	}
	if err := os.MkdirAll(f.CacheDir, 0755); err != nil {
		return "", err
	}
	// Atomic publish: write to a temp file then rename, so a concurrent reader
	// never sees a half-written cdat.
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, nil
}
