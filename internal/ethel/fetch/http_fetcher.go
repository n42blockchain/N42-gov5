// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package fetch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/n42blockchain/N42/log"
)

// HTTPFetcherOptions tunes an HTTPFetcher.
type HTTPFetcherOptions struct {
	// Client is the HTTP client used for all requests. nil means a
	// fresh client with a 30-minute total timeout — long enough for
	// large segments on slow links, short enough that a stuck mirror
	// rotates within reason.
	Client *http.Client

	// AllowPlaintext lets the fetcher accept SourceHTTP entries
	// (plain http://). Default false: production manifests must use
	// HTTPS so that mirrors cannot serve tampered bytes.
	AllowPlaintext bool

	// ProgressInterval throttles ProgressFunc invocations. Default 1s.
	// Setting it lower spends more time in callback overhead, higher
	// makes log lines stale.
	ProgressInterval time.Duration

	// MaxAttempts is the per-Source retry budget. Default 3. After
	// exhausting attempts the Fetcher returns an error and the
	// MultiSourceFetcher tries the next Source.
	MaxAttempts int
}

// HTTPFetcher implements Fetcher for HTTPS / HTTP sources. It supports:
//
//   - Resume via HTTP Range when an existing partial file is found at
//     the destination path. The partial file's existing bytes are fed
//     into the SHA256 stream so the final hash matches.
//   - Per-Source retry up to MaxAttempts before giving up; the
//     MultiSourceFetcher then tries the next Source.
//   - Atomic-ish completion: writes go to a ".part" sibling and only
//     rename to the final name once the whole download verifies.
type HTTPFetcher struct {
	opts HTTPFetcherOptions
}

// NewHTTPFetcher constructs an HTTPFetcher with the given options. Zero
// fields fall back to documented defaults.
func NewHTTPFetcher(opts HTTPFetcherOptions) *HTTPFetcher {
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Minute}
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = time.Second
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	return &HTTPFetcher{opts: opts}
}

// Kinds reports HTTPS support, and HTTP too when AllowPlaintext is set.
func (f *HTTPFetcher) Kinds() []SourceKind {
	if f.opts.AllowPlaintext {
		return []SourceKind{SourceHTTPS, SourceHTTP}
	}
	return []SourceKind{SourceHTTPS}
}

// Fetch downloads asset into dstDir from the (single) Source supplied by
// the MultiSourceFetcher dispatch. progress may be nil.
//
// Behavior:
//   - Existing destination file with matching size + hash → success.
//   - Existing ".part" file → resume via Range.
//   - Final on-disk file is renamed atomically from ".part" only after
//     the SHA256 matches.
func (f *HTTPFetcher) Fetch(ctx context.Context, asset Asset, dstDir string, progress ProgressFunc) error {
	if err := asset.Validate(); err != nil {
		return err
	}
	if len(asset.Sources) != 1 {
		return fmt.Errorf("http fetcher: expected exactly one source per Fetch call, got %d", len(asset.Sources))
	}
	src := asset.Sources[0]
	if src.Kind != SourceHTTPS && !(f.opts.AllowPlaintext && src.Kind == SourceHTTP) {
		return fmt.Errorf("http fetcher: unsupported source kind %q", src.Kind)
	}
	parsed, err := url.Parse(src.URI)
	if err != nil {
		return fmt.Errorf("http fetcher: parse %q: %w", src.URI, err)
	}
	if parsed.Scheme != "https" && !(f.opts.AllowPlaintext && parsed.Scheme == "http") {
		return fmt.Errorf("http fetcher: refuse non-HTTPS source %q (set AllowPlaintext to override)", src.URI)
	}

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("http fetcher: mkdir %s: %w", dstDir, err)
	}

	finalPath := filepath.Join(dstDir, asset.Name)
	if ok, _ := verifyExistingFile(finalPath, asset); ok {
		log.Debug("download: existing file already matches, skipping",
			"asset", asset.Name, "path", finalPath)
		return nil
	}

	partPath := finalPath + ".part"

	var lastErr error
	for attempt := 1; attempt <= f.opts.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := f.fetchOnce(ctx, asset, src.URI, partPath, progress)
		if err == nil {
			if err := verifyAndCommit(partPath, finalPath, asset); err != nil {
				_ = os.Remove(partPath)
				if errors.Is(err, ErrChecksumMismatch) {
					// Don't retry on hash mismatch — bytes are
					// presumably corrupt at the mirror, retry would
					// just re-download the same bad bytes.
					return err
				}
				return err
			}
			return nil
		}
		lastErr = err
		log.Warn("download: http attempt failed",
			"asset", asset.Name, "attempt", attempt, "max", f.opts.MaxAttempts, "err", err)
	}
	return fmt.Errorf("http fetcher: gave up after %d attempts: %w", f.opts.MaxAttempts, lastErr)
}

// fetchOnce performs one HTTP GET (with Range when partPath already
// exists) and writes to partPath. It does NOT verify the SHA256 — that
// happens in verifyAndCommit so the partial file can be reused on a
// retry-after-disconnect that does not require redownload.
func (f *HTTPFetcher) fetchOnce(ctx context.Context, asset Asset, uri, partPath string, progress ProgressFunc) error {
	var existingSize int64
	if info, err := os.Stat(partPath); err == nil {
		existingSize = info.Size()
	}
	if uint64(existingSize) >= asset.SizeBytes {
		// Stale .part bigger than expected — discard and start over.
		_ = os.Remove(partPath)
		existingSize = 0
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return err
	}
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	resp, err := f.opts.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// Server ignored the Range header (or we did not send one).
		// Truncate any partial we had — the rest of the function will
		// rewrite the file from byte 0.
		if existingSize > 0 {
			_ = os.Remove(partPath)
			existingSize = 0
		}
	case http.StatusPartialContent:
		// Good — server honored the Range. Append.
	default:
		return fmt.Errorf("http %s: status %d", uri, resp.StatusCode)
	}

	// Validate Content-Length when present so we fail fast on a mirror
	// that responds with the wrong file.
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		want := int64(asset.SizeBytes) - existingSize
		got, parseErr := strconv.ParseInt(cl, 10, 64)
		if parseErr == nil && got != want && got > 0 {
			return fmt.Errorf("http %s: Content-Length=%d, want %d (existing=%d, total=%d)",
				uri, got, want, existingSize, asset.SizeBytes)
		}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if existingSize > 0 {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	out, err := os.OpenFile(partPath, flag, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	pw := &progressWriter{
		dst:      out,
		assetSiz: asset.SizeBytes,
		assetNam: asset.Name,
		source:   uri,
		current:  uint64(existingSize),
		progress: progress,
		interval: f.opts.ProgressInterval,
	}
	if _, err := io.Copy(pw, resp.Body); err != nil {
		return err
	}
	pw.flush()
	return nil
}

// verifyExistingFile checks whether finalPath already holds the asset.
// It is a fast pre-check that avoids any network round trip when an
// earlier run already completed the download.
func verifyExistingFile(finalPath string, asset Asset) (bool, error) {
	info, err := os.Stat(finalPath)
	if err != nil {
		return false, err
	}
	if uint64(info.Size()) != asset.SizeBytes {
		return false, nil
	}
	if isZeroHash(asset.SHA256) {
		// No declared hash → trust the size match. Test mode only.
		return true, nil
	}
	got, err := hashFile(finalPath)
	if err != nil {
		return false, err
	}
	return got == asset.SHA256, nil
}

// verifyAndCommit hashes partPath against asset.SHA256, and on success
// renames it to finalPath. On hash mismatch the .part file is removed by
// the caller.
func verifyAndCommit(partPath, finalPath string, asset Asset) error {
	info, err := os.Stat(partPath)
	if err != nil {
		return err
	}
	if uint64(info.Size()) != asset.SizeBytes {
		return fmt.Errorf("download: size mismatch on %s: got %d, want %d",
			filepath.Base(finalPath), info.Size(), asset.SizeBytes)
	}
	if !isZeroHash(asset.SHA256) {
		got, err := hashFile(partPath)
		if err != nil {
			return err
		}
		if got != asset.SHA256 {
			return fmt.Errorf("%w: %s", ErrChecksumMismatch, filepath.Base(finalPath))
		}
	}
	return os.Rename(partPath, finalPath)
}

// hashFile streams the SHA256 of the file at path.
func hashFile(path string) ([32]byte, error) {
	var out [32]byte
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}

// isZeroHash returns true when h is the all-zero sentinel — meaning "no
// hash supplied", which Asset.Validate documents as test-only.
func isZeroHash(h [32]byte) bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

// progressWriter is an io.Writer wrapper that emits Progress callbacks
// at most once per ProgressInterval. It does NOT compute SHA256 inline
// because resume + Range complicate the hash stream — verification is
// done end-to-end after the file is complete.
type progressWriter struct {
	dst      io.Writer
	assetSiz uint64
	assetNam string
	source   string
	current  uint64
	progress ProgressFunc
	interval time.Duration
	last     time.Time

	// hash stays nil — see comment above.
	_unused hash.Hash
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.dst.Write(p)
	pw.current += uint64(n)
	if pw.progress != nil && time.Since(pw.last) >= pw.interval {
		pw.last = time.Now()
		pw.progress(Progress{
			Asset:  pw.assetNam,
			Bytes:  pw.current,
			Total:  pw.assetSiz,
			Source: pw.source,
		})
	}
	return n, err
}

// flush emits one final Progress so the caller sees the 100% line even
// if the last Write happened inside the interval window.
func (pw *progressWriter) flush() {
	if pw.progress == nil {
		return
	}
	pw.progress(Progress{
		Asset:  pw.assetNam,
		Bytes:  pw.current,
		Total:  pw.assetSiz,
		Source: pw.source,
	})
}

// schemeOf is a tiny helper used by the test suite to check that
// HTTPFetcher rejects non-HTTPS URIs without instantiating a real
// http.Client.
func schemeOf(uri string) string {
	i := strings.Index(uri, "://")
	if i < 0 {
		return ""
	}
	return uri[:i]
}
