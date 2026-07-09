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
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const (
	segTabLeafA = 0
	segTabLeafS = 1
	segTabChgA  = 2
	segTabChgS  = 3

	leafSegMagic   = "DATCLS1\n"
	leafFrameRaw   = 256 << 10 // target uncompressed bytes per frame
	leafSpillDir   = "leafspill"
	leafSegDir     = "leafseg"
	leafFrameCache = 1536 // decompressed frames kept hot (~48 MB at 32 KiB fine frames; pre-recast 256 KiB frames make this ~384 MB — still fine on the build host)

	// Back-compat aliases (older call sites / tests).
	leafTableA = segTabLeafA
	leafTableS = segTabLeafS
)

var segTabNames = [4]string{"a", "s", "ca", "cs"}

// segPrefixLen is the number of leading key bytes that form the bucket id.
// Leaves bucket on the hashed key's first byte (uniform). Chg rows bucket on
// (level byte, second byte) — the second byte is domain[0] for storage rows
// and the first path nibble for account rows.
var segPrefixLen = [4]int{1, 1, 2, 2}

func segBucketOf(table int, k []byte) int {
	if len(k) == 0 {
		return 0
	}
	if segPrefixLen[table] == 1 {
		return int(k[0])
	}
	b := int(k[0]) << 8
	if len(k) > 1 {
		b |= int(k[1])
	}
	return b
}

func segFileName(table, bucket int) string {
	if segPrefixLen[table] == 1 {
		return fmt.Sprintf("%s.%02x", segTabNames[table], bucket)
	}
	return fmt.Sprintf("%s.%04x", segTabNames[table], bucket)
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
	rows    [4]uint64
	scratch []byte
}

func newLeafSpillWriter(outDir string) (*leafSpillWriter, error) {
	return newLeafSpillWriterDir(filepath.Join(outDir, leafSpillDir))
}

// newLeafSpillWriterDir opens a spill writer at an explicit directory —
// cs-to-spill writes new-range rows to leafspill2/ so the original spill (the
// durable truth) is never touched.
func newLeafSpillWriterDir(dir string) (*leafSpillWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &leafSpillWriter{dir: dir, streams: make(map[int]*spillStream)}, nil
}

func (w *leafSpillWriter) stream(table, bucket int) (*spillStream, error) {
	id := table<<16 | bucket
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
		// SpeedDefault (kept deliberately, favouring spill compression ratio).
		// EVERY spill stream keeps its encoder open for the whole run — the 2 leaf
		// tables (256 buckets each) PLUS the 2 chg tables (2-byte buckets) total
		// ~1.8k resident encoders at ~2.63 MB each (doubleFast, two hash tables) =
		// ~4.7 GB steady-state heap. NOTE: the WithWindowSize 256 KiB->64 KiB below
		// is NEARLY a no-op for memory — the hash tables are window-INDEPENDENT, so
		// only the small window buffer shrank (~100 MB total); an earlier claim that
		// it saved ~3 GB was wrong (it compared an early-resume heap, before all
		// encoders existed, against a steady-state one). The single-table
		// SpeedFastest would cut per-encoder memory ~38% (-> ~2.9 GB) but trades
		// spill ratio, which we keep. zstd frames are self-describing, so either
		// window still decodes (resume-safe).
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(1<<16))
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
// removes the spill dir. One bucket is processed at a time (decoded rows for
// a 25M-mainnet bucket are single-digit GB — in-RAM sortable).
func finalizeLeafSegments(outDir string) error {
	// In-place semantics (build resume): spill=leafspill, merge source == output.
	return finalizeLeafSegmentsOpts(outDir, leafSpillDir, leafSegDir, leafSegDir)
}

// finalizeLeafSegmentsOpts is the parameterized finalize: rows from
// <outDir>/<spillSub> merge-stream with the segments in <outDir>/<oldSegSub>
// (read-only unless == outSegSub) into <outDir>/<outSegSub>. With oldSegSub !=
// outSegSub this is the RECAST path: existing segments are never mutated (the
// backup rule), and the output picks up the current segFrameRawTarget — used
// by the full-range fine-frame recast (old leafseg + cs-to-spill leafspill2 →
// leafseg2 at 32 KiB frames).
func finalizeLeafSegmentsOpts(outDir, spillSub, oldSegSub, outSegSub string) error {
	spill := filepath.Join(outDir, spillSub)
	oldd := filepath.Join(outDir, oldSegSub)
	segd := filepath.Join(outDir, outSegSub)
	if err := os.MkdirAll(segd, 0o755); err != nil {
		return err
	}
	names, err := filepath.Glob(filepath.Join(spill, "*.zspill"))
	if err != nil {
		return err
	}
	// Recast: buckets with no new spill rows still need re-encoding (frame size)
	// — include every old segment as a spill-less bucket.
	if oldd != segd {
		olds, err := filepath.Glob(filepath.Join(oldd, "*.seg"))
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(names))
		for _, n := range names {
			b := filepath.Base(n)
			have[b[:len(b)-len(".zspill")]] = true
		}
		for _, o := range olds {
			b := filepath.Base(o)
			base := b[:len(b)-len(".seg")]
			if !have[base] {
				names = append(names, filepath.Join(spill, base+".zspill")) // may not exist; tolerated
			}
		}
	}
	// Bucket-parallel recast: buckets are independent (disjoint key prefixes),
	// so re-compression — the ~470 GB SpeedBetterCompression bottleneck — fans
	// across cores. Each worker owns a decoder+encoder (EncodeAll on a shared
	// encoder is safe, but per-worker avoids WithEncoderConcurrency
	// oversubscription). Memory is gated two ways: a total worker cap, and a
	// SEPARATE heavy-bucket semaphore — a bucket whose compressed spill exceeds
	// heavyBytes reads the whole file into RAM (in-RAM sort) or streams it via
	// the external sort's whole-file read (~17 GB for s.ab), so only a few may
	// run at once. Small buckets (the vast majority) run up to the worker cap.
	workers := runtime.NumCPU() - 2
	if v := os.Getenv("N42_DATC_FINALIZE_WORKERS"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			workers = n
		}
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(names) {
		workers = len(names)
	}
	heavyBytes := int64(3) << 30 // >N GiB compressed ⇒ heavy (whole-file read)
	if v := os.Getenv("N42_DATC_FINALIZE_HEAVY_GB"); v != "" {
		if f, e := strconv.ParseFloat(v, 64); e == nil && f > 0 {
			heavyBytes = int64(f * float64(int64(1)<<30))
		}
	}
	heavyMax := 2
	if v := os.Getenv("N42_DATC_FINALIZE_HEAVY"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			heavyMax = n
		}
	}

	// Big buckets first: better load balance (a 17 GB bucket started last would
	// tail the whole run).
	sort.Slice(names, func(i, j int) bool {
		fi, _ := os.Stat(names[i])
		fj, _ := os.Stat(names[j])
		var si, sj int64
		if fi != nil {
			si = fi.Size()
		}
		if fj != nil {
			sj = fj.Size()
		}
		return si > sj
	})

	fmt.Printf("[leafseg] recast %d buckets: %d workers, heavy(>%dGiB) cap %d, %d KiB frames\n",
		len(names), workers, heavyBytes>>30, heavyMax, segFrameRawTarget>>10)

	jobs := make(chan string, len(names))
	for _, src := range names {
		jobs <- src
	}
	close(jobs)

	heavySem := make(chan struct{}, heavyMax)
	var mu sync.Mutex
	totalCorrupt := 0
	var firstErr error
	var done int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			zr := zr2()
			defer zr.Close()
			enc, eerr := zstd.NewWriter(nil,
				zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
				zstd.WithEncoderConcurrency(1))
			if eerr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = eerr
				}
				mu.Unlock()
				return
			}
			defer enc.Close()
			for src := range jobs {
				mu.Lock()
				stop := firstErr != nil
				mu.Unlock()
				if stop {
					return
				}
				base0 := filepath.Base(src)
				dst0 := filepath.Join(segd, base0[:len(base0)-len(".zspill")]+".seg")
				// Resume: a complete .seg (atomic tmp→rename) means this bucket
				// already recast — skip it (only meaningful when segd != oldd).
				if segd != oldd {
					if _, e := os.Stat(dst0); e == nil {
						mu.Lock()
						done++
						mu.Unlock()
						continue
					}
				}
				heavy := false
				if fi, e := os.Stat(src); e == nil && fi.Size() > heavyBytes {
					heavy = true
					heavySem <- struct{}{}
				}
				base := filepath.Base(src)
				segName := base[:len(base)-len(".zspill")] + ".seg"
				dst := filepath.Join(segd, segName)
				oldSeg := filepath.Join(oldd, segName)
				cf := 0
				err := finalizeBucket(zr, enc, src, oldSeg, dst, &cf)
				if heavy {
					<-heavySem
				}
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("bucket %s: %w", base, err)
				}
				totalCorrupt += cf
				done++
				if done%64 == 0 || done == int64(len(names)) {
					fmt.Printf("[leafseg] recast %d/%d buckets\n", done, len(names))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	// NEVER delete the spill (operator rule): every .zspill is kept as the
	// durable, resumable recovery source — segments are derived, the spill is
	// the truth. Removal is always a manual op after `verify`.
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
	// NEVER delete the spill (operator rule) even when every bucket was clean:
	// it stays as the resumable recovery source. Remove it manually after a
	// successful `verify` if the space is ever needed.
	return nil
}

func finalizeBucket(zr *zstd.Decoder, enc *zstd.Encoder, src, oldSeg, dst string, corruptOut *int) error {
	// Judge by size: an oversized bucket (a hot-storage prefix whose decompressed
	// rows exceed RAM) is sorted by a bounded-memory external merge sort. Loading
	// it whole — as the path below does — exhausts physical memory, fills the
	// system drive via paging, and can hard-crash marginal hardware. The 84
	// mainnet-25M buckets that fit in RAM are all well under extSpillThreshold;
	// only true monster prefixes cross it.
	if fi, serr := os.Stat(src); serr == nil && fi.Size() > extThreshold() {
		return finalizeBucketExternal(zr, enc, src, oldSeg, dst, corruptOut)
	}
	var comp []byte
	if f, err := os.Open(src); err == nil {
		comp, err = io.ReadAll(bufio.NewReaderSize(f, 1<<20))
		f.Close()
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	} // missing spill (recast bucket with no new rows): re-encode oldSeg only
	// Kill-resilient decode: a hard-killed --leaf-seg build leaves a TRUNCATED
	// zstd frame at the tail of that run's stream; a resumed build then appends
	// a SECOND, cleanly-closed zstd stream to the same file. A plain
	// io.ReadAll over the concatenation dies at the truncated frame
	// ("reserved bits not zero"). Recover by decoding frame-by-frame (split on
	// the 4-byte zstd magic), accumulating consecutive good frames into one
	// contiguous group, and resyncing at the next frame whenever one fails to
	// decode. Rows are parsed per group, so a group's trailing partial row —
	// and the rows in the dropped truncated frame — are simply discarded; the
	// next group begins at a fresh frame boundary (a resumed run's stream
	// starts row-aligned, and a single frame never spans two runs). Loss is
	// bounded to the few rows buffered in the kill-tail frame.
	zstdMagic := []byte{0x28, 0xb5, 0x2f, 0xfd}
	var frameStarts []int
	for i := 0; i+4 <= len(comp); {
		j := bytes.Index(comp[i:], zstdMagic)
		if j < 0 {
			break
		}
		frameStarts = append(frameStarts, i+j)
		i += j + 4
	}
	var raw []byte    // recovered, row-aligned concatenation
	var offs []uint64 // start of each record in raw
	appendGroup := func(g []byte) {
		p := 0
		for p < len(g) {
			kl, m := binary.Uvarint(g[p:])
			if m <= 0 || kl > uint64(len(g)) {
				return // partial/garbage tail of this group — stop here
			}
			ks := p + m
			ke := ks + int(kl)
			if ke > len(g) {
				return
			}
			vl, m2 := binary.Uvarint(g[ke:])
			if m2 <= 0 || vl > uint64(len(g)) {
				return
			}
			ve := ke + m2 + int(vl)
			if ve > len(g) {
				return
			}
			offs = append(offs, uint64(len(raw)))
			raw = append(raw, g[p:ve]...)
			p = ve
		}
	}
	// The magic-scan yields FALSE candidates too: a row is uvarint(keyLen)+key,
	// so bucket a.b5's rows all start 28 B5 (keyLen 40 = 0x28) and a key
	// beginning b5 2f fd stored as raw literals completes the zstd magic
	// 28 B5 2F FD inside a healthy frame. Decoding start→next-candidate then
	// splits that frame and BOTH halves fail — the whole real frame's rows were
	// discarded as "corrupt" (the v5 a.b5 61-frame / 425k-row loss, bug #2).
	// Heal: extend the end across subsequent candidates until the slice
	// decodes; only a genuinely truncated frame (kill-tail) remains corrupt.
	var group []byte
	corruptFrames := 0
	for fi := 0; fi < len(frameStarts); {
		start := frameStarts[fi]
		decoded := false
		for ei := fi + 1; ei <= len(frameStarts); ei++ {
			end := len(comp)
			if ei < len(frameStarts) {
				end = frameStarts[ei]
			}
			if end-start > 256<<20 {
				break // no real frame is this large — truly corrupt
			}
			dec, derr := zr.DecodeAll(comp[start:end], nil)
			if derr != nil {
				continue // false-magic split candidate — extend further
			}
			group = append(group, dec...)
			fi = ei
			decoded = true
			break
		}
		if !decoded {
			// Truncated/corrupt frame (kill-tail): flush the current contiguous
			// group's complete rows and resync at the next frame boundary.
			appendGroup(group)
			group = group[:0]
			corruptFrames++
			fi++
		}
	}
	appendGroup(group)
	if corruptFrames > 0 {
		fmt.Printf("[leafseg] %s: skipped %d corrupt frame(s) (kill-tail), recovered %d rows\n",
			filepath.Base(src), corruptFrames, len(offs))
	}
	recKey := func(off uint64) []byte {
		kl, m := binary.Uvarint(raw[off:])
		return raw[off+uint64(m) : off+uint64(m)+kl]
	}
	recEnd := func(off uint64) uint64 {
		kl, m := binary.Uvarint(raw[off:])
		p := off + uint64(m) + kl
		vl, m2 := binary.Uvarint(raw[p:])
		return p + uint64(m2) + vl
	}
	// Sort by full key (which embeds the block/epoch suffix → version order).
	// STABLE: duplicate keys (a resumed build re-spilling an overlap) keep
	// arrival order, so output is deterministic.
	sort.SliceStable(offs, func(i, j int) bool {
		return bytes.Compare(recKey(offs[i]), recKey(offs[j])) < 0
	})

	// A RESUMED build appends to a bucket that was already finalized: merge
	// the existing segment (sorted, streamed frame by frame) with the new
	// rows. Equal keys keep the OLD row first — arrival order, deterministic.
	// In the recast path oldSeg != dst: the source segment is read-only.
	var old *oldSegIter
	if f, err := os.Open(oldSeg); err == nil {
		sf, lerr := loadLeafSegFile(f)
		if lerr != nil {
			f.Close()
			return fmt.Errorf("existing segment: %w", lerr)
		}
		old = &oldSegIter{sf: sf, zr: zr2()}
		defer old.close()
	}

	tmp := dst + ".tmp"
	sw, err := newSegFrameWriter(tmp, enc)
	if err != nil {
		return err
	}
	ni := 0
	emitNew := func() error {
		end := recEnd(offs[ni])
		err := sw.add(raw[offs[ni]:end], recKey(offs[ni]))
		ni++
		return err
	}
	for old != nil && old.valid() {
		ok, oerr := old.ensure()
		if oerr != nil {
			return oerr
		}
		if !ok {
			break
		}
		for ni < len(offs) && bytes.Compare(recKey(offs[ni]), old.key()) < 0 {
			if err := emitNew(); err != nil {
				return err
			}
		}
		if err := sw.add(old.rec(), old.key()); err != nil {
			return err
		}
		old.next()
	}
	for ni < len(offs) {
		if err := emitNew(); err != nil {
			return err
		}
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

// segFrameWriter emits the frames + footer format of one segment file.
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
}

func newSegFrameWriter(path string, enc *zstd.Encoder) (*segFrameWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := &segFrameWriter{f: f, bw: bufio.NewWriterSize(f, 1<<20), enc: enc}
	if _, err := w.bw.WriteString(leafSegMagic); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// segFrameRawTarget is the uncompressed frame-size target used by segment
// writers. Default = leafFrameRaw (256 KiB). finalize-leaves --frame-kb
// overrides it: smaller frames mean a point read (leafFloor / ckpt fold)
// decompresses proportionally fewer bytes — the fine-frame lever that cuts a
// proof's decompMB ~8x at 32 KiB.
var segFrameRawTarget = leafFrameRaw

func (w *segFrameWriter) add(rec, key []byte) error {
	if w.firstKey == nil {
		w.firstKey = append([]byte{}, key...)
	}
	w.frame = append(w.frame, rec...)
	if len(w.frame) >= segFrameRawTarget {
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

func (it *oldSegIter) valid() bool { return !it.closed && it.fi < len(it.sf.frames) }

// ensure decodes the current frame; returns false at end.
func (it *oldSegIter) ensure() (bool, error) {
	for {
		if !it.valid() {
			return false, nil
		}
		if it.raw == nil {
			fm := it.sf.frames[it.fi]
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
	off      int64 // compressed offset in file
	comp     int
	raw      int
	firstKey []byte
}

type leafSegFile struct {
	f      *os.File
	frames []frameMeta
}

// leafSegSet is the reader for one table's bucket segments.
type leafSegSet struct {
	buckets map[int]*leafSegFile
	ids     []int // sorted bucket ids (bucket order == key order)
	table   int
	cache   *frameLRU
}

type decodedFrame struct {
	raw  []byte
	offs []uint32 // record starts
}

type frameLRU struct {
	m   map[uint64]*decodedFrame // key: table<<48 | bucket<<24 | frameIdx
	ord []uint64
}

func newFrameLRU() *frameLRU { return &frameLRU{m: make(map[uint64]*decodedFrame)} }

func (l *frameLRU) get(k uint64) *decodedFrame { return l.m[k] }
func (l *frameLRU) put(k uint64, d *decodedFrame) {
	if _, ok := l.m[k]; ok {
		return
	}
	if len(l.ord) >= leafFrameCache {
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
	s := &leafSegSet{table: table, cache: cache, buckets: make(map[int]*leafSegFile)}
	for _, name := range names {
		base := filepath.Base(name)
		var bucket int
		if _, err := fmt.Sscanf(base, segTabNames[table]+".%x.seg", &bucket); err != nil {
			continue
		}
		f, err := os.Open(name)
		if err != nil {
			return nil, false, err
		}
		sf, err := loadLeafSegFile(f)
		if err != nil {
			f.Close()
			return nil, false, fmt.Errorf("%s: %w", name, err)
		}
		s.buckets[bucket] = sf
		s.ids = append(s.ids, bucket)
	}
	sort.Ints(s.ids)
	return s, len(s.ids) > 0, nil
}

// Close releases the segment file handles.
func (s *leafSegSet) Close() {
	for b, sf := range s.buckets {
		_ = sf.f.Close()
		delete(s.buckets, b)
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
	sf := &leafSegFile{f: f, frames: make([]frameMeta, 0, n)}
	off := int64(len(leafSegMagic))
	for i := uint64(0); i < n; i++ {
		comp, m1 := binary.Uvarint(foot[p:])
		p += m1
		rawLen, m2 := binary.Uvarint(foot[p:])
		p += m2
		kl, m3 := binary.Uvarint(foot[p:])
		p += m3
		fk := append([]byte{}, foot[p:p+int(kl)]...)
		p += int(kl)
		sf.frames = append(sf.frames, frameMeta{off: off, comp: int(comp), raw: int(rawLen), firstKey: fk})
		off += int64(comp)
	}
	return sf, nil
}

func (s *leafSegSet) frameKey(bucket, fi int) uint64 {
	return uint64(s.table)<<48 | uint64(bucket)<<24 | uint64(fi)
}

// Diagnostic counters (proof-bench --profile): frame decompressions and bytes.
var dbgFrameDecodes, dbgFrameCompBytes, dbgFrameRawBytes int64

func (s *leafSegSet) decodeFrame(bucket, fi int) (*decodedFrame, error) {
	ck := s.frameKey(bucket, fi)
	if d := s.cache.get(ck); d != nil {
		return d, nil
	}
	sf := s.buckets[bucket]
	fm := sf.frames[fi]
	dbgFrameDecodes++
	dbgFrameCompBytes += int64(fm.comp)
	dbgFrameRawBytes += int64(fm.raw)
	comp := make([]byte, fm.comp)
	if _, err := sf.f.ReadAt(comp, fm.off); err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	raw, err := zr.DecodeAll(comp, make([]byte, 0, fm.raw))
	zr.Close()
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
		sf := c.set.buckets[bucket]
		if len(sf.frames) == 0 {
			continue
		}
		var fi, ri int
		if bucket > probe {
			fi, ri = 0, 0 // everything in a later bucket is > k
		} else {
			// Last frame whose firstKey <= k (or frame 0 if k precedes all).
			fi = sort.Search(len(sf.frames), func(i int) bool {
				return bytes.Compare(sf.frames[i].firstKey, k) > 0
			}) - 1
			if fi < 0 {
				fi = 0
			}
			d, err := c.set.decodeFrame(bucket, fi)
			if err != nil {
				return nil, nil, err
			}
			ri = sort.Search(len(d.offs), func(i int) bool {
				kk, _ := d.kv(i)
				return bytes.Compare(kk, k) >= 0
			})
			if ri == len(d.offs) {
				// Past this frame: first row of the next frame (or next bucket).
				if fi+1 < len(sf.frames) {
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
	sf := c.set.buckets[c.set.ids[c.bi]]
	if c.fi+1 < len(sf.frames) {
		if err := c.position(c.bi, c.fi+1, 0); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	for bi := c.bi + 1; bi < len(c.set.ids); bi++ {
		if bf := c.set.buckets[c.set.ids[bi]]; len(bf.frames) > 0 {
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
		if bf := c.set.buckets[c.set.ids[bi]]; len(bf.frames) > 0 {
			fi := len(bf.frames) - 1
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
		if bf := c.set.buckets[c.set.ids[bi]]; len(bf.frames) > 0 {
			fi := len(bf.frames) - 1
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
