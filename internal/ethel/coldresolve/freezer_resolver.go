package coldresolve

import (
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
)

// FreezerResolver implements freezer.ColdResolver for cold-offloaded freezer
// tables (e.g. receipts) whose files are NOT the ethel 8B/segment columnar and
// therefore are not block-range indexed in the manifest. It resolves purely by
// FILE NAME ("receipts.0042.cdat") — the freezer already maps blockNum→fileNum
// via its own per-block .cidx, so the only missing piece is fetching the named
// cold file. Fetched files are SHA256-verified against the manifest and cached.
type FreezerResolver struct {
	byName  map[string]torrentsync.SegmentInfo
	fetcher Fetcher
	verify  bool

	mu    sync.Mutex
	cache map[string]string
}

// NewFreezerResolver indexes the manifest by file name. Works with the seed
// manifest produced by n42-cold-seed (which lists receipts.NNNN.cdat by name).
func NewFreezerResolver(m *torrentsync.Manifest, f Fetcher, verifySHA256 bool) *FreezerResolver {
	byName := map[string]torrentsync.SegmentInfo{}
	if m != nil {
		for _, s := range m.Segments {
			byName[s.FileName] = s
		}
	}
	return &FreezerResolver{byName: byName, fetcher: f, verify: verifySHA256, cache: map[string]string{}}
}

// ResolveDataFile fetches the named cold file (once) and returns its local path.
// Implements freezer.ColdResolver.
func (r *FreezerResolver) ResolveDataFile(fileName string) (string, error) {
	r.mu.Lock()
	if p, ok := r.cache[fileName]; ok {
		r.mu.Unlock()
		return p, nil
	}
	r.mu.Unlock()

	seg, ok := r.byName[fileName]
	if !ok {
		return "", fmt.Errorf("coldresolve: no cold segment named %s", fileName)
	}
	p, err := r.fetcher.Fetch(seg)
	if err != nil {
		return "", err
	}
	if r.verify && seg.SHA256 != "" {
		sum, err := sha256File(p)
		if err != nil {
			return "", err
		}
		if sum != seg.SHA256 {
			return "", fmt.Errorf("coldresolve: %s sha256 mismatch: got %s want %s", fileName, sum, seg.SHA256)
		}
	}
	r.mu.Lock()
	r.cache[fileName] = p
	r.mu.Unlock()
	return p, nil
}
