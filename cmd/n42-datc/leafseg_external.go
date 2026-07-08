// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// leafseg_external — bounded-memory external merge sort for oversized buckets.
//
// A hot-storage prefix (e.g. a heavily-written contract's storage trie path)
// can spill hundreds of millions of leaf rows into one bucket — tens of GB
// compressed, well over RAM when decompressed. finalizeBucket loads + sorts
// the whole bucket, so such a bucket exhausts physical memory, forces the OS to
// page (system drive to 100%), and hangs the machine.
//
// finalizeBucketExternal sorts these buckets with a fixed memory budget:
//
//	chunk:  stream the spill's frames; accumulate decoded rows until the chunk
//	        reaches a BYTE budget (independent of how large individual frames
//	        are — the earlier frame-COUNT split blew up on big-frame buckets);
//	        stable-sort the chunk and write it as an on-disk sorted run.
//	merge:  k-way merge the runs (plus any existing segment) with a min-heap,
//	        streaming one frame per run into the final segment.
//
// Peak memory = compressed input + one chunk (~2 GiB), independent of bucket
// size. RESUMABLE: completed runs are checkpoints; a re-run reuses them and
// rebuilds only the missing ones. The spill is NEVER deleted.
package main

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// extThreshold: a bucket whose .zspill exceeds this compressed size is external-
// sorted instead of loaded whole (override with N42_DATC_EXT_THRESHOLD bytes).
func extThreshold() int64 {
	if v := os.Getenv("N42_DATC_EXT_THRESHOLD"); v != "" {
		if n, e := strconv.ParseInt(v, 10, 64); e == nil && n >= 0 {
			return n
		}
	}
	return 4 << 30 // 4 GiB
}

// extChunkBytes: decoded bytes accumulated per sorted run. Peak memory is the
// compressed input plus ~2× this (the chunk plus its parse copy), so keep it
// small enough that (comp + ~2×budget) stays comfortably under RAM.
// Override with N42_DATC_EXT_CHUNK_BYTES.
func extChunkBytes() int {
	if v := os.Getenv("N42_DATC_EXT_CHUNK_BYTES"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			return n
		}
	}
	return 2 << 30 // 2 GiB decoded per run
}

// finalizeBucketExternal sorts one oversized bucket spill into dst with a fixed
// memory budget. corruptOut receives the count of kill-tail frames skipped.
func finalizeBucketExternal(zr *zstd.Decoder, enc *zstd.Encoder, src, oldSeg, dst string, corruptOut *int) error {
	comp, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Frame boundaries: split on the 4-byte zstd magic (one frame == one flush
	// of the spill writer; a frame never spans two build runs).
	var frameStarts []int
	for i := 0; i+4 <= len(comp); {
		j := bytes.Index(comp[i:], zstdMagic)
		if j < 0 {
			break
		}
		frameStarts = append(frameStarts, i+j)
		i += j + 4
	}
	budget := extChunkBytes()
	fmt.Printf("[leafseg] %s: external sort — %.2f GB compressed, %d frames, %d MiB/run\n",
		filepath.Base(src), float64(len(comp))/(1<<30), len(frameStarts), budget>>20)

	runDir := dst + ".runs"
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	corruptFrames := 0
	runIdx := 0
	var runPaths []string
	var raw []byte    // current chunk: contiguous complete rows
	var offs []uint64 // start of each row in raw
	var group []byte  // decoded bytes of consecutive good frames, not yet parsed

	// parseGroup moves complete rows from group into raw/offs. It keeps a
	// trailing partial row (a row split across the frame boundary) unless flush
	// is set (end of a good run — at a corrupt frame or EOF — where the tail is
	// incomplete and discarded, exactly as the in-RAM path does).
	parseGroup := func(flush bool) {
		p := 0
		for p < len(group) {
			kl, m := binary.Uvarint(group[p:])
			if m <= 0 || kl > uint64(len(group)) {
				break
			}
			ks := p + m
			ke := ks + int(kl)
			if ke > len(group) {
				break
			}
			vl, m2 := binary.Uvarint(group[ke:])
			if m2 <= 0 || vl > uint64(len(group)) {
				break
			}
			ve := ke + m2 + int(vl)
			if ve > len(group) {
				break
			}
			offs = append(offs, uint64(len(raw)))
			raw = append(raw, group[p:ve]...)
			p = ve
		}
		if flush || p == len(group) {
			group = group[:0]
		} else if p > 0 {
			group = append(group[:0], group[p:]...) // keep unparsed tail
		}
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
	flushRun := func() error {
		if len(offs) == 0 {
			return nil
		}
		runPath := filepath.Join(runDir, fmt.Sprintf("run.%05d.seg", runIdx))
		runPaths = append(runPaths, runPath)
		idx := runIdx
		runIdx++
		if fi, serr := os.Stat(runPath); serr == nil && fi.Size() > 0 {
			raw, offs = raw[:0], offs[:0] // resume: run already a checkpoint
			return nil
		}
		sort.SliceStable(offs, func(i, j int) bool {
			return bytes.Compare(recKey(offs[i]), recKey(offs[j])) < 0
		})
		tmp := runPath + ".tmp"
		sw, err := newSegFrameWriter(tmp, enc)
		if err != nil {
			return err
		}
		for _, o := range offs {
			if err := sw.add(raw[o:recEnd(o)], recKey(o)); err != nil {
				return err
			}
		}
		if err := sw.finish(); err != nil {
			return err
		}
		if err := os.Rename(tmp, runPath); err != nil {
			return err
		}
		fmt.Printf("[leafseg] %s: run %05d — %d rows\n", filepath.Base(src), idx, len(offs))
		raw, offs = nil, nil // release the chunk before the next one grows
		return nil
	}

	// Heal false-magic candidate splits by extending the frame end across
	// candidates until the slice decodes (see finalizeBucket for the full
	// rationale — bucket a.b5's 28 B5 row header + b5 2f fd keys forge magics).
	for fi := 0; fi < len(frameStarts); {
		start := frameStarts[fi]
		var dec []byte
		decoded := false
		for ei := fi + 1; ei <= len(frameStarts); ei++ {
			end := len(comp)
			if ei < len(frameStarts) {
				end = frameStarts[ei]
			}
			if end-start > 256<<20 {
				break // no real frame is this large — truly corrupt
			}
			var derr error
			dec, derr = zr.DecodeAll(comp[start:end], nil)
			if derr != nil {
				continue // false-magic split — extend further
			}
			fi = ei
			decoded = true
			break
		}
		if !decoded {
			parseGroup(true) // flush good rows, drop partial tail, resync
			corruptFrames++
			fi++
			continue
		}
		group = append(group, dec...)
		if len(group) >= 256<<20 { // keep the unparsed buffer bounded
			parseGroup(false)
		}
		if len(raw) >= budget {
			if err := flushRun(); err != nil {
				return err
			}
		}
	}
	parseGroup(true)
	if err := flushRun(); err != nil {
		return err
	}
	comp = nil // free the compressed input before the merge phase

	if err := mergeRuns(enc, oldSeg, dst, runPaths); err != nil {
		return err
	}
	// Runs are scratch, regenerable from the retained spill — safe to drop. The
	// spill (src) is intentionally NOT touched.
	_ = os.RemoveAll(runDir)

	if corruptOut != nil {
		*corruptOut = corruptFrames
	}
	if corruptFrames > 0 {
		fmt.Printf("[leafseg] %s: external-sorted %d runs, skipped %d corrupt frame(s) (kill-tail)\n",
			filepath.Base(src), len(runPaths), corruptFrames)
	} else {
		fmt.Printf("[leafseg] %s: external-sorted %d runs\n", filepath.Base(src), len(runPaths))
	}
	return nil
}

// mergeItem is one live iterator head in the merge heap.
type mergeItem struct {
	key []byte // copied: outlives the source iterator's frame
	idx int
}

type mergeHeap []mergeItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	if c := bytes.Compare(h[i].key, h[j].key); c != 0 {
		return c < 0
	}
	return h[i].idx < h[j].idx // stable: lower index (earlier input) first
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x any)   { *h = append(*h, x.(mergeItem)) }
func (h *mergeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// mergeRuns k-way merges the existing segment (if any, index 0 — oldest) and
// the sorted runs into dst via a min-heap, streaming one frame per input.
func mergeRuns(enc *zstd.Encoder, oldSeg, dst string, runPaths []string) error {
	var iters []*oldSegIter
	defer func() {
		for _, it := range iters {
			it.close()
		}
	}()
	open := func(path string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		sf, lerr := loadLeafSegFile(f)
		if lerr != nil {
			f.Close()
			return lerr
		}
		iters = append(iters, &oldSegIter{sf: sf, zr: zr2()})
		return nil
	}
	// Existing segment first so equal keys keep the OLD row ahead of new rows.
	// (oldSeg == dst on the in-place build path; a distinct read-only source on
	// the recast path.)
	if _, err := os.Stat(oldSeg); err == nil {
		if err := open(oldSeg); err != nil {
			return err
		}
	}
	for _, rp := range runPaths {
		if err := open(rp); err != nil {
			return err
		}
	}

	tmp := dst + ".tmp"
	out, err := newSegFrameWriter(tmp, enc)
	if err != nil {
		return err
	}

	h := &mergeHeap{}
	for i, it := range iters {
		ok, err := it.ensure()
		if err != nil {
			return err
		}
		if ok {
			h.Push(mergeItem{append([]byte{}, it.key()...), i})
		}
	}
	heap.Init(h)
	for h.Len() > 0 {
		m := heap.Pop(h).(mergeItem)
		it := iters[m.idx]
		if err := out.add(it.rec(), it.key()); err != nil {
			return err
		}
		it.next()
		ok, err := it.ensure()
		if err != nil {
			return err
		}
		if ok {
			heap.Push(h, mergeItem{append([]byte{}, it.key()...), m.idx})
		}
	}
	if err := out.finish(); err != nil {
		return err
	}
	for _, it := range iters {
		it.close()
	}
	iters = nil
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, dst)
}
