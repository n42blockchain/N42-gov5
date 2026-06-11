// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// leafseg — streaming leaf-history segment store.
//
// The leaf-history tables (DatcLeafA/DatcLeafS) are the bulk of a DATC build
// (~2.2 TB in MDBX format at mainnet 25M scale — over the disk budget), yet
// the builder never reads them back and the rows are immutable on write. So
// in --leaf-seg mode they bypass MDBX entirely:
//
//	build:    rows (key|block8 → value) arrive in block order and append to
//	          256 per-bucket zstd spill streams (bucket = key[0]).
//	finalize: per bucket, decode all rows, sort by full key (stable w.r.t.
//	          arrival, and the key embeds the block, so this is (key, block)
//	          order), write a static segment: zstd frames (~256 KiB raw) with
//	          a footer index of each frame's first key.
//	verify:   segLeafCursor implements the same Seek/Next/Prev/Last contract
//	          asOfLeaves uses on an MDBX cursor, with an LRU of decompressed
//	          frames. The frame index makes the floor jump (key|n+1 then
//	          Prev) a binary search.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/klauspost/compress/zstd"
)

const (
	leafTableA = 0
	leafTableS = 1

	leafSegMagic   = "DATCLS1\n"
	leafFrameRaw   = 256 << 10 // target uncompressed bytes per frame
	leafSpillDir   = "leafspill"
	leafSegDir     = "leafseg"
	leafFrameCache = 192 // decompressed frames kept hot (~48 MB)
)

func leafTableName(table int) string {
	if table == leafTableA {
		return "a"
	}
	return "s"
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
	streams [2][256]*spillStream
	rows    [2]uint64
	scratch []byte
}

func newLeafSpillWriter(outDir string) (*leafSpillWriter, error) {
	dir := filepath.Join(outDir, leafSpillDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &leafSpillWriter{dir: dir}, nil
}

func (w *leafSpillWriter) stream(table, bucket int) (*spillStream, error) {
	if s := w.streams[table][bucket]; s != nil {
		return s, nil
	}
	name := filepath.Join(w.dir, fmt.Sprintf("%s.%02x.zspill", leafTableName(table), bucket))
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
	w.streams[table][bucket] = s
	return s, nil
}

// add appends one row. k must start with the bucketing byte (hashed key /
// domain addrHash first byte).
func (w *leafSpillWriter) add(table int, k, v []byte) error {
	s, err := w.stream(table, int(k[0]))
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

func (w *leafSpillWriter) close() error {
	for t := range w.streams {
		for b := range w.streams[t] {
			s := w.streams[t][b]
			if s == nil {
				continue
			}
			if err := s.zw.Close(); err != nil {
				return err
			}
			if err := s.bw.Flush(); err != nil {
				return err
			}
			if err := s.f.Close(); err != nil {
				return err
			}
			w.streams[t][b] = nil
		}
	}
	return nil
}

// finalizeLeafSegments turns the spill files into sorted static segments and
// removes the spill dir. One bucket is processed at a time (decoded rows for
// a 25M-mainnet bucket are ~7 GB — in-RAM sortable).
func finalizeLeafSegments(outDir string) error {
	spill := filepath.Join(outDir, leafSpillDir)
	segd := filepath.Join(outDir, leafSegDir)
	if err := os.MkdirAll(segd, 0o755); err != nil {
		return err
	}
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer zr.Close()
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
		zstd.WithEncoderConcurrency(2))
	if err != nil {
		return err
	}
	defer enc.Close()

	for t := 0; t < 2; t++ {
		for b := 0; b < 256; b++ {
			src := filepath.Join(spill, fmt.Sprintf("%s.%02x.zspill", leafTableName(t), b))
			if _, err := os.Stat(src); err != nil {
				continue // bucket never written
			}
			if err := finalizeBucket(zr, enc, src,
				filepath.Join(segd, fmt.Sprintf("%s.%02x.seg", leafTableName(t), b))); err != nil {
				return fmt.Errorf("bucket %s.%02x: %w", leafTableName(t), b, err)
			}
			_ = os.Remove(src)
		}
	}
	return os.RemoveAll(spill)
}

func finalizeBucket(zr *zstd.Decoder, enc *zstd.Encoder, src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := zr.Reset(bufio.NewReaderSize(f, 1<<20)); err != nil {
		return err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return err
	}
	// Decode row boundaries.
	var offs []uint64 // start of each record in raw
	pos := uint64(0)
	for pos < uint64(len(raw)) {
		offs = append(offs, pos)
		kl, m := binary.Uvarint(raw[pos:])
		if m <= 0 {
			return fmt.Errorf("corrupt spill at %d", pos)
		}
		pos += uint64(m) + kl
		vl, m2 := binary.Uvarint(raw[pos:])
		if m2 <= 0 {
			return fmt.Errorf("corrupt spill at %d", pos)
		}
		pos += uint64(m2) + vl
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
	// Sort by full key (which embeds the block suffix → (key, block) order).
	// STABLE: duplicate (key, block) rows (a resumed build re-spilling an
	// overlap) keep arrival order, so output is deterministic.
	sort.SliceStable(offs, func(i, j int) bool {
		return bytes.Compare(recKey(offs[i]), recKey(offs[j])) < 0
	})

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)
	if _, err := bw.WriteString(leafSegMagic); err != nil {
		return err
	}

	// Emit frames of ~leafFrameRaw uncompressed bytes.
	type frameMetaW struct {
		comp, rawLen int
		firstKey     []byte
	}
	var metas []frameMetaW
	var frame []byte
	var firstKey []byte
	flush := func() error {
		if len(frame) == 0 {
			return nil
		}
		comp := enc.EncodeAll(frame, nil)
		if _, err := bw.Write(comp); err != nil {
			return err
		}
		metas = append(metas, frameMetaW{comp: len(comp), rawLen: len(frame), firstKey: firstKey})
		frame = nil
		firstKey = nil
		return nil
	}
	for _, off := range offs {
		end := recEnd(off)
		if firstKey == nil {
			firstKey = append([]byte{}, recKey(off)...)
		}
		frame = append(frame, raw[off:end]...)
		if len(frame) >= leafFrameRaw {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}

	// Footer: frameCount, then per frame comp|raw|firstKey.
	var foot []byte
	foot = binary.AppendUvarint(foot, uint64(len(metas)))
	for _, m := range metas {
		foot = binary.AppendUvarint(foot, uint64(m.comp))
		foot = binary.AppendUvarint(foot, uint64(m.rawLen))
		foot = binary.AppendUvarint(foot, uint64(len(m.firstKey)))
		foot = append(foot, m.firstKey...)
	}
	if _, err := bw.Write(foot); err != nil {
		return err
	}
	var tail [16]byte
	binary.BigEndian.PutUint64(tail[:8], uint64(len(foot)))
	copy(tail[8:], leafSegMagic)
	if _, err := bw.Write(tail[:]); err != nil {
		return err
	}
	return bw.Flush()
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

// leafSegSet is the reader for one table's 256 bucket segments.
type leafSegSet struct {
	buckets [256]*leafSegFile
	table   int
	cache   *frameLRU
}

type decodedFrame struct {
	raw  []byte
	offs []uint32 // record starts
}

type frameLRU struct {
	m   map[uint32]*decodedFrame // key: table<<24 | bucket<<16 | frameIdx
	ord []uint32
}

func newFrameLRU() *frameLRU { return &frameLRU{m: make(map[uint32]*decodedFrame)} }

func (l *frameLRU) get(k uint32) *decodedFrame { return l.m[k] }
func (l *frameLRU) put(k uint32, d *decodedFrame) {
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
// build did not use --leaf-seg.
func openLeafSegSet(outDir string, table int, cache *frameLRU) (*leafSegSet, bool, error) {
	dir := filepath.Join(outDir, leafSegDir)
	if _, err := os.Stat(dir); err != nil {
		return nil, false, nil
	}
	s := &leafSegSet{table: table, cache: cache}
	any := false
	for b := 0; b < 256; b++ {
		name := filepath.Join(dir, fmt.Sprintf("%s.%02x.seg", leafTableName(table), b))
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sf, err := loadLeafSegFile(f)
		if err != nil {
			f.Close()
			return nil, false, fmt.Errorf("%s: %w", name, err)
		}
		s.buckets[b] = sf
		any = true
	}
	return s, any, nil
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

// Close releases the segment file handles.
func (s *leafSegSet) Close() {
	for b := range s.buckets {
		if s.buckets[b] != nil {
			_ = s.buckets[b].f.Close()
			s.buckets[b] = nil
		}
	}
}

func (s *leafSegSet) frameKey(bucket, fi int) uint32 {
	return uint32(s.table)<<24 | uint32(bucket)<<16 | uint32(fi)
}

func (s *leafSegSet) decodeFrame(bucket, fi int) (*decodedFrame, error) {
	ck := s.frameKey(bucket, fi)
	if d := s.cache.get(ck); d != nil {
		return d, nil
	}
	sf := s.buckets[bucket]
	fm := sf.frames[fi]
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
// semantics (the asOfLeaves contract).
type segLeafCursor struct {
	set    *leafSegSet
	bucket int
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

// position sets the cursor to (bucket, fi, ri) after decode.
func (c *segLeafCursor) position(bucket, fi, ri int) error {
	d, err := c.set.decodeFrame(bucket, fi)
	if err != nil {
		return err
	}
	c.bucket, c.fi, c.ri, c.cur, c.eof = bucket, fi, ri, d, false
	return nil
}

// Seek positions at the first entry >= k.
func (c *segLeafCursor) Seek(k []byte) ([]byte, []byte, error) {
	start := 0
	if len(k) > 0 {
		start = int(k[0])
	}
	for b := start; b < 256; b++ {
		sf := c.set.buckets[b]
		if sf == nil || len(sf.frames) == 0 {
			continue
		}
		var fi, ri int
		if b > start {
			fi, ri = 0, 0 // everything in a later bucket is > k
		} else {
			// Last frame whose firstKey <= k (or frame 0 if k precedes all).
			fi = sort.Search(len(sf.frames), func(i int) bool {
				return bytes.Compare(sf.frames[i].firstKey, k) > 0
			}) - 1
			if fi < 0 {
				fi = 0
			}
			d, err := c.set.decodeFrame(b, fi)
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
		if err := c.position(b, fi, ri); err != nil {
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
	sf := c.set.buckets[c.bucket]
	if c.fi+1 < len(sf.frames) {
		if err := c.position(c.bucket, c.fi+1, 0); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	for b := c.bucket + 1; b < 256; b++ {
		if bf := c.set.buckets[b]; bf != nil && len(bf.frames) > 0 {
			if err := c.position(b, 0, 0); err != nil {
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
		d, err := c.set.decodeFrame(c.bucket, c.fi-1)
		if err != nil {
			return nil, nil, err
		}
		if err := c.position(c.bucket, c.fi-1, len(d.offs)-1); err != nil {
			return nil, nil, err
		}
		return c.current()
	}
	for b := c.bucket - 1; b >= 0; b-- {
		if bf := c.set.buckets[b]; bf != nil && len(bf.frames) > 0 {
			fi := len(bf.frames) - 1
			d, err := c.set.decodeFrame(b, fi)
			if err != nil {
				return nil, nil, err
			}
			if err := c.position(b, fi, len(d.offs)-1); err != nil {
				return nil, nil, err
			}
			return c.current()
		}
	}
	c.eof = true
	return nil, nil, nil
}

func (c *segLeafCursor) Last() ([]byte, []byte, error) {
	for b := 255; b >= 0; b-- {
		if bf := c.set.buckets[b]; bf != nil && len(bf.frames) > 0 {
			fi := len(bf.frames) - 1
			d, err := c.set.decodeFrame(b, fi)
			if err != nil {
				return nil, nil, err
			}
			if err := c.position(b, fi, len(d.offs)-1); err != nil {
				return nil, nil, err
			}
			return c.current()
		}
	}
	c.eof = true
	return nil, nil, nil
}
