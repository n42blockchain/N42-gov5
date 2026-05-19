// Copyright 2022-2026 The N42 Authors

package bundle

import (
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/blake2b"
	"lukechampine.com/blake3"
)

// newHasher returns a fresh 32-byte hash.Hash for the named algorithm.
// Both AlgoBlake3_256 and AlgoBlake2b256 are supported.
func newHasher(algo string) (hash.Hash, error) {
	switch algo {
	case AlgoBlake3_256:
		return blake3.New(32, nil), nil
	case AlgoBlake2b256:
		return blake2b.New256(nil)
	default:
		return nil, fmt.Errorf("bundle: unknown hash algorithm %q", algo)
	}
}

// FileMatcher decides which files under rootDir to include in the
// manifest. Returns true to include, false to skip. Path is rooted-
// relative with forward slashes.
type FileMatcher func(relPath string, info os.FileInfo) bool

// DefaultMatcher includes every freezer artifact under chain/freezer/
// (cidx + cdat + ridx) and skips everything else (locks, lockfiles,
// random scratch). Operators with custom layouts can pass their own.
func DefaultMatcher(relPath string, info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	name := strings.ToLower(filepath.Base(relPath))
	if !strings.Contains(relPath, "chain/freezer") && !strings.Contains(relPath, "chain\\freezer") {
		return false
	}
	if strings.HasSuffix(name, ".cidx") || strings.HasSuffix(name, ".cdat") ||
		strings.HasSuffix(name, ".ridx") {
		return true
	}
	return false
}

// PermissiveMatcher accepts any regular file under root that is NOT a
// transient operator artifact (locks, staging temps, the manifest
// being written, hidden dotfiles). Use for flat archive dirs that
// don't follow the chain/freezer layout (snapshot, history, warm CS).
func PermissiveMatcher(relPath string, info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	name := strings.ToLower(info.Name())
	switch {
	case name == "manifest.json":
		return false
	case strings.HasPrefix(name, "."):
		return false
	case strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, ".lck"):
		return false
	case strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".staging"):
		return false
	case strings.HasSuffix(name, ".new"), strings.HasSuffix(name, ".bak"):
		return false
	}
	return true
}

// BuildOptions controls manifest generation.
type BuildOptions struct {
	ChainID    uint64
	BlockRange BlockRange
	Matcher    FileMatcher // nil → DefaultMatcher
	Workers    int         // 0 → GOMAXPROCS
	// Algorithm overrides the digest. Empty → DefaultAlgorithm.
	Algorithm string
	// Progress is called periodically with (filesHashed, totalFiles,
	// bytesHashed, totalBytes). nil to skip.
	Progress func(files, totalFiles int64, bytes, totalBytes int64)
}

// Build walks rootDir and hashes every matching file, returning a
// Manifest ready to Save. Hashing is parallel across files (Workers
// goroutines); within a file we stream-hash with a 4 MiB buffer.
func Build(rootDir string, opts BuildOptions) (*Manifest, error) {
	matcher := opts.Matcher
	if matcher == nil {
		matcher = DefaultMatcher
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	algo := opts.Algorithm
	if algo == "" {
		algo = DefaultAlgorithm
	}
	if !isSupportedAlgorithm(algo) {
		return nil, fmt.Errorf("bundle: unsupported algorithm %q", algo)
	}

	type fileEntry struct {
		path string // absolute
		rel  string // relative (forward slashes)
		info os.FileInfo
	}

	var entries []fileEntry
	var totalBytes int64
	if err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !matcher(rel, info) {
			return nil
		}
		entries = append(entries, fileEntry{path: path, rel: rel, info: info})
		totalBytes += info.Size()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("bundle: walk %s: %w", rootDir, err)
	}

	// Sort for deterministic manifest output — two servers hashing the
	// same directory must produce byte-identical manifests so the
	// digest of the manifest itself is comparable (trustless via
	// multi-server reproduction).
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	files := make([]File, len(entries))
	jobs := make(chan int, len(entries))
	var wg sync.WaitGroup
	var firstErr atomic.Value
	var filesDone, bytesDone atomic.Int64
	totalFiles := int64(len(entries))

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4*1024*1024)
			whole, err := newHasher(algo)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			var seg hashAcc
			if err := seg.init(algo); err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			for idx := range jobs {
				e := entries[idx]
				whole.Reset()
				seg.reset()
				f, err := hashFile(e.path, e.info.Size(), e.rel, buf, whole, &seg, &bytesDone)
				if err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				files[idx] = f
				filesDone.Add(1)
			}
		}()
	}

	// Progress goroutine.
	if opts.Progress != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					opts.Progress(filesDone.Load(), totalFiles, bytesDone.Load(), totalBytes)
				}
			}
		}()
	}

	for i := range entries {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	if e := firstErr.Load(); e != nil {
		return nil, e.(error)
	}
	if opts.Progress != nil {
		opts.Progress(filesDone.Load(), totalFiles, bytesDone.Load(), totalBytes)
	}

	return &Manifest{
		Version:     ManifestVersion,
		Algorithm:   algo,
		SegmentSize: SegmentSize,
		GeneratedAt: time.Now().UTC(),
		ChainID:     opts.ChainID,
		BlockRange:  opts.BlockRange,
		Files:       files,
	}, nil
}

// hashFile streams the file through the configured digest, computing
// a per-segment hash chain on the side when Size >= SegmentThreshold.
// The bytes counter is updated continuously so the progress goroutine
// sees in-flight progress (not just per-file completion). The buf,
// whole, and segHasher are worker-owned and pre-reset by the caller.
func hashFile(path string, size int64, relPath string, buf []byte, whole hash.Hash, segHasher *hashAcc, bytesDone *atomic.Int64) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("bundle: open %s: %w", path, err)
	}
	defer f.Close()

	var seg []string
	includeSegments := size >= SegmentThreshold

	var pos int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			whole.Write(buf[:n])
			bytesDone.Add(int64(n))
			if includeSegments {
				if err := segHasher.feed(buf[:n], &seg, &pos); err != nil {
					return File{}, err
				}
			}
			pos += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return File{}, fmt.Errorf("bundle: read %s: %w", path, rerr)
		}
	}
	if includeSegments {
		segHasher.flushFinal(&seg)
	}

	return File{
		Path:     relPath,
		Size:     size,
		Hash:     hex.EncodeToString(whole.Sum(nil)),
		Segments: seg,
	}, nil
}

// hashAcc accumulates one running hash across SegmentSize bytes,
// emitting a hex digest and resetting at every boundary. Crossing the
// boundary mid-buffer requires splitting the write into two feeds.
type hashAcc struct {
	inner   hash.Hash
	written int64
}

// init allocates the inner hasher once per worker. reset() between
// files is then a cheap Reset+zero, avoiding per-file hasher allocs.
func (a *hashAcc) init(algo string) error {
	h, err := newHasher(algo)
	if err != nil {
		return err
	}
	a.inner = h
	a.written = 0
	return nil
}

func (a *hashAcc) reset() {
	a.inner.Reset()
	a.written = 0
}

// feed writes buf to the current segment hasher, flushing whenever a
// SegmentSize boundary is crossed. pos is the file offset of buf[0]
// (unused here — kept for future asserts).
func (a *hashAcc) feed(buf []byte, out *[]string, pos *int64) error {
	for len(buf) > 0 {
		remain := SegmentSize - a.written
		if remain <= 0 {
			*out = append(*out, hex.EncodeToString(a.inner.Sum(nil)))
			a.inner.Reset()
			a.written = 0
			continue
		}
		take := int64(len(buf))
		if take > remain {
			take = remain
		}
		if _, err := a.inner.Write(buf[:take]); err != nil {
			return err
		}
		a.written += take
		buf = buf[take:]
	}
	return nil
}

// flushFinal emits the last partial segment (size < SegmentSize). Must
// be called once at EOF.
func (a *hashAcc) flushFinal(out *[]string) {
	if a.written > 0 {
		*out = append(*out, hex.EncodeToString(a.inner.Sum(nil)))
	}
}
