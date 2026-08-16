// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ancientera

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"
	"lukechampine.com/blake3"
)

// Writer seals one era file. Usage:
//
//	w, _ := NewWriter(dir, class, era, span, chainID, genesisHash, creator)
//	for each block in [era*span, (era+1)*span): w.AddBlock(entry, canonicalHash)
//	path, meta, _ := w.Finalize(parentHash)
//
// The file is built as <name>.tmp and atomically renamed on Finalize.
// AddBlock must be called exactly span times in ascending block order.
type Writer struct {
	dir         string
	class       Class
	era         uint64
	span        uint64
	chainID     uint64
	genesisHash string
	creator     string

	tmpPath string
	file    *os.File
	buf     *bufio.Writer
	written uint64 // bytes emitted into the frame region (after magic)

	enc         *zstd.Encoder
	payloadHash *blake3.Hasher

	pending    []byte // raw (uncompressed) current frame
	blockCount uint64
	frames     []frameIndexEntry

	firstHash string
	lastHash  string
	accHash   *blake3.Hasher // class A accumulator over canonical hashes
}

// NewWriter creates a writer for era file <class>-<era>-<hash8>.era in dir.
// The final name needs the first canonical hash, so the temp file uses a
// hash-free name until Finalize.
func NewWriter(dir string, class Class, era, span, chainID uint64, genesisHash types32, creator string) (*Writer, error) {
	if span == 0 || span%FrameBlocks != 0 {
		return nil, fmt.Errorf("era: span %d not a multiple of %d", span, FrameBlocks)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp := filepath.Join(dir, fmt.Sprintf("%s-%08d%s.tmp", class, era, FileExt))
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBetterCompression),
		zstd.WithEncoderConcurrency(1))
	if err != nil {
		f.Close()
		return nil, err
	}
	w := &Writer{
		dir: dir, class: class, era: era, span: span,
		chainID: chainID, genesisHash: hexStr(genesisHash), creator: creator,
		tmpPath: tmp, file: f, buf: bufio.NewWriterSize(f, 1<<20),
		enc: enc, payloadHash: blake3.New(32, nil),
	}
	if class == ClassChain {
		w.accHash = blake3.New(32, nil)
	}
	if _, err := w.buf.WriteString(headMagic); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// types32 is any 32-byte hash value (avoids importing common/types here;
// keeps the package dependency-light for tools).
type types32 = [32]byte

func hexStr(h [32]byte) string { return "0x" + hex.EncodeToString(h[:]) }

// AddBlock appends the record for the next block. canonicalHash is
// required for every class (it names the file and chains eras); only
// class chain embeds it as an entry — callers for exec/aux pass it for
// meta/accumulator purposes only.
func (w *Writer) AddBlock(entry BlockEntry, canonicalHash [32]byte) error {
	if w.blockCount >= w.span {
		return errors.New("era: AddBlock past span")
	}
	if w.blockCount == 0 {
		w.firstHash = hexStr(canonicalHash)
	}
	w.lastHash = hexStr(canonicalHash)
	if w.accHash != nil {
		w.accHash.Write(canonicalHash[:])
	}
	w.pending = entry.EncodeInto(w.pending)
	w.blockCount++
	if w.blockCount%FrameBlocks == 0 {
		return w.flushFrame()
	}
	return nil
}

func (w *Writer) flushFrame() error {
	comp := w.enc.EncodeAll(w.pending, nil)
	fe := frameIndexEntry{
		offset:  uint64(len(headMagic)) + w.written,
		compLen: uint32(len(comp)),
		rawLen:  uint32(len(w.pending)),
		xxh:     xxhash.Sum64(comp),
	}
	if _, err := w.buf.Write(comp); err != nil {
		return err
	}
	w.payloadHash.Write(comp)
	w.written += uint64(len(comp))
	w.frames = append(w.frames, fe)
	w.pending = w.pending[:0]
	return nil
}

// Finalize writes index/meta/footer, fsyncs and renames into place.
// Returns the final path and the meta it embedded.
func (w *Writer) Finalize(parentHash [32]byte) (string, *EraMeta, error) {
	if w.blockCount != w.span {
		return "", nil, fmt.Errorf("era: sealed %d of %d blocks", w.blockCount, w.span)
	}
	idxBytes := encodeFrameIndex(w.frames)
	var payload [32]byte
	w.payloadHash.Sum(payload[:0])

	meta := &EraMeta{
		FormatVersion: FormatVersion,
		Class:         w.class.String(),
		Era:           w.era,
		Span:          w.span,
		ChainID:       w.chainID,
		GenesisHash:   w.genesisHash,
		StartBlock:    w.era * w.span,
		EndBlock:      (w.era + 1) * w.span,
		FirstHash:     w.firstHash,
		LastHash:      w.lastHash,
		ParentHash:    hexStr(parentHash),
		Codecs:        PinnedCodecs,
		PayloadBlake3: "0x" + hex.EncodeToString(payload[:]),
		Creator:       w.creator,
	}
	if w.accHash != nil {
		var acc [32]byte
		w.accHash.Sum(acc[:0])
		meta.Accumulator = "0x" + hex.EncodeToString(acc[:])
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return "", nil, err
	}

	frameIndexOff := uint64(len(headMagic)) + w.written
	metaOff := frameIndexOff + uint64(len(idxBytes))
	var lenBuf [4]byte
	putU32(lenBuf[:], uint32(len(metaBytes)))

	ft := &footer{
		frameIndexOff: frameIndexOff,
		metaOff:       metaOff,
		payloadBlake3: payload,
		indexXxh:      xxhash.Sum64(idxBytes),
		metaXxh:       xxhash.Sum64(metaBytes),
		version:       FormatVersion,
	}
	for _, chunk := range [][]byte{idxBytes, lenBuf[:], metaBytes, encodeFooter(ft)} {
		if _, err := w.buf.Write(chunk); err != nil {
			return "", nil, err
		}
	}
	if err := w.buf.Flush(); err != nil {
		return "", nil, err
	}
	if err := w.file.Sync(); err != nil {
		return "", nil, err
	}
	if err := w.file.Close(); err != nil {
		return "", nil, err
	}
	final := filepath.Join(w.dir, FileName(w.class, w.era, meta.FirstHash[2:10]))
	if err := os.Rename(w.tmpPath, final); err != nil {
		return "", nil, err
	}
	return final, meta, nil
}

// Abort removes the temp file.
func (w *Writer) Abort() {
	if w.file != nil {
		w.file.Close()
	}
	os.Remove(w.tmpPath)
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
