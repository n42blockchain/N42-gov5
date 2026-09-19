// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reframe.go — rewrite finished segments with another frame size.
//
// A segment's frame size is a read-cost knob, not part of the data: the rows
// and their order stay byte-identical, only the places where the zstd frames
// are cut move. An archive finalized with the old 256 KiB node-record frames
// gets the 16 KiB ones this way, without a rebuild.
package main

import (
	"flag"
	"fmt"
	"hash/crc64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

var reframeCRC = crc64.MakeTable(crc64.ECMA)

// segDigest is what a reframe must preserve: the rows, in order.
type segDigest struct {
	rows uint64
	raw  uint64
	crc  uint64
}

// reframeSegment copies src to dst with `target` uncompressed bytes per frame
// and returns the digest of the rows written. dst is written under a .tmp
// name and renamed into place, so it may be src itself or a symlink to it.
func reframeSegment(enc *zstd.Encoder, src, dst string, target int) (segDigest, error) {
	var dg segDigest
	f, err := os.Open(src)
	if err != nil {
		return dg, err
	}
	sf, err := loadLeafSegFile(f)
	if err != nil {
		f.Close()
		return dg, fmt.Errorf("%s: %w", src, err)
	}
	it := &oldSegIter{sf: sf, zr: zr2()}
	defer it.close()

	tmp := dst + ".tmp"
	sw, err := newSegFrameWriter(tmp, enc, target)
	if err != nil {
		return dg, err
	}
	for {
		ok, err := it.ensure()
		if err != nil {
			sw.f.Close()
			os.Remove(tmp)
			return dg, err
		}
		if !ok {
			break
		}
		rec := it.rec()
		dg.rows++
		dg.raw += uint64(len(rec))
		dg.crc = crc64.Update(dg.crc, reframeCRC, rec)
		if err := sw.add(rec, it.key()); err != nil {
			sw.f.Close()
			os.Remove(tmp)
			return dg, err
		}
		it.next()
	}
	if err := sw.finish(); err != nil {
		os.Remove(tmp)
		return dg, err
	}
	it.close() // release the source before replacing it (Windows)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return dg, err
	}
	return dg, os.Rename(tmp, dst)
}

// digestSegment reads a segment back and digests its rows.
func digestSegment(path string) (segDigest, error) {
	var dg segDigest
	f, err := os.Open(path)
	if err != nil {
		return dg, err
	}
	sf, err := loadLeafSegFile(f)
	if err != nil {
		f.Close()
		return dg, fmt.Errorf("%s: %w", path, err)
	}
	it := &oldSegIter{sf: sf, zr: zr2()}
	defer it.close()
	for {
		ok, err := it.ensure()
		if err != nil {
			return dg, err
		}
		if !ok {
			return dg, nil
		}
		rec := it.rec()
		dg.rows++
		dg.raw += uint64(len(rec))
		dg.crc = crc64.Update(dg.crc, reframeCRC, rec)
		it.next()
	}
}

// linkArchiveView makes `view` a read-only view of the archive `out`: mdbx.dat
// and every segment are symlinks to the originals. Segments reframed into the
// view then replace their links, and the original archive is never written.
func linkArchiveView(out, view string) error {
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(view, leafSegDir), 0o755); err != nil {
		return err
	}
	link := func(from, to string) error {
		if _, err := os.Lstat(to); err == nil {
			return nil // keep what is there: a rerun must not undo reframed segments
		}
		return os.Symlink(from, to)
	}
	if err := link(filepath.Join(absOut, "mdbx.dat"), filepath.Join(view, "mdbx.dat")); err != nil {
		return err
	}
	names, err := filepath.Glob(filepath.Join(absOut, leafSegDir, "*.seg"))
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := link(n, filepath.Join(view, leafSegDir, filepath.Base(n))); err != nil {
			return err
		}
	}
	return nil
}

func runReframe(args []string) {
	fs := flag.NewFlagSet("reframe", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir whose leafseg/ segments are read")
	view := fs.String("view", "", "write the reframed segments into this NEW archive dir, a symlink view of --out (the original is not touched); empty = replace the segments in --out")
	tables := fs.String("tables", "na", "comma-separated segment tables to reframe (a,s,ca,cs,sr,na)")
	frameKiB := fs.Int("frame-kib", 0, "uncompressed KiB per frame; 0 = each table's default")
	workers := fs.Int("workers", 16, "segments rewritten at once")
	check := fs.Bool("check", true, "read every rewritten segment back and compare its rows with the source")
	match := fs.String("match", "", "only segments whose file name matches this glob (e.g. 's.ab.seg'); for trying a frame size on a few buckets")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	srcDir := filepath.Join(*out, leafSegDir)
	dstDir := srcDir
	if *view != "" {
		if err := linkArchiveView(*out, *view); err != nil {
			die("view: %v", err)
		}
		dstDir = filepath.Join(*view, leafSegDir)
	}

	type job struct {
		name   string
		target int
	}
	var jobs []job
	for _, tn := range strings.Split(*tables, ",") {
		tn = strings.TrimSpace(tn)
		table, ok := segTableOfName(tn + ".")
		if !ok {
			die("unknown table %q", tn)
		}
		target := segFrameRawFor(table)
		if *frameKiB > 0 {
			target = *frameKiB << 10
		}
		names, err := filepath.Glob(filepath.Join(srcDir, tn+".*.seg"))
		if err != nil {
			die("glob: %v", err)
		}
		sort.Strings(names)
		for _, n := range names {
			if *match != "" {
				if ok, err := filepath.Match(*match, filepath.Base(n)); err != nil {
					die("--match: %v", err)
				} else if !ok {
					continue
				}
			}
			jobs = append(jobs, job{filepath.Base(n), target})
		}
	}
	if len(jobs) == 0 {
		die("no segments of tables %q under %s", *tables, srcDir)
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		done      int
		inB, outB int64
		rows      uint64
		start     = time.Now()
		ch        = make(chan job)
		sizeOf    = func(p string) int64 {
			st, err := os.Stat(p)
			if err != nil {
				return 0
			}
			return st.Size()
		}
	)
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			enc, err := zstd.NewWriter(nil,
				zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
				zstd.WithEncoderConcurrency(1))
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			defer enc.Close()
			for j := range ch {
				src := filepath.Join(srcDir, j.name)
				dst := filepath.Join(dstDir, j.name)
				in := sizeOf(src)
				// An in-place rewrite replaces src, so its digest has to come
				// from the copy pass itself; the read-back then checks dst.
				dg, err := reframeSegment(enc, src, dst, j.target)
				if err == nil && *check {
					var back segDigest
					if back, err = digestSegment(dst); err == nil && back != dg {
						err = fmt.Errorf("read-back differs: wrote %+v, read %+v", dg, back)
					}
				}
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", j.name, err)
				}
				done++
				inB += in
				outB += sizeOf(dst)
				rows += dg.rows
				if done%100 == 0 || done == len(jobs) {
					fmt.Printf("[reframe] %d/%d segments  %.1f GB -> %.1f GB  %s\n",
						done, len(jobs), float64(inB)/1e9, float64(outB)/1e9, time.Since(start).Truncate(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		mu.Lock()
		failed := firstErr != nil
		mu.Unlock()
		if failed {
			break
		}
		ch <- j
	}
	close(ch)
	wg.Wait()
	if firstErr != nil {
		die("reframe: %v", firstErr)
	}
	fmt.Printf("reframed %d segments (%d rows): %.2f GB -> %.2f GB in %s\n",
		len(jobs), rows, float64(inB)/1e9, float64(outB)/1e9, time.Since(start).Truncate(time.Second))
}
