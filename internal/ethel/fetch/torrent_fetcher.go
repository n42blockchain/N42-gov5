// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// torrent_fetcher.go — BitTorrent Fetcher implementation.
//
// TorrentFetcher wraps internal/distributed/storage/torrent.Client
// through a narrow torrentClient interface so the unit tests can mock
// the swarm without starting a real libp2p / DHT stack. Fetch resolves
// the SourceTorrent magnet URI via AddMagnet, waits up to InfoTimeout
// for the info dict, downloads the single artifact file and finally
// verifies its SHA256 against the Asset declaration before renaming it
// into place. Progress callbacks are throttled by ProgressInterval.

package fetch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent"

	storagetorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
	"github.com/n42blockchain/N42/log"
)

// torrentClient is the narrow surface TorrentFetcher needs from
// internal/distributed/storage/torrent.Client. Defining a local
// interface keeps the unit tests mockable without spinning up a real
// libp2p / DHT stack.
type torrentClient interface {
	AddMagnet(magnetURI string) (*torrent.Torrent, error)
	RemoveTorrent(ih [20]byte)
}

// TorrentFetcherOptions tunes a TorrentFetcher.
type TorrentFetcherOptions struct {
	// InfoTimeout caps how long Fetch waits for the torrent metadata
	// (info dict) after AddMagnet. Default 60s — long enough for cold
	// DHT lookups, short enough that an unreachable swarm rotates
	// within reason.
	InfoTimeout time.Duration

	// ProgressInterval throttles ProgressFunc invocations. Default 1s.
	ProgressInterval time.Duration

	// MaxAttempts is the per-Source retry budget. Default 1 — torrent
	// download already retries pieces internally, so retrying the whole
	// magnet is rarely useful. Set higher for flaky private trackers.
	MaxAttempts int

	// KeepSeeding leaves the torrent in the client after a successful
	// fetch so other peers can pull from us. Default true; set false
	// when running on a metered uplink.
	KeepSeeding bool
}

// TorrentFetcher implements Fetcher for BitTorrent v1 sources, backed by
// N42's existing internal/distributed/storage/torrent.Client. The client
// is owned by the eth-el Node and shared across the whole binary —
// TorrentFetcher only borrows it.
//
// Limitations:
//
//   - Single-file torrents only. eth-el manifests publish one Asset per
//     .torrent so this is the natural shape; multi-file torrents return
//     a clear error.
//
// Features picked up for free from the upstream anacrolix/torrent client:
//
//   - BT v2 (BEP 52) magnets via `urn:btmh:` — recognised by the same
//     AddMagnet call that handles v1.
//   - Hybrid v1+v2 magnets (both xt values on one magnet link) serving
//     the same swarm to both wire versions.
//   - WebSeed (BEP 19): anacrolix automatically drains HTTP web-seeds
//     parsed from the magnet's `ws=` parameter.
//   - Tracker, DHT, PEX, uTP: all present by default.
type TorrentFetcher struct {
	client torrentClient
	opts   TorrentFetcherOptions
}

// NewTorrentFetcher constructs a TorrentFetcher around an existing
// storage/torrent.Client. The client must already be Start()ed —
// TorrentFetcher will not call Start or Stop on it.
func NewTorrentFetcher(client *storagetorrent.Client, opts TorrentFetcherOptions) *TorrentFetcher {
	return newTorrentFetcher(torrentClientAdapter{client}, opts)
}

// newTorrentFetcher is the internal constructor used by tests to inject
// a fake client. Production code calls NewTorrentFetcher.
func newTorrentFetcher(client torrentClient, opts TorrentFetcherOptions) *TorrentFetcher {
	if opts.InfoTimeout <= 0 {
		opts.InfoTimeout = 60 * time.Second
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 1
	}
	return &TorrentFetcher{client: client, opts: opts}
}

// Kinds reports BT v1 AND BT v2. The upstream anacrolix/torrent client
// supports both wire versions from v1.50 onwards; v1.61 is the version
// N42 pins. Hybrid magnets (xt=urn:btih + xt=urn:btmh on the same URI)
// work through either dispatch path because the underlying client
// parses both components from a single Torrent entry.
func (f *TorrentFetcher) Kinds() []SourceKind {
	return []SourceKind{SourceBT, SourceBTV2}
}

// Fetch downloads asset via BitTorrent. The Source must be a magnet URI
// (either `magnet:?xt=urn:btih:...` for v1 or `magnet:?xt=urn:btmh:...`
// for v2; hybrid magnets listing both xt values work too). A bare
// 40-character v1 info-hash hex is also accepted as a convenience
// shorthand.
func (f *TorrentFetcher) Fetch(ctx context.Context, asset Asset, dstDir string, progress ProgressFunc) error {
	if err := asset.Validate(); err != nil {
		return err
	}
	if f.client == nil {
		return errors.New("torrent fetcher: no torrent client configured")
	}
	if len(asset.Sources) != 1 {
		return fmt.Errorf("torrent fetcher: expected exactly one source per Fetch call, got %d", len(asset.Sources))
	}
	src := asset.Sources[0]
	if src.Kind != SourceBT && src.Kind != SourceBTV2 {
		return fmt.Errorf("torrent fetcher: unsupported source kind %q", src.Kind)
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("torrent fetcher: mkdir %s: %w", dstDir, err)
	}

	finalPath := filepath.Join(dstDir, asset.Name)
	if ok, _ := verifyExistingFile(finalPath, asset); ok {
		log.Debug("download: existing file already matches, skipping",
			"asset", asset.Name, "path", finalPath)
		return nil
	}

	magnet := normaliseMagnet(src.URI)

	var lastErr error
	for attempt := 1; attempt <= f.opts.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := f.fetchOnce(ctx, asset, magnet, finalPath, progress)
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, ErrChecksumMismatch) {
			// Same reason as HTTPFetcher: a hash mismatch is content
			// corruption, not a transport hiccup. Retrying the same
			// magnet would just produce the same wrong bytes.
			return err
		}
		lastErr = err
		log.Warn("download: torrent attempt failed",
			"asset", asset.Name, "attempt", attempt, "max", f.opts.MaxAttempts, "err", err)
	}
	return fmt.Errorf("torrent fetcher: gave up after %d attempts: %w", f.opts.MaxAttempts, lastErr)
}

// fetchOnce performs one full magnet → file download. It is responsible
// for cleaning up the torrent (when not seeding) on every exit path.
func (f *TorrentFetcher) fetchOnce(ctx context.Context, asset Asset, magnet, finalPath string, progress ProgressFunc) error {
	t, err := f.client.AddMagnet(magnet)
	if err != nil {
		return fmt.Errorf("add magnet: %w", err)
	}
	defer func() {
		if !f.opts.KeepSeeding {
			f.client.RemoveTorrent(t.InfoHash())
		}
	}()

	// Wait for the info dict so we can decide which File to read.
	infoCtx, cancelInfo := context.WithTimeout(ctx, f.opts.InfoTimeout)
	defer cancelInfo()
	select {
	case <-infoCtx.Done():
		return fmt.Errorf("waiting for torrent info: %w", infoCtx.Err())
	case <-t.GotInfo():
	}

	files := t.Files()
	if len(files) == 0 {
		return errors.New("torrent fetcher: torrent has no files")
	}
	if len(files) > 1 {
		return fmt.Errorf("torrent fetcher: multi-file torrents are not supported (got %d files)", len(files))
	}
	file := files[0]
	if uint64(file.Length()) != asset.SizeBytes {
		return fmt.Errorf("torrent fetcher: file size mismatch: torrent says %d, asset says %d",
			file.Length(), asset.SizeBytes)
	}

	t.DownloadAll()

	// Tee the torrent reader into both the destination file and a
	// SHA256 stream. Streaming verification means we never load the
	// whole file into RAM and the partial file is removed on mismatch
	// without rereading bytes from disk.
	tmpPath := finalPath + ".part"
	if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale .part: %w", err)
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open .part: %w", err)
	}

	hasher := sha256.New()
	pw := &progressWriter{
		dst:      io.MultiWriter(out, hasher),
		assetSiz: asset.SizeBytes,
		assetNam: asset.Name,
		source:   magnet,
		progress: progress,
		interval: f.opts.ProgressInterval,
	}

	reader := file.NewReader()
	defer reader.Close()
	// Bias the reader toward sequential delivery so progress lines
	// advance smoothly and the SHA256 stream sees bytes in order.
	reader.SetReadahead(int64(8 * 1024 * 1024))
	reader.SetResponsive()

	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(pw, reader)
		copyDone <- err
	}()

	select {
	case <-ctx.Done():
		out.Close()
		_ = os.Remove(tmpPath)
		return ctx.Err()
	case err := <-copyDone:
		out.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("read torrent: %w", err)
		}
	}
	pw.flush()

	if pw.current != asset.SizeBytes {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("torrent fetcher: short read: got %d, want %d", pw.current, asset.SizeBytes)
	}
	if !isZeroHash(asset.SHA256) {
		var got [32]byte
		copy(got[:], hasher.Sum(nil))
		if got != asset.SHA256 {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, asset.Name)
		}
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// normaliseMagnet accepts:
//
//   - A full `magnet:?...` URI with any combination of `xt=urn:btih:`
//     (v1) and `xt=urn:btmh:` (v2) — passed through unchanged.
//   - A bare 40-character v1 info-hash hex — rewritten to
//     `magnet:?xt=urn:btih:<hex>`.
//
// anacrolix/torrent parses both v1 and v2 magnet forms from the same
// AddMagnet code path, so the caller does not need to know which wire
// version a Source uses.
func normaliseMagnet(uri string) string {
	if strings.HasPrefix(uri, "magnet:") {
		return uri
	}
	if len(uri) == 40 && isHexString(uri) {
		return "magnet:?xt=urn:btih:" + uri
	}
	return uri
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// torrentClientAdapter bridges *storagetorrent.Client to the local
// torrentClient interface. The wrapper exists so the test suite can
// supply a fake client without instantiating a real anacrolix client.
type torrentClientAdapter struct {
	c *storagetorrent.Client
}

func (a torrentClientAdapter) AddMagnet(magnetURI string) (*torrent.Torrent, error) {
	return a.c.AddMagnet(magnetURI)
}

func (a torrentClientAdapter) RemoveTorrent(ih [20]byte) {
	a.c.RemoveTorrent(ih)
}
