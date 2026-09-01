// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// header_frames.go — sub-segment framing for headerc, mirroring body_frames.go.
//
// headerc has the same shape as bodyc: 8192 blocks per segment, the whole
// segment compressed as one zstd frame, one segment cached at a time. So a
// random ReadHeader decompresses and decodes 8192 headers. The frame index and
// the skippable-magic discrimination are shared with bodyc (bodyFrameIndex,
// readFrameHeader), only the payload codec differs.
//
// One headerc-specific detail: Number is NOT stored columnar, it is derived as
// segmentStart + index. A frame therefore has to be told its own block offset
// so the numbers it fills in stay absolute — see readOneHeaderFrame.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
)

// headerFrameSize mirrors bodyFrameSize. Headers are far smaller than bodies,
// so the same block count yields a much smaller frame; that is fine — the cost
// being removed is per-block decode work, not bytes.
const headerFrameSize = bodyFrameSize

// encodeHeaderSegmentFramed encodes a header segment as independent frames.
// frameSize <= 0 or >= len(headers) produces the legacy single-frame layout.
func encodeHeaderSegmentFramed(headers []*block.Header, enc *zstd.Encoder, frameSize int) []byte {
	if frameSize <= 0 || frameSize >= len(headers) {
		return encodeHeaderSegment(headers, enc)
	}

	frameCount := (len(headers) + frameSize - 1) / frameSize
	payloads := make([][]byte, 0, frameCount)
	entries := make([]bodyFrameEntry, 0, frameCount)

	var off uint32
	for i := 0; i < len(headers); i += frameSize {
		end := i + frameSize
		if end > len(headers) {
			end = len(headers)
		}
		p := encodeHeaderSegment(headers[i:end], enc)
		entries = append(entries, bodyFrameEntry{
			compOffset: off,
			compLen:    uint32(len(p)),
			blockStart: uint16(i),
			blockCount: uint16(end - i),
		})
		off += uint32(len(p))
		payloads = append(payloads, p)
	}

	body := make([]byte, 0, 2+len(entries)*frameIndexEntrySize)
	var u16 [2]byte
	binary.LittleEndian.PutUint16(u16[:], uint16(len(entries)))
	body = append(body, u16[:]...)
	var e [frameIndexEntrySize]byte
	for _, en := range entries {
		binary.LittleEndian.PutUint32(e[0:4], en.compOffset)
		binary.LittleEndian.PutUint32(e[4:8], en.compLen)
		binary.LittleEndian.PutUint16(e[8:10], en.blockStart)
		binary.LittleEndian.PutUint16(e[10:12], en.blockCount)
		body = append(body, e[:]...)
	}

	out := make([]byte, 0, 8+len(body)+int(off))
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], zstdSkippableMagic)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(body)))
	out = append(out, hdr[:]...)
	out = append(out, body...)
	for _, p := range payloads {
		out = append(out, p...)
	}
	return out
}

// readOneHeaderFrame reads and decodes a single header frame off disk.
// baseBlock is the segment's first block number; the frame's own offset is
// added so Number stays absolute.
func readOneHeaderFrame(readAt func(p []byte, off int64) error, base int64, fi *bodyFrameIndex, frame int, dec *zstd.Decoder, baseBlock uint64) ([]*block.Header, error) {
	if frame < 0 || frame >= len(fi.entries) {
		return nil, fmt.Errorf("frame %d out of range (%d)", frame, len(fi.entries))
	}
	e := fi.entries[frame]
	buf := make([]byte, e.compLen)
	if err := readAt(buf, base+int64(fi.dataStart)+int64(e.compOffset)); err != nil {
		return nil, fmt.Errorf("read frame %d: %w", frame, err)
	}
	raw, err := dec.DecodeAll(buf, nil)
	if err != nil {
		return nil, fmt.Errorf("frame %d decompress: %w", frame, err)
	}
	headers, err := decodeHeaderSegment(raw)
	if err != nil {
		return nil, fmt.Errorf("frame %d decode: %w", frame, err)
	}
	// Number is derived, not stored — offset it by this frame's position.
	start := baseBlock + uint64(e.blockStart)
	for i, h := range headers {
		if h.Number == nil {
			h.Number = new(uint256.Int).SetUint64(start + uint64(i))
		}
	}
	return headers, nil
}

// ---------- Reader side ----------

// cachedHeaderFrame is one decoded header frame retained for random access.
type cachedHeaderFrame struct {
	seg     int64
	start   int // segment-relative index of headers[0]
	headers []*block.Header
}

// headerFrameCacheSize mirrors bodyFrameCacheSize. A single-frame cache was
// measured on the body side to be 5x SLOWER than the legacy whole-segment
// reader for in-segment random access — the frame was evicted and re-decoded
// on nearly every read. Headers are ~500 B each, so 32 frames of 256 costs a
// few MB.
const headerFrameCacheSize = bodyFrameCacheSize

// lookupHeaderFrame returns a cached header and promotes its frame.
func (r *HeaderCompactReader) lookupHeaderFrame(seg int64, idx int) (*block.Header, bool) {
	for i := range r.frameCache {
		c := &r.frameCache[i]
		if c.seg == seg && idx >= c.start && idx-c.start < len(c.headers) {
			h := c.headers[idx-c.start]
			if i > 0 {
				f := *c
				copy(r.frameCache[1:i+1], r.frameCache[0:i])
				r.frameCache[0] = f
			}
			return h, true
		}
	}
	return nil, false
}

// storeHeaderFrame inserts a frame at the front, evicting the least-recent.
func (r *HeaderCompactReader) storeHeaderFrame(f cachedHeaderFrame) {
	if len(r.frameCache) < headerFrameCacheSize {
		r.frameCache = append(r.frameCache, cachedHeaderFrame{})
	}
	copy(r.frameCache[1:], r.frameCache[:len(r.frameCache)-1])
	r.frameCache[0] = f
}

// readHeaderFramed serves one header from a framed segment, decoding only the
// frame that holds it. ok=false means the segment uses the legacy
// whole-segment layout and the caller should fall through unchanged.
func (r *HeaderCompactReader) readHeaderFramed(seg int64, idx int) (*block.Header, bool, error) {
	if uint64(seg) >= r.segments {
		return nil, false, fmt.Errorf("segment %d out of range (%d)", seg, r.segments)
	}
	if h, ok := r.lookupHeaderFrame(seg, idx); ok {
		return h, true, nil
	}

	var entryBuf [8]byte
	if _, err := r.idxFile.ReadAt(entryBuf[:], seg*8); err != nil {
		return nil, false, fmt.Errorf("read index: %w", err)
	}
	e := decodeHeaderIdx(entryBuf[:])
	df, err := r.dataFile(e.fileNum)
	if err != nil {
		return nil, false, err
	}
	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(e.offset)); err != nil {
		return nil, false, fmt.Errorf("read size: %w", err)
	}
	compSize := binary.LittleEndian.Uint32(sizeBuf[:])
	base := int64(e.offset) + 4
	readAt := func(p []byte, off int64) error {
		_, err := df.ReadAt(p, off)
		return err
	}
	fi, err := readFrameHeader(readAt, base, compSize)
	if err != nil {
		return nil, false, err
	}
	if fi == nil {
		return nil, false, nil // legacy layout
	}
	fr, ok := fi.frameFor(idx)
	if !ok {
		return nil, false, fmt.Errorf("no frame covers index %d", idx)
	}
	headers, err := readOneHeaderFrame(readAt, base, fi, fr, r.dec, uint64(seg)*HeaderSegmentSize)
	if err != nil {
		return nil, false, err
	}
	start := int(fi.entries[fr].blockStart)
	if idx-start >= len(headers) {
		return nil, false, fmt.Errorf("index %d outside decoded frame (%d headers at %d)",
			idx, len(headers), start)
	}
	r.storeHeaderFrame(cachedHeaderFrame{seg: seg, start: start, headers: headers})
	return headers[idx-start], true, nil
}
