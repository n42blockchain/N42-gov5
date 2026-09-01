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
