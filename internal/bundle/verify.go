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
	"sync"
	"sync/atomic"
	"time"
)

// VerifyResult is the per-file outcome of a verify pass. A whole-file
// hash mismatch leaves Segments empty; for files with segment hashes,
// BadSegments lists indices of corrupt 64 MiB chunks so the operator
// can re-fetch just those ranges (BT-style).
type VerifyResult struct {
	Path        string
	Size        int64
	Status      VerifyStatus
	BadSegments []int // segment indices that failed (for retry)
	Err         error
}

type VerifyStatus int

const (
	StatusOK              VerifyStatus = iota // hash matches
	StatusMissing                             // file not found
	StatusSizeMismatch                        // wrong size on disk
	StatusHashMismatch                        // whole-file hash differs
	StatusPartialMismatch                     // some segments differ — see BadSegments
	StatusReadError                           // IO error during hash
)

func (s VerifyStatus) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusMissing:
		return "MISSING"
	case StatusSizeMismatch:
		return "SIZE_MISMATCH"
	case StatusHashMismatch:
		return "HASH_MISMATCH"
	case StatusPartialMismatch:
		return "PARTIAL_MISMATCH"
	case StatusReadError:
		return "READ_ERROR"
	default:
		return "UNKNOWN"
	}
}

// VerifyOptions controls a verify pass.
type VerifyOptions struct {
	Workers int // 0 → GOMAXPROCS
	// Progress is called periodically with (filesDone, totalFiles,
	// bytesDone, totalBytes). nil to skip.
	Progress func(files, totalFiles int64, bytes, totalBytes int64)
}

// Verify walks the manifest's files under rootDir and checks each
// against its recorded hashes. Returns a per-file results slice in
// manifest order — caller decides how to react to non-OK statuses
// (typically: retry partial segments before re-downloading whole file).
func Verify(rootDir string, m *Manifest, opts VerifyOptions) ([]VerifyResult, error) {
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	results := make([]VerifyResult, len(m.Files))
	jobs := make(chan int, len(m.Files))
	var wg sync.WaitGroup
	var filesDone, bytesDone atomic.Int64
	var totalBytes int64
	for _, f := range m.Files {
		totalBytes += f.Size
	}
	totalFiles := int64(len(m.Files))

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4*1024*1024)
			whole, err := newHasher(m.Algorithm)
			if err != nil {
				// Manifest already validated by Load, but fall back to
				// per-file error reporting if a hasher init fails here.
				for idx := range jobs {
					results[idx] = VerifyResult{Path: m.Files[idx].Path, Size: m.Files[idx].Size, Status: StatusReadError, Err: err}
					filesDone.Add(1)
				}
				return
			}
			var seg hashAcc
			if err := seg.init(m.Algorithm); err != nil {
				for idx := range jobs {
					results[idx] = VerifyResult{Path: m.Files[idx].Path, Size: m.Files[idx].Size, Status: StatusReadError, Err: err}
					filesDone.Add(1)
				}
				return
			}
			for idx := range jobs {
				whole.Reset()
				seg.reset()
				r := verifyOne(filepath.Join(rootDir, filepath.FromSlash(m.Files[idx].Path)),
					m.Files[idx], buf, whole, &seg, &bytesDone)
				results[idx] = r
				filesDone.Add(1)
			}
		}()
	}

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

	for i := range m.Files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if opts.Progress != nil {
		opts.Progress(filesDone.Load(), totalFiles, bytesDone.Load(), totalBytes)
	}
	return results, nil
}

func verifyOne(absPath string, expect File, buf []byte, whole hash.Hash, segHash *hashAcc, bytesDone *atomic.Int64) VerifyResult {
	r := VerifyResult{Path: expect.Path, Size: expect.Size}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = StatusMissing
		} else {
			r.Status = StatusReadError
			r.Err = err
		}
		return r
	}
	if info.Size() != expect.Size {
		r.Status = StatusSizeMismatch
		r.Err = fmt.Errorf("size on disk %d != manifest %d", info.Size(), expect.Size)
		return r
	}

	f, err := os.Open(absPath)
	if err != nil {
		r.Status = StatusReadError
		r.Err = err
		return r
	}
	defer f.Close()

	var (
		bad    []int
		segIdx int
	)
	useSegs := len(expect.Segments) > 0

	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			whole.Write(buf[:n])
			bytesDone.Add(int64(n))
			if useSegs {
				if err := segHash.feedCheck(buf[:n], expect.Segments, &segIdx, &bad); err != nil {
					r.Status = StatusReadError
					r.Err = err
					return r
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			r.Status = StatusReadError
			r.Err = rerr
			return r
		}
	}
	if useSegs {
		segHash.flushFinalCheck(expect.Segments, &segIdx, &bad)
	}

	got := hex.EncodeToString(whole.Sum(nil))
	if got != expect.Hash {
		if useSegs && len(bad) > 0 {
			r.Status = StatusPartialMismatch
			r.BadSegments = bad
		} else {
			r.Status = StatusHashMismatch
			r.Err = fmt.Errorf("got %s, expected %s", got, expect.Hash)
		}
		return r
	}
	r.Status = StatusOK
	return r
}

// feedCheck flushes whenever a SegmentSize boundary is crossed,
// comparing each completed segment hash against expect[segIdx] and
// appending segIdx to bad on mismatch.
func (a *hashAcc) feedCheck(buf []byte, expect []string, segIdx *int, bad *[]int) error {
	for len(buf) > 0 {
		remain := SegmentSize - a.written
		if remain <= 0 {
			got := hex.EncodeToString(a.inner.Sum(nil))
			if *segIdx >= len(expect) || got != expect[*segIdx] {
				*bad = append(*bad, *segIdx)
			}
			*segIdx++
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

// flushFinalCheck compares the last partial segment.
func (a *hashAcc) flushFinalCheck(expect []string, segIdx *int, bad *[]int) {
	if a.written == 0 {
		return
	}
	got := hex.EncodeToString(a.inner.Sum(nil))
	if *segIdx >= len(expect) || got != expect[*segIdx] {
		*bad = append(*bad, *segIdx)
	}
	*segIdx++
}
