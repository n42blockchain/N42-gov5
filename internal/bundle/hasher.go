// Copyright 2022-2026 The N42 Authors

package bundle

import (
	"encoding/hex"
	"fmt"
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
)

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

// BuildOptions controls manifest generation.
type BuildOptions struct {
	ChainID    uint64
	BlockRange BlockRange
	Matcher    FileMatcher // nil → DefaultMatcher
	Workers    int         // 0 → GOMAXPROCS
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
	// blake2b(manifest) itself is comparable (trustless via multi-server
	// reproduction).
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
			for idx := range jobs {
				e := entries[idx]
				f, err := hashFile(e.path, e.info.Size(), e.rel, &bytesDone)
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
		Algorithm:   Algorithm,
		SegmentSize: SegmentSize,
		GeneratedAt: time.Now().UTC(),
		ChainID:     opts.ChainID,
		BlockRange:  opts.BlockRange,
		Files:       files,
	}, nil
}

// hashFile streams the file through blake2b-256, computing a per-segment
// hash chain on the side when Size >= SegmentThreshold. The bytes
// counter is updated continuously so the progress goroutine sees
// in-flight progress (not just per-file completion).
func hashFile(path string, size int64, relPath string, bytesDone *atomic.Int64) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("bundle: open %s: %w", path, err)
	}
	defer f.Close()

	whole, _ := blake2b.New256(nil)
	var (
		seg       []string
		segHasher hashAcc
	)
	includeSegments := size >= SegmentThreshold
	if includeSegments {
		segHasher.reset()
	}

	buf := make([]byte, 4*1024*1024)
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

// hashAcc accumulates one running blake2b across SegmentSize bytes,
// emitting a hex digest and resetting at every boundary. Crossing the
// boundary mid-buffer requires splitting the write into two feeds.
type hashAcc struct {
	h     io.Writer // blake2b hash, retyped to access Sum
	inner interface {
		Sum(b []byte) []byte
		Reset()
		Write(p []byte) (int, error)
	}
	written int64
}

func (a *hashAcc) reset() {
	h, _ := blake2b.New256(nil)
	a.inner = h
	a.h = h
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
