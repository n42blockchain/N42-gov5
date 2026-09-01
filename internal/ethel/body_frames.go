// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// body_frames.go — sub-segment framing for bodyc.
//
// A segment is 8192 blocks and, until this file existed, also exactly one zstd
// frame: reading a single block decompressed and decoded all 8192 (measured
// 2.8-4.0 s cold, and the Amdahl serial section behind the 2026-08-24
// witness-replay profile where 128 cores sat 99.4% idle).
//
// Framing splits the segment into independently decompressible frames of
// bodyFrameSize blocks while keeping the segment, the .cidx and every segment
// number arithmetic untouched. Measured on real segment 3051 at F=256:
// +1.73% bytes for a 35.8x cut in random-read latency (1.635 s -> 45.7 ms),
// with no trained dictionary needed.
//
// Format discrimination is by zstd magic, so old and new segments coexist with
// no version field and no ambiguity:
//
//	legacy : [zstd frame]                      first 4 bytes = 0xFD2FB528
//	framed : [skippable frame: index][zstd]... first 4 bytes = 0x184D2A50
//
// The skippable frame is a standard zstd container that any zstd decoder
// ignores, so the payload stays a valid zstd stream.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/klauspost/compress/zstd"
)

const (
	// bodyFrameSize is how many blocks share one independently decompressible
	// frame. 256 was chosen by measurement (see the file comment): smaller
	// frames buy latency almost linearly but cost ratio, and going below ~128
	// starts paying more in size than it returns in speed for this data.
	bodyFrameSize = 256

	// bodyFrameCacheSize is how many decoded frames a reader retains for random
	// access. 32 frames == one whole segment at bodyFrameSize=256, so the worst
	// case matches what whole-segment caching used to hold, while a miss still
	// costs one frame instead of 8192 blocks.
	bodyFrameCacheSize = 32

	// zstdSkippableMagic is the first of the eight skippable-frame magics
	// (0x184D2A50..0x184D2A5F). Decoders skip these, so a legacy reader that
	// DecodeAll's the whole payload still sees only real frames.
	zstdSkippableMagic = 0x184D2A50
	zstdFrameMagic     = 0xFD2FB528

	// frameIndexEntrySize is (compOffset u32, compLen u32, blockStart u16,
	// blockCount u16).
	frameIndexEntrySize = 12
)

// bodyFrameIndex describes where each frame lives inside a framed payload.
type bodyFrameIndex struct {
	entries []bodyFrameEntry
	// dataStart is the offset of the first real zstd frame, i.e. just past the
	// skippable container.
	dataStart int
}

type bodyFrameEntry struct {
	compOffset uint32 // relative to dataStart
	compLen    uint32
	blockStart uint16 // first block index within the segment
	blockCount uint16
}

// isFramedPayload reports whether a segment payload uses the framed layout.
func isFramedPayload(p []byte) bool {
	if len(p) < 4 {
		return false
	}
	return binary.LittleEndian.Uint32(p[:4]) == zstdSkippableMagic
}

// encodeBodySegmentFramed encodes one segment as independent frames of
// frameSize blocks each. frameSize <= 0 or >= len(blocks) falls back to the
// legacy single-frame layout, so callers can disable framing without a branch.
func encodeBodySegmentFramed(blocks []*DecodedBlock, chainID uint64, enc *zstd.Encoder, frameSize int) []byte {
	if frameSize <= 0 || frameSize >= len(blocks) {
		return encodeBodySegment(blocks, chainID, enc)
	}

	frameCount := (len(blocks) + frameSize - 1) / frameSize
	payloads := make([][]byte, 0, frameCount)
	entries := make([]bodyFrameEntry, 0, frameCount)

	var off uint32
	for i := 0; i < len(blocks); i += frameSize {
		end := i + frameSize
		if end > len(blocks) {
			end = len(blocks)
		}
		p := encodeBodySegment(blocks[i:end], chainID, enc)
		entries = append(entries, bodyFrameEntry{
			compOffset: off,
			compLen:    uint32(len(p)),
			blockStart: uint16(i),
			blockCount: uint16(end - i),
		})
		off += uint32(len(p))
		payloads = append(payloads, p)
	}

	// Skippable frame: [magic u32][size u32][frameCount u16][entries...]
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

// parseBodyFrameIndex reads the skippable container at the head of a framed
// payload.
func parseBodyFrameIndex(p []byte) (*bodyFrameIndex, error) {
	if len(p) < 8 {
		return nil, fmt.Errorf("framed payload too short: %d", len(p))
	}
	if binary.LittleEndian.Uint32(p[:4]) != zstdSkippableMagic {
		return nil, fmt.Errorf("not a framed payload")
	}
	bodyLen := int(binary.LittleEndian.Uint32(p[4:8]))
	if 8+bodyLen > len(p) || bodyLen < 2 {
		return nil, fmt.Errorf("frame index truncated: bodyLen=%d payload=%d", bodyLen, len(p))
	}
	body := p[8 : 8+bodyLen]
	n := int(binary.LittleEndian.Uint16(body[:2]))
	if 2+n*frameIndexEntrySize > len(body) {
		return nil, fmt.Errorf("frame index says %d frames but body is %d bytes", n, len(body))
	}
	idx := &bodyFrameIndex{entries: make([]bodyFrameEntry, n), dataStart: 8 + bodyLen}
	for i := 0; i < n; i++ {
		o := 2 + i*frameIndexEntrySize
		idx.entries[i] = bodyFrameEntry{
			compOffset: binary.LittleEndian.Uint32(body[o : o+4]),
			compLen:    binary.LittleEndian.Uint32(body[o+4 : o+8]),
			blockStart: binary.LittleEndian.Uint16(body[o+8 : o+10]),
			blockCount: binary.LittleEndian.Uint16(body[o+10 : o+12]),
		}
	}
	return idx, nil
}

// frameFor returns the index of the frame holding segment-relative block idx.
func (fi *bodyFrameIndex) frameFor(idx int) (int, bool) {
	for i, e := range fi.entries {
		if idx >= int(e.blockStart) && idx < int(e.blockStart)+int(e.blockCount) {
			return i, true
		}
	}
	return 0, false
}

// decodeOneFrame decompresses and decodes a single frame of a framed payload.
func decodeOneFrame(p []byte, fi *bodyFrameIndex, frame int, dec *zstd.Decoder) ([]*DecodedBlock, bodyFlags, error) {
	if frame < 0 || frame >= len(fi.entries) {
		return nil, 0, fmt.Errorf("frame %d out of range (%d)", frame, len(fi.entries))
	}
	e := fi.entries[frame]
	start := fi.dataStart + int(e.compOffset)
	end := start + int(e.compLen)
	if end > len(p) {
		return nil, 0, fmt.Errorf("frame %d spans past payload (%d > %d)", frame, end, len(p))
	}
	raw, err := dec.DecodeAll(p[start:end], nil)
	if err != nil {
		return nil, 0, fmt.Errorf("frame %d decompress: %w", frame, err)
	}
	blocks, err := decodeBodySegment(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("frame %d decode: %w", frame, err)
	}
	return blocks, bodySegmentFlags(raw), nil
}

// decodeAllFrames decodes every frame in order — the whole-segment equivalent,
// used by sequential consumers and by the compatibility path.
func decodeAllFrames(p []byte, fi *bodyFrameIndex, dec *zstd.Decoder) ([]*DecodedBlock, bodyFlags, error) {
	var out []*DecodedBlock
	var flags bodyFlags
	for i := range fi.entries {
		blocks, f, err := decodeOneFrame(p, fi, i, dec)
		if err != nil {
			return nil, 0, err
		}
		if i == 0 {
			flags = f
		}
		out = append(out, blocks...)
	}
	return out, flags, nil
}

// readFrameHeader reads just enough of a payload to learn whether it is framed
// and, if so, its frame index — without pulling the whole segment off disk.
// Returns nil index for a legacy payload.
func readFrameHeader(readAt func(p []byte, off int64) error, base int64, compSize uint32) (*bodyFrameIndex, error) {
	if compSize < 8 {
		return nil, nil
	}
	var hdr [8]byte
	if err := readAt(hdr[:], base); err != nil {
		return nil, err
	}
	if binary.LittleEndian.Uint32(hdr[:4]) != zstdSkippableMagic {
		return nil, nil // legacy single-frame payload
	}
	bodyLen := binary.LittleEndian.Uint32(hdr[4:8])
	if 8+bodyLen > compSize || bodyLen < 2 {
		return nil, fmt.Errorf("frame index truncated: bodyLen=%d compSize=%d", bodyLen, compSize)
	}
	body := make([]byte, bodyLen)
	if err := readAt(body, base+8); err != nil {
		return nil, err
	}
	n := int(binary.LittleEndian.Uint16(body[:2]))
	if 2+n*frameIndexEntrySize > len(body) {
		return nil, fmt.Errorf("frame index says %d frames, body %d bytes", n, len(body))
	}
	idx := &bodyFrameIndex{entries: make([]bodyFrameEntry, n), dataStart: 8 + int(bodyLen)}
	for i := 0; i < n; i++ {
		o := 2 + i*frameIndexEntrySize
		idx.entries[i] = bodyFrameEntry{
			compOffset: binary.LittleEndian.Uint32(body[o : o+4]),
			compLen:    binary.LittleEndian.Uint32(body[o+4 : o+8]),
			blockStart: binary.LittleEndian.Uint16(body[o+8 : o+10]),
			blockCount: binary.LittleEndian.Uint16(body[o+10 : o+12]),
		}
	}
	return idx, nil
}

// readOneFrame pulls exactly one frame's compressed bytes off disk and decodes
// it. This is what makes a random block read cost one frame instead of the
// whole segment.
func readOneFrame(readAt func(p []byte, off int64) error, base int64, fi *bodyFrameIndex, frame int, dec *zstd.Decoder) ([]*DecodedBlock, bodyFlags, error) {
	if frame < 0 || frame >= len(fi.entries) {
		return nil, 0, fmt.Errorf("frame %d out of range (%d)", frame, len(fi.entries))
	}
	e := fi.entries[frame]
	buf := make([]byte, e.compLen)
	if err := readAt(buf, base+int64(fi.dataStart)+int64(e.compOffset)); err != nil {
		return nil, 0, fmt.Errorf("read frame %d: %w", frame, err)
	}
	raw, err := dec.DecodeAll(buf, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("frame %d decompress: %w", frame, err)
	}
	blocks, err := decodeBodySegment(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("frame %d decode: %w", frame, err)
	}
	return blocks, bodySegmentFlags(raw), nil
}

// FrameSize reports the blocks-per-frame this stage will emit (0 = legacy
// whole-segment). Exposed so a generation run can LOG the layout it produced:
// a framed set and a legacy set have identical filenames, so without this the
// only way to tell them apart afterwards is to open one and look for the
// skippable-frame magic.
func (s *BodyCompactStage) FrameSize() int { return s.frameSize }
