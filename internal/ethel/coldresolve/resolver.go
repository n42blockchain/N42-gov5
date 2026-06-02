// Package coldresolve implements an ethel.ColdResolver backed by a torrentsync
// Manifest: when the bodyc reader hits a trimmed (cold) segment, it looks up the
// covering cold archive in the manifest, fetches it via a pluggable Fetcher
// (local cold dir, torrent, CAS), optionally verifies its SHA256, and returns
// the local path for the reader to open. Resolved files are cached so a segment
// is fetched at most once per process.
//
// This is the transparent cold-read path for the EIP-4444 Full-node standard
// (see internal/ethel/historyexpiry + docs/ethel/body-compression-design.md §9).
package coldresolve

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// Fetcher makes a cold segment file locally available and returns its path.
type Fetcher interface {
	Fetch(seg torrentsync.SegmentInfo) (localPath string, err error)
}

// LocalDirFetcher resolves cold segments from a local cold directory (e.g. a
// cheap cold disk, or a torrent client's download dir). "Fetch" verifies the
// file is present and returns its path.
type LocalDirFetcher struct{ ColdDir string }

func (f LocalDirFetcher) Fetch(seg torrentsync.SegmentInfo) (string, error) {
	p := filepath.Join(f.ColdDir, seg.FileName)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("coldresolve: %s not in cold dir: %w", seg.FileName, err)
	}
	return p, nil
}

// ManifestResolver implements ethel.ColdResolver against a manifest + fetcher.
type ManifestResolver struct {
	m       *torrentsync.Manifest
	fetcher Fetcher
	verify  bool

	mu     sync.Mutex
	cache  map[string]string // FileName -> resolved local path
	Hits   int               // cache hits (already-resolved segments)
	Misses int               // fetches performed
}

// New builds a resolver. If verifySHA256 is set, the fetched file's SHA256 is
// checked against the manifest entry before use (rejects corrupt/wrong data).
func New(m *torrentsync.Manifest, f Fetcher, verifySHA256 bool) *ManifestResolver {
	return &ManifestResolver{m: m, fetcher: f, verify: verifySHA256, cache: map[string]string{}}
}

// Resolve finds the cold archive covering blockNum, fetches it (once), and
// returns its local path. Implements ethel.ColdResolver.
func (r *ManifestResolver) Resolve(blockNum uint64) (string, error) {
	si := r.m.FindSegment(blockNum)
	if si == nil {
		return "", fmt.Errorf("coldresolve: no cold segment covers block %d", blockNum)
	}
	r.mu.Lock()
	if p, ok := r.cache[si.FileName]; ok {
		r.Hits++
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()

	p, err := r.fetcher.Fetch(*si)
	if err != nil {
		return "", err
	}
	if r.verify && si.SHA256 != "" {
		sum, err := sha256File(p)
		if err != nil {
			return "", err
		}
		if sum != si.SHA256 {
			return "", fmt.Errorf("coldresolve: %s sha256 mismatch: got %s want %s", si.FileName, sum, si.SHA256)
		}
	}
	r.mu.Lock()
	r.cache[si.FileName] = p
	r.Misses++
	r.mu.Unlock()
	return p, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
