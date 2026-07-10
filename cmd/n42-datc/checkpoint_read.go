// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Checkpoint reader + checkpoint-accelerated subtree fold. A checkpoint file
// (<out>/ckpt/<table>.<block>.ckpt, v2 framed format — see checkpoint_build.go)
// holds the key-sorted live-key-set at that block. asOfLeavesCkpt bounds the
// fold to the keys that EXIST at N: it takes the keys under the fold prefix
// from the smallest checkpoint ≥ N (every key live at N was created ≤ N ≤ that
// checkpoint) and reads each key's floor value ≤ N — deletions/absent floor to
// empty and drop out. For early N the state (and thus the checkpoint slice
// under a prefix) is tiny, so this replaces the ~100M-key future-scan with a
// few-K read.
//
// Memory: only the footer frame index (~80 B/frame) is resident per opened
// checkpoint; key data is decompressed frame-by-frame (256 KiB raw) through a
// byte-bounded LRU (N42_DATC_CKPT_CACHE_MB, default 1024). The v1 single-
// stream format required materializing the WHOLE set (largest storage
// checkpoint ≈ 68 GB raw) and OOMed the 128 GB build host — v1 files are
// rejected at open with a rebuild hint.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// asOfLeavesCkpt folds the subtree under (domain, path) at N using the live-key
// checkpoints instead of scanning the whole prefix. Candidate keys = the keys
// under the fold prefix from the smallest checkpoint ≥ N (its ever-appeared
// key-set is a complete candidate set for "keys existing at N"). Each
// candidate's floor value ≤ N is read (empty ⇒ deleted/absent, dropped).
// Returns (leaves, true) when checkpoints can answer; (nil, false) when they
// cannot (no checkpoint ≥ N) so the caller falls back to the scan.
func (q *querier) asOfLeavesCkpt(domain, path []byte, n uint64) ([]foldLeaf, bool, error) {
	tab := segTabLeafA
	if domain != nil {
		tab = segTabLeafS
	}
	if q.ckpt == nil || !q.ckpt.available(tab) || n > q.ckpt.maxBlock(tab) {
		return nil, false, nil // N past the last checkpoint: fall back to scan
	}
	// Smallest checkpoint ≥ N: its ever-appeared key-set is a COMPLETE candidate
	// set for "keys existing at N" (every such key was created ≤ N ≤ above).
	_, above, _, haveAbove := q.ckpt.bracket(tab, n)
	if !haveAbove {
		if n == q.ckpt.maxBlock(tab) {
			above, haveAbove = n, true // N exactly at the last checkpoint
		} else {
			return nil, false, nil
		}
	}
	s, err := q.ckpt.load(tab, above)
	if err != nil {
		return nil, false, err
	}

	fullNibbles := path
	bytePrefix := make([]byte, 0, len(domain)+len(fullNibbles)/2)
	bytePrefix = append(bytePrefix, domain...)
	for i := 0; i+1 < len(fullNibbles); i += 2 {
		bytePrefix = append(bytePrefix, fullNibbles[i]<<4|fullNibbles[i+1])
	}
	oddNib := len(fullNibbles)%2 == 1
	var oddVal byte
	if oddNib {
		oddVal = fullNibbles[len(fullNibbles)-1]
	}

	it := s.iterUnder(bytePrefix)
	var out []foldLeaf
	for {
		key, ok, err := it.next()
		if err != nil {
			return nil, false, err
		}
		if !ok {
			break
		}
		if oddNib && key[len(bytePrefix)]>>4 != oddVal {
			continue // wrong odd-nibble branch
		}
		val, _, err := q.leafFloor(domain != nil, key, n)
		if err != nil {
			return nil, false, err
		}
		q.leafReads++
		q.distinctKeys++
		if err := q.emitFoldLeaf(domain, fullNibbles, key, val, n, &out); err != nil {
			return nil, false, err
		}
	}
	return out, true, nil
}

// ckptFrame is one frame's footer entry: compressed extent + first key.
type ckptFrame struct {
	off      int64 // compressed offset in file
	comp     int
	rows     int
	firstKey []byte
}

// ckptSet is one opened checkpoint: file handle + frame index. Key data stays
// on disk; frames are decompressed on demand through the store's LRU.
type ckptSet struct {
	st     *ckptStore
	tab    int
	block  uint64
	f      *os.File
	keyLen int
	frames []ckptFrame
}

// iterUnder returns an iterator over the keys with the given byte prefix, in
// key order. An empty prefix iterates the whole set (same candidate volume the
// caller would fold anyway — but streamed, not materialized).
func (s *ckptSet) iterUnder(prefix []byte) *ckptIter {
	// Start at the last frame whose firstKey ≤ prefix — the first key ≥ prefix
	// can be inside it; earlier frames are entirely < prefix.
	i := sort.Search(len(s.frames), func(i int) bool {
		return bytes.Compare(s.frames[i].firstKey, prefix) > 0
	})
	if i > 0 {
		i--
	}
	return &ckptIter{s: s, prefix: prefix, fi: i}
}

// ckptIter streams keys under a prefix, one decompressed frame at a time.
type ckptIter struct {
	s      *ckptSet
	prefix []byte
	fi, ri int
	raw    []byte
}

// next returns the next key with the prefix; ok=false at end of range.
func (it *ckptIter) next() ([]byte, bool, error) {
	kl := it.s.keyLen
	for {
		if it.fi >= len(it.s.frames) {
			return nil, false, nil
		}
		if it.raw == nil {
			raw, err := it.s.frame(it.fi)
			if err != nil {
				return nil, false, err
			}
			it.raw = raw
			rows := len(raw) / kl
			it.ri = sort.Search(rows, func(i int) bool {
				return bytes.Compare(raw[i*kl:(i+1)*kl], it.prefix) >= 0
			})
		}
		if (it.ri+1)*kl <= len(it.raw) {
			key := it.raw[it.ri*kl : (it.ri+1)*kl]
			it.ri++
			if !bytes.HasPrefix(key, it.prefix) {
				return nil, false, nil // sorted: past the prefix range
			}
			return key, true, nil
		}
		it.fi++
		it.raw = nil
	}
}

// frame returns frame i's decompressed keys, via the store's byte-bounded LRU.
func (s *ckptSet) frame(i int) ([]byte, error) {
	k := ckptFrameCacheKey(s.tab, s.block, i)
	if raw, ok := s.st.fcache[k]; ok {
		return raw, nil
	}
	fm := s.frames[i]
	comp := make([]byte, fm.comp)
	if _, err := s.f.ReadAt(comp, fm.off); err != nil {
		return nil, err
	}
	raw, err := s.st.zr.DecodeAll(comp, make([]byte, 0, fm.rows*s.keyLen))
	if err != nil {
		return nil, err
	}
	dbgFrameDecodes++
	dbgFrameCompBytes += int64(fm.comp)
	dbgFrameRawBytes += int64(len(raw))
	s.st.fput(k, raw)
	return raw, nil
}

// ckptFrameCacheKey packs (table, block, frameIdx): 1 bit | 39 bits | 24 bits.
func ckptFrameCacheKey(tab int, block uint64, idx int) uint64 {
	return uint64(tab)<<63 | block<<24 | uint64(idx)
}

// ckptStore lazily opens and caches checkpoint frame indexes per (table,
// block), plus a byte-bounded FIFO cache of decompressed frames shared across
// all checkpoints. Single-goroutine use (the bench/verify queriers are
// sequential) — no locking.
type ckptStore struct {
	dir     string
	blocks  [2][]uint64         // [table] sorted checkpoint blocks present
	loaded  map[uint64]*ckptSet // key: table<<40 | block
	keyLens [2]int
	zr      *zstd.Decoder

	fcache map[uint64][]byte // decompressed frames
	ford   []uint64          // insertion order (FIFO eviction)
	fbytes int64
	fcap   int64
}

// autoCkptMaxKeys is the auto-gate threshold: checkpoints whose live-key-set
// exceeds this are excluded from the fold routing. Measured on the 15.22M
// bprime2 archive (seed 42, n=200, verified): routing mid/late-N folds through
// large candidate sets is ~7× SLOWER than the dense-record path they would
// otherwise take (p50 1.1s vs 130ms), while small early-block sets are the
// whole point of ckpt-fold (kill the 100M-key future-scan). 32M keeps the
// validated ≤4M set (23.2M acct / 9.4M sto keys) and gates 5M+ (44.7M/48.1M).
// Var, not const: tests tighten it.
var autoCkptMaxKeys = uint64(32_000_000)

// openCkptStore scans <outDir>/ckpt and selects the checkpoints to route folds
// through. maxBlock: 0 = auto (per-file key-count gate at autoCkptMaxKeys),
// >0 = hard block cutoff (keep blocks ≤ maxBlock), <0 = keep everything.
// Blocks past the kept set fall back to the record/scan path — which the
// benchmark shows is faster there anyway.
func openCkptStore(outDir string, maxBlock int64) *ckptStore {
	st := &ckptStore{
		dir:    filepath.Join(outDir, ckptDir),
		loaded: map[uint64]*ckptSet{},
		zr:     zr2(),
		fcache: map[uint64][]byte{},
		fcap:   1024 << 20,
	}
	if v := os.Getenv("N42_DATC_CKPT_CACHE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			st.fcap = int64(mb) << 20
		}
	}
	st.keyLens = [2]int{32, 72}
	for tab := 0; tab < 2; tab++ {
		matches, _ := filepath.Glob(filepath.Join(st.dir, strconv.Itoa(tab)+".*.ckpt"))
		var gated []uint64
		for _, m := range matches {
			// base = "<tab>.<block>.ckpt"
			parts := strings.Split(strings.TrimSuffix(filepath.Base(m), ".ckpt"), ".")
			if len(parts) != 2 || parts[0] != strconv.Itoa(tab) {
				continue
			}
			b, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				continue
			}
			estKeys, v2 := ckptPeek(m)
			if !v2 {
				fmt.Fprintf(os.Stderr, "ckpt: SKIP %s — legacy v1 (single zstd stream, reader would materialize the whole set); rebuild with `n42-datc checkpoint-build`\n", filepath.Base(m))
				continue
			}
			switch {
			case maxBlock > 0 && b > uint64(maxBlock):
				gated = append(gated, b)
				continue
			case maxBlock == 0 && estKeys > autoCkptMaxKeys:
				gated = append(gated, b)
				continue
			}
			st.blocks[tab] = append(st.blocks[tab], b)
		}
		sort.Slice(st.blocks[tab], func(i, j int) bool { return st.blocks[tab][i] < st.blocks[tab][j] })
		if len(gated) > 0 {
			sort.Slice(gated, func(i, j int) bool { return gated[i] < gated[j] })
			reason := fmt.Sprintf("> --ckpt-max-block %d", maxBlock)
			if maxBlock == 0 {
				reason = fmt.Sprintf("auto: > ~%dM live keys", autoCkptMaxKeys/1_000_000)
			}
			fmt.Printf("ckpt: table=%d gated %d checkpoint(s) %d..%d (%s — the record path is faster for large sets)\n",
				tab, len(gated), gated[0], gated[len(gated)-1], reason)
		}
	}
	return st
}

// ckptPeek reads a checkpoint's tail + footer head only: v2 magic check and an
// estimated live-key count (frameCount × keys-per-full-frame; overestimates by
// at most one partial frame). No key data is touched.
func ckptPeek(path string) (estKeys uint64, v2 bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < int64(len(ckptMagic))+16 {
		return 0, false
	}
	var tail [16]byte
	if _, err := f.ReadAt(tail[:], st.Size()-16); err != nil {
		return 0, false
	}
	if string(tail[8:]) != ckptMagic {
		return 0, false
	}
	footLen := int64(binary.BigEndian.Uint64(tail[:8]))
	if footLen <= 0 || footLen > st.Size()-16-int64(len(ckptMagic)) {
		return 0, false
	}
	head := make([]byte, 24)
	if int64(len(head)) > footLen {
		head = head[:footLen]
	}
	if _, err := f.ReadAt(head, st.Size()-16-footLen); err != nil {
		return 0, false
	}
	kl, m := binary.Uvarint(head)
	if m <= 0 || kl == 0 {
		return 0, false
	}
	nFrames, m2 := binary.Uvarint(head[m:])
	if m2 <= 0 {
		return 0, false
	}
	return nFrames * (ckptFrameRaw / kl), true
}

// Close releases the opened checkpoint file handles and the frame cache.
func (st *ckptStore) Close() {
	for k, s := range st.loaded {
		_ = s.f.Close()
		delete(st.loaded, k)
	}
	if st.zr != nil {
		st.zr.Close()
		st.zr = nil
	}
	st.fcache = map[uint64][]byte{}
	st.ford, st.fbytes = nil, 0
}

// fput inserts a decompressed frame, evicting FIFO past the byte cap.
func (st *ckptStore) fput(k uint64, raw []byte) {
	if _, ok := st.fcache[k]; ok {
		return
	}
	for st.fbytes+int64(len(raw)) > st.fcap && len(st.ford) > 0 {
		old := st.ford[0]
		st.ford = st.ford[1:]
		st.fbytes -= int64(len(st.fcache[old]))
		delete(st.fcache, old)
	}
	st.fcache[k] = raw
	st.ford = append(st.ford, k)
	st.fbytes += int64(len(raw))
}

// available reports whether any checkpoints exist for the table.
func (st *ckptStore) available(tab int) bool { return len(st.blocks[tab]) > 0 }

// maxBlock is the highest checkpoint block for the table (0 if none).
func (st *ckptStore) maxBlock(tab int) uint64 {
	bs := st.blocks[tab]
	if len(bs) == 0 {
		return 0
	}
	return bs[len(bs)-1]
}

// bracket returns the checkpoint blocks below (≤n) and above (>n) n. below=^0
// when none is ≤ n.
func (st *ckptStore) bracket(tab int, n uint64) (below, above uint64, haveBelow, haveAbove bool) {
	below = ^uint64(0)
	for _, b := range st.blocks[tab] {
		if b <= n {
			below, haveBelow = b, true
		} else {
			above, haveAbove = b, true
			break
		}
	}
	return
}

// load opens a checkpoint's frame index (footer only — no key data).
func (st *ckptStore) load(tab int, block uint64) (*ckptSet, error) {
	key := uint64(tab)<<40 | block
	if s, ok := st.loaded[key]; ok {
		return s, nil
	}
	path := filepath.Join(st.dir, strconv.Itoa(tab)+"."+strconv.FormatUint(block, 10)+".ckpt")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	s, err := loadCkptSet(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.st, s.tab, s.block = st, tab, block
	if s.keyLen != st.keyLens[tab] {
		f.Close()
		return nil, fmt.Errorf("%s: keyLen %d != expected %d", path, s.keyLen, st.keyLens[tab])
	}
	st.loaded[key] = s
	return s, nil
}

// loadCkptSet parses the v2 footer into a frame index.
func loadCkptSet(f *os.File) (*ckptSet, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < int64(len(ckptMagic))+16 {
		return nil, fmt.Errorf("truncated checkpoint")
	}
	var tail [16]byte
	if _, err := f.ReadAt(tail[:], fi.Size()-16); err != nil {
		return nil, err
	}
	if string(tail[8:]) != ckptMagic {
		return nil, fmt.Errorf("legacy v1 checkpoint (single zstd stream) — rebuild with `n42-datc checkpoint-build`")
	}
	footLen := int64(binary.BigEndian.Uint64(tail[:8]))
	if footLen <= 0 || footLen > fi.Size()-16-int64(len(ckptMagic)) {
		return nil, fmt.Errorf("bad footer length %d", footLen)
	}
	foot := make([]byte, footLen)
	if _, err := f.ReadAt(foot, fi.Size()-16-footLen); err != nil {
		return nil, err
	}
	kl, m := binary.Uvarint(foot)
	if m <= 0 || kl == 0 {
		return nil, fmt.Errorf("bad footer keyLen")
	}
	p := m
	nFrames, m2 := binary.Uvarint(foot[p:])
	if m2 <= 0 {
		return nil, fmt.Errorf("bad footer frame count")
	}
	p += m2
	s := &ckptSet{f: f, keyLen: int(kl), frames: make([]ckptFrame, 0, nFrames)}
	off := int64(len(ckptMagic))
	for i := uint64(0); i < nFrames; i++ {
		comp, m1 := binary.Uvarint(foot[p:])
		if m1 <= 0 {
			return nil, fmt.Errorf("bad frame %d compLen", i)
		}
		p += m1
		rows, m2 := binary.Uvarint(foot[p:])
		if m2 <= 0 || p+m2+int(kl) > len(foot) {
			return nil, fmt.Errorf("bad frame %d rows/firstKey", i)
		}
		p += m2
		fk := append([]byte{}, foot[p:p+int(kl)]...)
		p += int(kl)
		s.frames = append(s.frames, ckptFrame{off: off, comp: int(comp), rows: int(rows), firstKey: fk})
		off += int64(comp)
	}
	return s, nil
}
