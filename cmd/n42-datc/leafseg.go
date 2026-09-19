// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// leafseg — streaming segment store for the write-once DATC tables.
//
// The leaf-history (DatcLeafA/S) and change-index (DatcAccChg/StoChg) tables
// are the bulk of a build (≈90% of the ~4 TB raw-MDBX projection at mainnet
// 25M scale — far over the disk budget), yet the builder never reads them
// back and their rows are immutable once written. In --leaf-seg mode they
// bypass MDBX entirely:
//
//	build:    rows append, in arrival order, to per-bucket zstd spill streams
//	          (bucket = the key's leading 1–2 bytes, so bucket order == key
//	          order).
//	finalize: per bucket, decode all rows, stable-sort by full key (the key
//	          embeds block/epoch, and arrival order breaks ties → resume
//	          overlaps stay deterministic), write a static segment: zstd
//	          frames (~256 KiB raw) with a footer index of first keys.
//	verify:   segCursor implements the Seek/Next/Prev/Last contract the MDBX
//	          cursors satisfied, with an LRU of decompressed frames, so
//	          asOfLeaves / changedChildren run unchanged on either source.
package main

import (
	"bufio"
	"bytes"
	"container/heap"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	segTabLeafA   = 0
	segTabLeafS   = 1
	segTabChgA    = 2
	segTabChgS    = 3
	segTabStoRoot = 4 // storage-root history: addrHash(32)|block(4) → root(32) (empty = no storage)
	segTabNodeA   = 5 // account-trie node records (pathLen|path|epoch4 → FULL/DIFF/MIXED/tombstone)
	segTabNodeS   = 6 // exact storage node records (pathLen|addrHash|path|block4 → FULL/DIFF/tombstone), see exactladder.go
	segTabCount   = 7

	leafSegMagic = "DATCLS1\n"
	leafFrameRaw = 64 << 10 // target uncompressed bytes per frame. A fold
	// decompresses one frame, so this is the fixed cost of every fold the
	// reader does; 256 KiB made a hot contract's proof spend ~0.6 ms per fold
	// on decompression alone (2026-09-17 measurement).
	leafSpillDir   = "leafspill"
	leafSegDir     = "leafseg"
	leafFrameCache = 192 // decompressed frames kept hot (~48 MB)

	// Back-compat aliases (older call sites / tests).
	leafTableA = segTabLeafA
	leafTableS = segTabLeafS
)

var segTabNames = [segTabCount]string{"a", "s", "ca", "cs", "sr", "na", "ns"}

// nodeFrameRaw is the frame target of the account node records. A proof reads
// ~270 of them, each from a different frame (one per level-3 path), so the
// frame is the unit of cost there: at 256 KiB an account proof spent 80% of
// its time decompressing frames it used one record of. 16 KiB costs ~5% in
// compression ratio and makes that read ~10x cheaper (2026-09-18 measurement).
const nodeFrameRaw = 16 << 10

// segFrameRawFor is the frame target a table's segments are written with.
func segFrameRawFor(table int) int {
	if table == segTabNodeA || table == segTabNodeS {
		return nodeFrameRaw
	}
	return leafFrameRaw
}

// segTableOfName maps a segment or spill file name ("na.030f01.seg") to its
// table.
func segTableOfName(base string) (int, bool) {
	for i := 0; i < len(base); i++ {
		if base[i] == '.' {
			for t, n := range segTabNames {
				if n == base[:i] {
					return t, true
				}
			}
			return 0, false
		}
	}
	return 0, false
}

// segDecoder is the one zstd decoder every frame read shares. DecodeAll is
// safe for concurrent use, and building a decoder per frame cost about as
// much as decompressing the frame.
var (
	segDecoderOnce sync.Once
	segDecoder     *zstd.Decoder
)

func sharedSegDecoder() *zstd.Decoder {
	segDecoderOnce.Do(func() {
		n := runtime.NumCPU()
		if n > 64 {
			n = 64
		}
		d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(n))
		if err != nil {
			panic(fmt.Sprintf("zstd decoder: %v", err))
		}
		segDecoder = d
	})
	return segDecoder
}

// segPrefixLen is the number of leading key bytes that form the bucket id
// (bucket order == key order for any prefix length). Leaves bucket on the
// hashed key's first byte (uniform). Chg rows bucket on (level byte, second
// byte) — domain[0] for storage rows, the first path nibble for account rows.
// Account node records bucket on (pathLen, nib0, nib1): the dense depth-3
// layer spreads over 256 buckets so finalize sorts ~1 GB at a time.
var segPrefixLen = [segTabCount]int{1, 1, 2, 2, 1, 3, 2}

func segBucketOf(table int, k []byte) int {
	b := 0
	for i := 0; i < segPrefixLen[table]; i++ {
		b <<= 8
		if i < len(k) {
			b |= int(k[i])
		}
	}
	return b
}

func segFileName(table, bucket int) string {
	return fmt.Sprintf("%s.%0*x", segTabNames[table], 2*segPrefixLen[table], bucket)
}

// ---------------------------------------------------------------------------
// write side

type spillStream struct {
	f  *os.File
	bw *bufio.Writer
	zw *zstd.Encoder
}

// leafSpillWriter appends rows to per-bucket compressed spill files.
type leafSpillWriter struct {
	dir     string
	streams map[int]*spillStream // key: table<<16 | bucket
	rows    [segTabCount]uint64
	scratch []byte
}

func newLeafSpillWriter(outDir string) (*leafSpillWriter, error) {
	dir := filepath.Join(outDir, leafSpillDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &leafSpillWriter{dir: dir, streams: make(map[int]*spillStream)}, nil
}

func (w *leafSpillWriter) stream(table, bucket int) (*spillStream, error) {
	id := table<<24 | bucket // buckets are up to 3 bytes wide (segPrefixLen)
	if s := w.streams[id]; s != nil {
		return s, nil
	}
	name := filepath.Join(w.dir, segFileName(table, bucket)+".zspill")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(f, 1<<16)
	zw, err := zstd.NewWriter(bw,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(1<<18))
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &spillStream{f: f, bw: bw, zw: zw}
	w.streams[id] = s
	return s, nil
}

// add appends one row.
func (w *leafSpillWriter) add(table int, k, v []byte) error {
	s, err := w.stream(table, segBucketOf(table, k))
	if err != nil {
		return err
	}
	w.scratch = w.scratch[:0]
	w.scratch = binary.AppendUvarint(w.scratch, uint64(len(k)))
	w.scratch = append(w.scratch, k...)
	w.scratch = binary.AppendUvarint(w.scratch, uint64(len(v)))
	w.scratch = append(w.scratch, v...)
	if _, err := s.zw.Write(w.scratch); err != nil {
		return err
	}
	w.rows[table]++
	return nil
}

// cut ends the current zstd frame on one stream and opens a fresh one,
// flushing bytes to the OS file buffer. After a cut, every prior batch sits in
// its own COMPLETE frame; a hard kill then loses only the in-flight (uncut)
// batch's frame, which finalize skips cleanly — instead of truncating one
// giant whole-run frame and losing the entire run (the 2026-06-13 incident).
func (s *spillStream) cut() error {
	if err := s.zw.Close(); err != nil {
		return err
	}
	if err := s.bw.Flush(); err != nil {
		return err
	}
	s.zw.Reset(s.bw) // next Write starts a new frame (new magic)
	return nil
}

// flushBatch cuts every open stream at a frame boundary. Call once per build
// batch commit so a kill never truncates more than the in-flight batch.
func (w *leafSpillWriter) flushBatch() error {
	for _, s := range w.streams {
		if err := s.cut(); err != nil {
			return err
		}
	}
	return nil
}

func (w *leafSpillWriter) close() error {
	for id, s := range w.streams {
		if err := s.zw.Close(); err != nil {
			return err
		}
		if err := s.bw.Flush(); err != nil {
			return err
		}
		if err := s.f.Close(); err != nil {
			return err
		}
		delete(w.streams, id)
	}
	return nil
}

// finalizeLeafSegments turns the spill files into sorted static segments and
// removes the spill dir. Buckets are independent (one spill → one segment), so
// finalizeWorkers of them run at a time, each worker with its own zstd codec
// state; a bucket larger than finalizeRunBytes is sorted externally (see
// finalizeBucket). Output is identical whatever the worker count.
func finalizeLeafSegments(outDir string) error {
	spill := filepath.Join(outDir, leafSpillDir)
	segd := filepath.Join(outDir, leafSegDir)
	if err := os.MkdirAll(segd, 0o755); err != nil {
		return err
	}
	names, err := filepath.Glob(filepath.Join(spill, "*.zspill"))
	if err != nil {
		return err
	}
	workers := finalizeWorkers
	if workers > len(names) {
		workers = len(names)
	}
	if workers < 1 {
		workers = 1
	}

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		firstErr     error
		totalCorrupt int
		done         int
	)
	start := time.Now()
	jobs := make(chan string)
	for w := 0; w < workers; w++ {
		zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			close(jobs)
			wg.Wait()
			return err
		}
		enc, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
			zstd.WithEncoderConcurrency(2))
		if err != nil {
			zr.Close()
			close(jobs)
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func(zr *zstd.Decoder, enc *zstd.Encoder) {
			defer wg.Done()
			defer zr.Close()
			defer enc.Close()
			for src := range jobs {
				base := filepath.Base(src)
				dst := filepath.Join(segd, base[:len(base)-len(".zspill")]+".seg")
				cf := 0
				err := finalizeBucket(zr, enc, src, dst, &cf)
				if err == nil && cf == 0 {
					_ = os.Remove(src) // clean bucket → drop its spill
				}
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("bucket %s: %w", base, err)
					}
				} else {
					totalCorrupt += cf
				}
				done++
				if done%200 == 0 || done == len(names) {
					fmt.Printf("[leafseg] finalize %d/%d buckets (%d workers, %s)\n",
						done, len(names), workers, time.Since(start).Truncate(time.Second))
				}
				mu.Unlock()
			}
		}(zr, enc)
	}
	for _, src := range names {
		mu.Lock()
		failed := firstErr != nil
		mu.Unlock()
		if failed {
			break // let the in-flight buckets finish, then report
		}
		jobs <- src
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if totalCorrupt > 0 {
		// SAFETY (feedback-human-time-is-precious, 2026-06-13): corrupt/truncated
		// frames were skipped, so rows may be MISSING from the segments. Do NOT
		// delete the spill — keep every .zspill so the loss stays recoverable and
		// inspectable. The operator verifies the segments, then removes the spill
		// manually once satisfied. (The prior version deleted the spill regardless,
		// turning a kill-truncation into permanent multi-day data loss.)
		fmt.Printf("[leafseg] WARNING: skipped %d corrupt frame(s) across buckets — "+
			"segments may be INCOMPLETE. Spill dir RETAINED at %s (NOT deleted). "+
			"Run `n42-datc verify` before removing it; re-build the affected range if verify fails.\n",
			totalCorrupt, spill)
		return nil
	}
	return os.RemoveAll(spill)
}

// finalizeWorkers is how many buckets finalize at once. Each worker holds up
// to finalizeRunBytes of decoded rows plus its codec buffers (~1.3 GB at the
// default run size), so the peak is about workers x 1.3 GB on top of the
// caller's heap — the build finalizes inside its own process with the builder
// heap still live. DATC_FINALIZE_WORKERS overrides it (0/unset = automatic).
var finalizeWorkers = func() int {
	if v, err := strconv.Atoi(os.Getenv("DATC_FINALIZE_WORKERS")); err == nil && v > 0 {
		return v
	}
	n := runtime.NumCPU() / 2
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}()

// finalizeRunBytes bounds the decoded rows one bucket finalize keeps in memory.
// A bucket that fits in one run is sorted in memory; a larger one (the storage
// bucket of a USDT-class contract holds tens of GB of rows, which OOM-killed the
// 25M build's finalize) is sorted in runs written next to the segment and then
// k-way merged, so memory stays near this bound whatever the bucket size.
// DATC_FINALIZE_RUN_BYTES overrides it; tests use a tiny value to exercise the
// merge path.
var finalizeRunBytes = func() int {
	if v, err := strconv.Atoi(os.Getenv("DATC_FINALIZE_RUN_BYTES")); err == nil && v > 0 {
		return v
	}
	return 1 << 30
}()

// finalizeStreamMin is the span size above which a candidate frame is decoded
// by STREAMING it twice (once to check it decodes, once to feed rows) instead
// of DecodeAll'ing it into one buffer. merge re-spills a whole bucket as a
// single frame, which can be gigabytes; DecodeAll would need the decoded size
// in RAM at once. DATC_FINALIZE_STREAM_MIN overrides it (tests lower it).
var finalizeStreamMin = func() int64 {
	if v, err := strconv.ParseInt(os.Getenv("DATC_FINALIZE_STREAM_MIN"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 64 << 20
}()

// finalizeMergeSpan bounds how far a candidate frame boundary is extended while
// resyncing: the 4-byte zstd magic also occurs inside compressed data, so a
// frame is only trusted once the span up to the next candidate decodes. Spans
// past this bound are only tried when the candidate is the end of the file —
// otherwise a truncated kill-tail frame would be retried over gigabytes.
var finalizeMergeSpan = func() int64 {
	if v, err := strconv.ParseInt(os.Getenv("DATC_FINALIZE_MERGE_SPAN"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 512 << 20
}()

// maxSpillField caps a row's key or value length while parsing decoded spill
// bytes. A larger length is garbage: the rest of that frame group is dropped,
// where the whole-group parser stopped too.
const maxSpillField = 64 << 20

func finalizeBucket(zr *zstd.Decoder, enc *zstd.Encoder, src, dst string, corruptOut *int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	// Kill-resilient decode: a hard-killed --leaf-seg build leaves a TRUNCATED
	// zstd frame at the tail of that run's stream; a resumed build then appends
	// a SECOND, cleanly-closed zstd stream to the same file. Decode frame by
	// frame (split on the 4-byte zstd magic, read by offset so the spill never
	// has to fit in memory), accumulating consecutive good frames into one
	// contiguous group, and resync at the next frame whenever one fails to
	// decode. Rows are parsed as frames arrive; a group's trailing partial row —
	// and the rows in the dropped truncated frame — are discarded when the group
	// ends. The next group begins at a fresh frame boundary (a resumed run's
	// stream starts row-aligned, and a single frame never spans two runs). Loss
	// is bounded to the few rows buffered in the kill-tail frame.
	frameStarts, err := scanZstdMagics(f, size)
	if err != nil {
		return err
	}
	runs := &bucketRuns{dst: dst, limit: finalizeRunBytes}
	defer runs.cleanup()

	var group []byte // decoded bytes of the current group not yet parsed into rows
	groupBad := false
	parseGroup := func() error {
		p := 0
		for p < len(group) && !groupBad {
			kl, m := binary.Uvarint(group[p:])
			if m < 0 || kl > maxSpillField {
				groupBad = true
				break
			}
			if m == 0 {
				break
			}
			ke := p + m + int(kl)
			if ke > len(group) {
				break
			}
			vl, m2 := binary.Uvarint(group[ke:])
			if m2 < 0 || vl > maxSpillField {
				groupBad = true
				break
			}
			if m2 == 0 {
				break
			}
			ve := ke + m2 + int(vl)
			if ve > len(group) {
				break
			}
			if err := runs.add(group[p:ve]); err != nil {
				return err
			}
			p = ve
		}
		group = group[:copy(group, group[p:])]
		return nil
	}
	endGroup := func() error {
		if err := parseGroup(); err != nil {
			return err
		}
		group = group[:0]
		groupBad = false
		return nil
	}

	corruptFrames := 0
	var cands []int
	sd := &spanDecoder{zr: zr}
	defer sd.close()
	for fi := 0; fi < len(frameStarts); {
		// Candidate ends for this frame: the next magics, then the end of the
		// file. Spans over finalizeMergeSpan are skipped unless the candidate
		// is EOF — a whole-bucket frame written by merge has no other end.
		cands := cands[:0]
		for j := fi + 1; j < len(frameStarts); j++ {
			if frameStarts[j]-frameStarts[fi] > finalizeMergeSpan {
				break
			}
			cands = append(cands, j)
		}
		cands = append(cands, len(frameStarts))
		decoded := false
		for _, j := range cands {
			end := size
			if j < len(frameStarts) {
				end = frameStarts[j]
			}
			ok, err := sd.decode(f, frameStarts[fi], end-frameStarts[fi], func(chunk []byte) error {
				if groupBad {
					return nil
				}
				group = append(group, chunk...)
				return parseGroup()
			})
			if err != nil {
				return err
			}
			if ok {
				fi = j
				decoded = true
				break
			}
		}
		if !decoded {
			// Truncated/corrupt frame: finish the current contiguous group's
			// complete rows and resync at the next candidate boundary.
			if err := endGroup(); err != nil {
				return err
			}
			corruptFrames++
			fi++
		}
	}
	if err := endGroup(); err != nil {
		return err
	}
	group = nil
	if corruptFrames > 0 {
		fmt.Printf("[leafseg] %s: skipped %d corrupt frame(s) (kill-tail), recovered %d rows\n",
			filepath.Base(src), corruptFrames, runs.rows)
	}

	// A RESUMED build appends to a bucket that was already finalized: merge
	// the existing segment (sorted, streamed frame by frame) with the new
	// rows. Equal keys keep the OLD row first — arrival order, deterministic.
	var old *oldSegIter
	if of, err := os.Open(dst); err == nil {
		sf, lerr := loadLeafSegFile(of)
		if lerr != nil {
			of.Close()
			return fmt.Errorf("existing segment: %w", lerr)
		}
		old = &oldSegIter{sf: sf, zr: zr2()}
		defer old.close()
	}

	tmp := dst + ".tmp"
	target := leafFrameRaw
	if table, ok := segTableOfName(filepath.Base(dst)); ok {
		target = segFrameRawFor(table)
	}
	sw, err := newSegFrameWriter(tmp, enc, target)
	if err != nil {
		return err
	}
	if err := runs.writeSorted(sw, old); err != nil {
		return err
	}
	if err := sw.finish(); err != nil {
		return err
	}
	if old != nil {
		old.close() // release the handle before replacing the file (Windows)
	}
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	if corruptOut != nil {
		*corruptOut = corruptFrames
	}
	return os.Rename(tmp, dst)
}

// scanZstdMagics returns the offset of every non-overlapping zstd frame magic
// in f, reading it in chunks.
func scanZstdMagics(f *os.File, size int64) ([]int64, error) {
	magic := []byte{0x28, 0xb5, 0x2f, 0xfd}
	const chunk = 16 << 20
	buf := make([]byte, chunk+len(magic)-1)
	var starts []int64
	carry := 0
	for off := int64(0); off < size; {
		n, err := f.ReadAt(buf[carry:carry+chunk], off)
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n == 0 {
			break
		}
		data := buf[:carry+n]
		base := off - int64(carry)
		next := 0 // first position a new match may start at (matches do not overlap)
		for i := 0; ; {
			j := bytes.Index(data[i:], magic)
			if j < 0 {
				break
			}
			starts = append(starts, base+int64(i+j))
			i += j + len(magic)
			next = i
		}
		keep := len(magic) - 1
		if keep > len(data) {
			keep = len(data)
		}
		if start := len(data) - keep; start < next {
			keep = len(data) - next
		}
		copy(buf, data[len(data)-keep:])
		carry = keep
		off += int64(n)
	}
	return starts, nil
}

func spillRecKey(raw []byte, off uint64) []byte {
	kl, m := binary.Uvarint(raw[off:])
	return raw[off+uint64(m) : off+uint64(m)+kl]
}

func spillRecEnd(raw []byte, off uint64) uint64 {
	kl, m := binary.Uvarint(raw[off:])
	p := off + uint64(m) + kl
	vl, m2 := binary.Uvarint(raw[p:])
	return p + uint64(m2) + vl
}

// spanDecoder decodes one candidate frame span of a spill file. A span up to
// finalizeStreamMin is decoded into one buffer; a larger one is streamed
// TWICE — the first pass only checks that it decodes, so a span that fails
// (a truncated kill-tail frame, or a false magic that cut a frame short)
// emits nothing, exactly as the buffered path does.
type spanDecoder struct {
	zr     *zstd.Decoder
	stream *zstd.Decoder
	dec    []byte
	buf    []byte
	span   []byte
}

// decode reports whether [off, off+n) of f decodes; every decoded chunk is
// passed to emit in order. Nothing is emitted when it returns false.
func (d *spanDecoder) decode(f *os.File, off, n int64, emit func([]byte) error) (bool, error) {
	if n <= finalizeStreamMin {
		if int64(cap(d.span)) < n {
			d.span = make([]byte, n)
		}
		d.span = d.span[:n]
		if _, err := f.ReadAt(d.span, off); err != nil && err != io.EOF {
			return false, err
		}
		var derr error
		d.dec, derr = d.zr.DecodeAll(d.span, d.dec[:0])
		if derr != nil {
			return false, nil
		}
		return true, emit(d.dec)
	}
	if d.stream == nil {
		zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return false, err
		}
		d.stream = zr
	}
	if d.buf == nil {
		d.buf = make([]byte, 1<<20)
	}
	for pass := 0; pass < 2; pass++ {
		if err := d.stream.Reset(io.NewSectionReader(f, off, n)); err != nil {
			return false, err
		}
		for {
			m, err := d.stream.Read(d.buf)
			if m > 0 && pass == 1 {
				if eerr := emit(d.buf[:m]); eerr != nil {
					return false, eerr
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return false, nil // pass 0 rejects the span; pass 1 cannot fail
			}
		}
	}
	return true, nil
}

func (d *spanDecoder) close() {
	if d.stream != nil {
		d.stream.Close()
		d.stream = nil
	}
}

// bucketRuns collects one bucket's rows in arrival order. Rows stay in memory
// until limit bytes, then are sorted into a run file beside the segment.
type bucketRuns struct {
	dst   string
	limit int
	raw   []byte
	offs  []uint64
	files []string
	rows  int
}

func (b *bucketRuns) add(rec []byte) error {
	b.offs = append(b.offs, uint64(len(b.raw)))
	b.raw = append(b.raw, rec...)
	b.rows++
	if len(b.raw) >= b.limit {
		return b.spillRun()
	}
	return nil
}

// sortMem sorts the buffered rows by full key (which embeds the block/epoch
// suffix → version order). STABLE: duplicate keys (a resumed build re-spilling
// an overlap) keep arrival order, so output is deterministic.
func (b *bucketRuns) sortMem() {
	raw := b.raw
	sort.SliceStable(b.offs, func(i, j int) bool {
		return bytes.Compare(spillRecKey(raw, b.offs[i]), spillRecKey(raw, b.offs[j])) < 0
	})
}

// spillRun sorts the buffered rows into the next run file (each record
// prefixed with its uvarint length) and empties the buffer.
func (b *bucketRuns) spillRun() error {
	if len(b.offs) == 0 {
		return nil
	}
	b.sortMem()
	name := fmt.Sprintf("%s.run%04d.tmp", b.dst, len(b.files))
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	b.files = append(b.files, name)
	bw := bufio.NewWriterSize(f, 4<<20)
	var lp [binary.MaxVarintLen64]byte
	for _, off := range b.offs {
		rec := b.raw[off:spillRecEnd(b.raw, off)]
		n := binary.PutUvarint(lp[:], uint64(len(rec)))
		if _, err := bw.Write(lp[:n]); err != nil {
			f.Close()
			return err
		}
		if _, err := bw.Write(rec); err != nil {
			f.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	b.raw = b.raw[:0]
	b.offs = b.offs[:0]
	return nil
}

func (b *bucketRuns) cleanup() {
	for _, name := range b.files {
		_ = os.Remove(name)
	}
}

// writeSorted emits every row in key order into sw, merged with the existing
// segment when old != nil. Equal keys come out old segment first, then in
// arrival order (earlier runs first, stable within a run) — exactly the order
// of one in-memory stable sort followed by the old-first merge.
func (b *bucketRuns) writeSorted(sw *segFrameWriter, old *oldSegIter) error {
	if len(b.files) == 0 {
		b.sortMem()
		ni := 0
		emitNew := func() error {
			off := b.offs[ni]
			ni++
			return sw.add(b.raw[off:spillRecEnd(b.raw, off)], spillRecKey(b.raw, off))
		}
		for old != nil && old.valid() {
			ok, err := old.ensure()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			for ni < len(b.offs) && bytes.Compare(spillRecKey(b.raw, b.offs[ni]), old.key()) < 0 {
				if err := emitNew(); err != nil {
					return err
				}
			}
			if err := sw.add(old.rec(), old.key()); err != nil {
				return err
			}
			old.next()
		}
		for ni < len(b.offs) {
			if err := emitNew(); err != nil {
				return err
			}
		}
		return nil
	}

	if err := b.spillRun(); err != nil {
		return err
	}
	b.raw, b.offs = nil, nil
	h := &mergeHeap{}
	if old != nil {
		src := &segMergeSource{it: old}
		ok, err := old.ensure()
		if err != nil {
			return err
		}
		if ok {
			*h = append(*h, mergeItem{src: src, prio: 0})
		}
	}
	var opened []*runMergeSource
	defer func() {
		for _, r := range opened {
			r.close()
		}
	}()
	for i, name := range b.files {
		r, err := openRunMergeSource(name)
		if err != nil {
			return err
		}
		opened = append(opened, r)
		ok, err := r.next()
		if err != nil {
			return err
		}
		if ok {
			*h = append(*h, mergeItem{src: r, prio: i + 1})
		}
	}
	heap.Init(h)
	for h.Len() > 0 {
		top := (*h)[0]
		if err := sw.add(top.src.rec(), top.src.key()); err != nil {
			return err
		}
		ok, err := top.src.next()
		if err != nil {
			return err
		}
		if ok {
			heap.Fix(h, 0)
		} else {
			heap.Pop(h)
		}
	}
	return nil
}

// mergeSource is one sorted input of the run merge, positioned on a record.
type mergeSource interface {
	key() []byte
	rec() []byte
	next() (bool, error)
}

type segMergeSource struct{ it *oldSegIter }

func (s *segMergeSource) key() []byte { return s.it.key() }
func (s *segMergeSource) rec() []byte { return s.it.rec() }
func (s *segMergeSource) next() (bool, error) {
	s.it.next()
	return s.it.ensure()
}

type runMergeSource struct {
	f   *os.File
	br  *bufio.Reader
	buf []byte
}

func openRunMergeSource(name string) (*runMergeSource, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return &runMergeSource{f: f, br: bufio.NewReaderSize(f, 4<<20)}, nil
}

func (s *runMergeSource) key() []byte { return spillRecKey(s.buf, 0) }
func (s *runMergeSource) rec() []byte { return s.buf }
func (s *runMergeSource) next() (bool, error) {
	n, err := binary.ReadUvarint(s.br)
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if uint64(cap(s.buf)) < n {
		s.buf = make([]byte, n)
	}
	s.buf = s.buf[:n]
	if _, err := io.ReadFull(s.br, s.buf); err != nil {
		return false, err
	}
	return true, nil
}
func (s *runMergeSource) close() { _ = s.f.Close() }

// mergeItem orders sources by current key, then by priority (0 = existing
// segment, then runs in arrival order).
type mergeItem struct {
	src  mergeSource
	prio int
}

type mergeHeap []mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if c := bytes.Compare(h[i].src.key(), h[j].src.key()); c != 0 {
		return c < 0
	}
	return h[i].prio < h[j].prio
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(mergeItem)) }
func (h *mergeHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	return it
}

type segFrameWriter struct {
	f     *os.File
	bw    *bufio.Writer
	enc   *zstd.Encoder
	metas []struct {
		comp, rawLen int
		firstKey     []byte
	}
	frame    []byte
	firstKey []byte
	target   int // uncompressed bytes per frame
}

func newSegFrameWriter(path string, enc *zstd.Encoder, target int) (*segFrameWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if target <= 0 {
		target = leafFrameRaw
	}
	w := &segFrameWriter{f: f, bw: bufio.NewWriterSize(f, 1<<20), enc: enc, target: target}
	if _, err := w.bw.WriteString(leafSegMagic); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

func (w *segFrameWriter) add(rec, key []byte) error {
	if w.firstKey == nil {
		w.firstKey = append([]byte{}, key...)
	}
	w.frame = append(w.frame, rec...)
	if len(w.frame) >= w.target {
		return w.flush()
	}
	return nil
}

func (w *segFrameWriter) flush() error {
	if len(w.frame) == 0 {
		return nil
	}
	comp := w.enc.EncodeAll(w.frame, nil)
	if _, err := w.bw.Write(comp); err != nil {
		return err
	}
	w.metas = append(w.metas, struct {
		comp, rawLen int
		firstKey     []byte
	}{len(comp), len(w.frame), w.firstKey})
	w.frame = w.frame[:0]
	w.firstKey = nil
	return nil
}

func (w *segFrameWriter) finish() error {
	if err := w.flush(); err != nil {
		return err
	}
	var foot []byte
	foot = binary.AppendUvarint(foot, uint64(len(w.metas)))
	for _, m := range w.metas {
		foot = binary.AppendUvarint(foot, uint64(m.comp))
		foot = binary.AppendUvarint(foot, uint64(m.rawLen))
		foot = binary.AppendUvarint(foot, uint64(len(m.firstKey)))
		foot = append(foot, m.firstKey...)
	}
	if _, err := w.bw.Write(foot); err != nil {
		return err
	}
	var tail [16]byte
	binary.BigEndian.PutUint64(tail[:8], uint64(len(foot)))
	copy(tail[8:], leafSegMagic)
	if _, err := w.bw.Write(tail[:]); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Close()
}

// oldSegIter streams an existing segment's records in order, one decompressed
// frame at a time.
type oldSegIter struct {
	sf     *leafSegFile
	zr     *zstd.Decoder
	fi, ri int
	raw    []byte
	offs   []uint32
	closed bool
}

func zr2() *zstd.Decoder {
	d, _ := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	return d
}

func (it *oldSegIter) valid() bool { return !it.closed && it.fi < it.sf.numFrames() }

// ensure decodes the current frame; returns false at end.
func (it *oldSegIter) ensure() (bool, error) {
	for {
		if !it.valid() {
			return false, nil
		}
		if it.raw == nil {
			fm := it.sf.frame(it.fi)
			comp := make([]byte, fm.comp)
			if _, err := it.sf.f.ReadAt(comp, fm.off); err != nil {
				return false, err
			}
			raw, err := it.zr.DecodeAll(comp, make([]byte, 0, fm.raw))
			if err != nil {
				return false, err
			}
			it.raw = raw
			it.offs = it.offs[:0]
			pos := uint32(0)
			for pos < uint32(len(raw)) {
				it.offs = append(it.offs, pos)
				kl, m := binary.Uvarint(raw[pos:])
				pos += uint32(m) + uint32(kl)
				vl, m2 := binary.Uvarint(raw[pos:])
				pos += uint32(m2) + uint32(vl)
			}
			it.ri = 0
		}
		if it.ri < len(it.offs) {
			return true, nil
		}
		it.fi++
		it.raw = nil
	}
}

func (it *oldSegIter) key() []byte {
	off := it.offs[it.ri]
	kl, m := binary.Uvarint(it.raw[off:])
	return it.raw[off+uint32(m) : off+uint32(m)+uint32(kl)]
}

func (it *oldSegIter) rec() []byte {
	off := it.offs[it.ri]
	kl, m := binary.Uvarint(it.raw[off:])
	p := off + uint32(m) + uint32(kl)
	vl, m2 := binary.Uvarint(it.raw[p:])
	return it.raw[off : p+uint32(m2)+uint32(vl)]
}

func (it *oldSegIter) next() { it.ri++ }

func (it *oldSegIter) close() {
	if !it.closed {
		it.closed = true
		_ = it.sf.f.Close()
		it.zr.Close()
	}
}

// ---------------------------------------------------------------------------
// read side

type frameMeta struct {
	off  int64 // compressed offset in file
	comp int
	raw  int
}

// leafSegFile is one open segment and its frame index. The index holds no
// pointers per frame: with 16 KiB node-record frames an archive has tens of
// millions of frames, and a slice of structs with a key slice each made the
// garbage collector walk all of them on every cycle (half the CPU of a
// parallel bench, 2026-09-18). It is immutable once loaded, so every reader
// in the process shares one copy (acquireSegFile).
type leafSegFile struct {
	f    *os.File
	offs []int64  // compressed start of frame i; offs[n] is the end of the last
	raws []uint32 // uncompressed length of frame i
	kOff []uint32 // first key of frame i = keys[kOff[i]:kOff[i+1]]
	keys []byte
}

func (sf *leafSegFile) numFrames() int { return len(sf.raws) }

func (sf *leafSegFile) frame(i int) frameMeta {
	return frameMeta{off: sf.offs[i], comp: int(sf.offs[i+1] - sf.offs[i]), raw: int(sf.raws[i])}
}

func (sf *leafSegFile) firstKey(i int) []byte { return sf.keys[sf.kOff[i]:sf.kOff[i+1]] }

// segFileEntry is a shared, reference-counted leafSegFile. The key carries the
// file's size and mtime, so a segment replaced on disk (finalize, reframe) is
// loaded afresh instead of answering from the old index.
type segFileEntry struct {
	key  string
	once sync.Once
	sf   *leafSegFile
	err  error
	refs int
}

var segFiles = struct {
	mu sync.Mutex
	m  map[string]*segFileEntry
	// retain keeps a segment open after its last reader closes. A process that
	// opens a querier per query (bench, a proof server) would otherwise reload
	// the index of every segment whose readers happened to all be done.
	retain bool
}{m: make(map[string]*segFileEntry)}

// retainSegFiles makes the shared segment files live as long as the process.
func retainSegFiles() {
	segFiles.mu.Lock()
	segFiles.retain = true
	segFiles.mu.Unlock()
}

func acquireSegFile(path string) (*segFileEntry, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	segFiles.mu.Lock()
	e := segFiles.m[key]
	if e == nil {
		e = &segFileEntry{key: key}
		segFiles.m[key] = e
	}
	e.refs++
	segFiles.mu.Unlock()
	e.once.Do(func() {
		f, err := os.Open(path)
		if err != nil {
			e.err = err
			return
		}
		if e.sf, e.err = loadLeafSegFile(f); e.err != nil {
			f.Close()
			e.err = fmt.Errorf("%s: %w", path, e.err)
		}
	})
	if e.err != nil {
		releaseSegFile(e)
		return nil, e.err
	}
	return e, nil
}

func releaseSegFile(e *segFileEntry) {
	segFiles.mu.Lock()
	e.refs--
	last := e.refs == 0 && !segFiles.retain
	if last {
		delete(segFiles.m, e.key)
	}
	segFiles.mu.Unlock()
	if last && e.sf != nil {
		_ = e.sf.f.Close()
	}
}

// preloadSegFiles loads the frame index of every segment under outDir and
// retains them for the life of the process. Parsing a 16 KiB-frame node
// segment's index takes tens of milliseconds, which a long-lived reader pays
// at start-up instead of inside the first proofs that touch each segment.
func preloadSegFiles(outDir string, workers int) (int, error) {
	names, err := filepath.Glob(filepath.Join(outDir, leafSegDir, "*.seg"))
	if err != nil {
		return 0, err
	}
	retainSegFiles()
	if workers < 1 {
		workers = 1
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		ch       = make(chan string)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range ch {
				e, err := acquireSegFile(name)
				if err == nil {
					releaseSegFile(e) // retained: stays loaded
					continue
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	for _, n := range names {
		ch <- n
	}
	close(ch)
	wg.Wait()
	return len(names), firstErr
}

// leafSegSet is the reader for one table's bucket segments. Segments are
// opened on first use: a proof touches a few dozen of an archive's ~2000
// files. A set serves one goroutine; the files behind it are shared.
type leafSegSet struct {
	paths map[int]string        // bucket → segment path
	open  map[int]*segFileEntry // buckets opened so far
	ids   []int                 // sorted bucket ids (bucket order == key order)
	table int
	cache *frameLRU
}

// file returns the bucket's segment, opening it on first use.
func (s *leafSegSet) file(bucket int) (*leafSegFile, error) {
	if e := s.open[bucket]; e != nil {
		return e.sf, nil
	}
	path, ok := s.paths[bucket]
	if !ok {
		return nil, fmt.Errorf("leafseg: no segment for bucket %x of table %s", bucket, segTabNames[s.table])
	}
	e, err := acquireSegFile(path)
	if err != nil {
		return nil, err
	}
	s.open[bucket] = e
	return e.sf, nil
}

// frameCount is the number of frames in one bucket's segment.
func (s *leafSegSet) frameCount(bucket int) (int, error) {
	sf, err := s.file(bucket)
	if err != nil {
		return 0, err
	}
	return sf.numFrames(), nil
}

type decodedFrame struct {
	raw  []byte
	offs []uint32 // record starts
}

type frameLRU struct {
	m         map[uint64]*decodedFrame // key: table<<48 | bucket<<24 | frameIdx
	ord       []uint64
	capFrames int
}

func newFrameLRU() *frameLRU { return newFrameLRUSize(leafFrameCache) }

func newFrameLRUSize(n int) *frameLRU {
	if n < 8 {
		n = 8
	}
	return &frameLRU{m: make(map[uint64]*decodedFrame, n), capFrames: n}
}

func (l *frameLRU) get(k uint64) *decodedFrame { return l.m[k] }
func (l *frameLRU) put(k uint64, d *decodedFrame) {
	if _, ok := l.m[k]; ok {
		return
	}
	if len(l.ord) >= l.capFrames {
		old := l.ord[0]
		l.ord = l.ord[1:]
		delete(l.m, old)
	}
	l.m[k] = d
	l.ord = append(l.ord, k)
}

// openLeafSegSet opens <outDir>/leafseg for one table; ok=false when the
// build did not use --leaf-seg (or wrote no rows for it).
func openLeafSegSet(outDir string, table int, cache *frameLRU) (*leafSegSet, bool, error) {
	dir := filepath.Join(outDir, leafSegDir)
	if _, err := os.Stat(dir); err != nil {
		return nil, false, nil
	}
	pat := segTabNames[table] + ".*.seg"
	names, err := filepath.Glob(filepath.Join(dir, pat))
	if err != nil {
		return nil, false, err
	}
	s := &leafSegSet{table: table, cache: cache, paths: make(map[int]string), open: make(map[int]*segFileEntry)}
	for _, name := range names {
		base := filepath.Base(name)
		var bucket int
		if _, err := fmt.Sscanf(base, segTabNames[table]+".%x.seg", &bucket); err != nil {
			continue
		}
		s.paths[bucket] = name
		s.ids = append(s.ids, bucket)
	}
	sort.Ints(s.ids)
	return s, len(s.ids) > 0, nil
}

// Close releases the segment file handles.
func (s *leafSegSet) Close() {
	for b, e := range s.open {
		releaseSegFile(e)
		delete(s.open, b)
	}
	s.ids = nil
}

func loadLeafSegFile(f *os.File) (*leafSegFile, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var tail [16]byte
	if _, err := f.ReadAt(tail[:], st.Size()-16); err != nil {
		return nil, err
	}
	if string(tail[8:]) != leafSegMagic {
		return nil, fmt.Errorf("bad trailing magic")
	}
	footLen := int64(binary.BigEndian.Uint64(tail[:8]))
	foot := make([]byte, footLen)
	if _, err := f.ReadAt(foot, st.Size()-16-footLen); err != nil {
		return nil, err
	}
	n, m := binary.Uvarint(foot)
	if m <= 0 {
		return nil, fmt.Errorf("bad footer")
	}
	p := m
	sf := &leafSegFile{f: f, offs: make([]int64, 0, n+1), raws: make([]uint32, 0, n), kOff: make([]uint32, 0, n+1)}
	off := int64(len(leafSegMagic))
	for i := uint64(0); i < n; i++ {
		comp, m1 := binary.Uvarint(foot[p:])
		p += m1
		rawLen, m2 := binary.Uvarint(foot[p:])
		p += m2
		kl, m3 := binary.Uvarint(foot[p:])
		p += m3
		sf.offs = append(sf.offs, off)
		sf.raws = append(sf.raws, uint32(rawLen))
		sf.kOff = append(sf.kOff, uint32(len(sf.keys)))
		sf.keys = append(sf.keys, foot[p:p+int(kl)]...)
		p += int(kl)
		off += int64(comp)
	}
	sf.offs = append(sf.offs, off)
	sf.kOff = append(sf.kOff, uint32(len(sf.keys)))
	return sf, nil
}

func (s *leafSegSet) frameKey(bucket, fi int) uint64 {
	return uint64(s.table)<<48 | uint64(bucket)<<24 | uint64(fi)
}

func (s *leafSegSet) decodeFrame(bucket, fi int) (*decodedFrame, error) {
	ck := s.frameKey(bucket, fi)
	if d := s.cache.get(ck); d != nil {
		return d, nil
	}
	sf, err := s.file(bucket)
	if err != nil {
		return nil, err
	}
	fm := sf.frame(fi)
	comp := make([]byte, fm.comp)
	if _, err := sf.f.ReadAt(comp, fm.off); err != nil {
		return nil, err
	}
	raw, err := sharedSegDecoder().DecodeAll(comp, make([]byte, 0, fm.raw))
	if err != nil {
		return nil, err
	}
	d := &decodedFrame{raw: raw}
	pos := uint32(0)
	for pos < uint32(len(raw)) {
		d.offs = append(d.offs, pos)
		kl, m := binary.Uvarint(raw[pos:])
		pos += uint32(m) + uint32(kl)
		vl, m2 := binary.Uvarint(raw[pos:])
		pos += uint32(m2) + uint32(vl)
	}
	s.cache.put(ck, d)
	return d, nil
}

func (d *decodedFrame) kv(i int) ([]byte, []byte) {
	off := d.offs[i]
	kl, m := binary.Uvarint(d.raw[off:])
	k := d.raw[off+uint32(m) : off+uint32(m)+uint32(kl)]
	p := off + uint32(m) + uint32(kl)
	vl, m2 := binary.Uvarint(d.raw[p:])
	v := d.raw[p+uint32(m2) : p+uint32(m2)+uint32(vl)]
	return k, v
}

// segLeafCursor walks a leafSegSet with MDBX-cursor Seek/Next/Prev/Last
// semantics (the asOfLeaves / changedChildren contract).
type segLeafCursor struct {
	set    *leafSegSet
	bi     int // index into set.ids
	fi, ri int
	cur    *decodedFrame
	eof    bool
}

func (s *leafSegSet) Cursor() *segLeafCursor { return &segLeafCursor{set: s, eof: true} }

func (c *segLeafCursor) Close() {}

func (c *segLeafCursor) current() ([]byte, []byte, error) {
	if c.eof {
		return nil, nil, nil
	}
	k, v := c.cur.kv(c.ri)
	return k, v, nil
}

// position sets the cursor to (bucket-index, fi, ri) after decode.
func (c *segLeafCursor) position(bi, fi, ri int) error {
	d, err := c.set.decodeFrame(c.set.ids[bi], fi)
	if err != nil {
		return err
	}
	c.bi, c.fi, c.ri, c.cur, c.eof = bi, fi, ri, d, false
	return nil
}

// Seek positions at the first entry >= k.
func (c *segLeafCursor) Seek(k []byte) ([]byte, []byte, error) {
	probe := segBucketOf(c.set.table, k)
	// First bucket id >= probe.
	bi := sort.SearchInts(c.set.ids, probe)
	for ; bi < len(c.set.ids); bi++ {
		bucket := c.set.ids[bi]
		sf, err := c.set.file(bucket)
		if err != nil {
			return nil, nil, err
		}
		nf := sf.numFrames()
		if nf == 0 {
			continue
		}
		var fi, ri int
		if bucket > probe {
			fi, ri = 0, 0 // everything in a later bucket is > k
		} else {
			// Last frame whose firstKey <= k (or frame 0 if k precedes all).
			// A fold seeks forward, key after key, mostly a few rows or frames
			// ahead: from a cursor already standing in this bucket at or before
			// k, gallop from its frame instead of bisecting the whole index
			// (tens of thousands of cold 68-byte keys per probe).
			lo, hi, riLo := 0, nf, 0
			var d *decodedFrame
			if !c.eof && c.cur != nil && c.bi == bi && c.fi < nf {
				if ck, _ := c.cur.kv(c.ri); bytes.Compare(ck, k) <= 0 {
					lo = c.fi
					step := 1
					for lo+step < nf && bytes.Compare(sf.firstKey(lo+step), k) <= 0 {
						lo += step
						step *= 2
					}
					if lo+step < nf {
						hi = lo + step + 1
					}
				}
			}
			fi = lo + sort.Search(hi-lo, func(i int) bool {
				return bytes.Compare(sf.firstKey(lo+i), k) > 0
			}) - 1
			if fi < 0 {
				fi = 0
			}
			if !c.eof && c.cur != nil && c.bi == bi && c.fi == fi {
				d = c.cur // same frame: no cache lookup
				if ck, _ := d.kv(c.ri); bytes.Compare(ck, k) <= 0 {
					riLo = c.ri
				}
			} else if d, err = c.set.decodeFrame(bucket, fi); err != nil {
				return nil, nil, err
			}
			ri = riLo + sort.Search(len(d.offs)-riLo, func(i int) bool {
				kk, _ := d.kv(riLo + i)
				return bytes.Compare(kk, k) >= 0
			})
			if ri == len(d.offs) {
				// Past this frame: first row of the next frame (or next bucket).
				if fi+1 < nf {
					fi, ri = fi+1, 0
				} else {
					continue
				}
			}
		}
		if err := c.position(bi, fi, ri); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	c.eof = true
	return nil, nil, nil
}

func (c *segLeafCursor) Next() ([]byte, []byte, error) {
	if c.eof {
		return nil, nil, nil
	}
	if c.ri+1 < len(c.cur.offs) {
		c.ri++
		return c.current()
	}
	nf, err := c.set.frameCount(c.set.ids[c.bi])
	if err != nil {
		return nil, nil, err
	}
	if c.fi+1 < nf {
		if err := c.position(c.bi, c.fi+1, 0); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	for bi := c.bi + 1; bi < len(c.set.ids); bi++ {
		nf, err := c.set.frameCount(c.set.ids[bi])
		if err != nil {
			return nil, nil, err
		}
		if nf > 0 {
			if err := c.position(bi, 0, 0); err != nil {
				return nil, nil, err
			}
			return c.current()
		}
	}
	c.eof = true
	return nil, nil, nil
}

func (c *segLeafCursor) Prev() ([]byte, []byte, error) {
	if c.eof {
		return nil, nil, nil
	}
	if c.ri > 0 {
		c.ri--
		return c.current()
	}
	if c.fi > 0 {
		d, err := c.set.decodeFrame(c.set.ids[c.bi], c.fi-1)
		if err != nil {
			return nil, nil, err
		}
		if err := c.position(c.bi, c.fi-1, len(d.offs)-1); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	for bi := c.bi - 1; bi >= 0; bi-- {
		nf, err := c.set.frameCount(c.set.ids[bi])
		if err != nil {
			return nil, nil, err
		}
		if nf > 0 {
			fi := nf - 1
			d, err := c.set.decodeFrame(c.set.ids[bi], fi)
			if err != nil {
				return nil, nil, err
			}
			if err := c.position(bi, fi, len(d.offs)-1); err != nil {
				return nil, nil, err
			}
			return c.current()
		}
	}
	c.eof = true
	return nil, nil, nil
}

func (c *segLeafCursor) Last() ([]byte, []byte, error) {
	for bi := len(c.set.ids) - 1; bi >= 0; bi-- {
		nf, err := c.set.frameCount(c.set.ids[bi])
		if err != nil {
			return nil, nil, err
		}
		if nf > 0 {
			fi := nf - 1
			d, err := c.set.decodeFrame(c.set.ids[bi], fi)
			if err != nil {
				return nil, nil, err
			}
			if err := c.position(bi, fi, len(d.offs)-1); err != nil {
				return nil, nil, err
			}
			return c.current()
		}
	}
	c.eof = true
	return nil, nil, nil
}
