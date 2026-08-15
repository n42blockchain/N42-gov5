// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ancientera

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"
	"lukechampine.com/blake3"
)

// Reader reads one sealed era file. Reads use pread (ReadAt) so a Reader
// is safe for concurrent use; decompressed-frame caching lives in the
// Store, not here.
type Reader struct {
	path   string
	file   *os.File
	size   int64
	Meta   *EraMeta
	frames []frameIndexEntry
	dec    *zstd.Decoder

	class Class
	start uint64 // first block
	span  uint64
}

// OpenReader opens an era file and performs the light check: footer magic
// and version, index/meta checksums, meta consistency. It does NOT hash
// the payload (see VerifyPayload for the full scrub).
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := &Reader{path: path, file: f}
	if err := r.init(); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

func (r *Reader) init() error {
	st, err := r.file.Stat()
	if err != nil {
		return err
	}
	r.size = st.Size()
	if r.size < int64(len(headMagic)+footerSize) {
		return errors.New("era: file too small")
	}
	var magic [8]byte
	if _, err := r.file.ReadAt(magic[:], 0); err != nil {
		return err
	}
	if string(magic[:]) != headMagic {
		return errors.New("era: bad head magic")
	}
	ftBuf := make([]byte, footerSize)
	if _, err := r.file.ReadAt(ftBuf, r.size-footerSize); err != nil {
		return err
	}
	ft, err := decodeFooter(ftBuf)
	if err != nil {
		return err
	}
	if ft.frameIndexOff >= uint64(r.size) || ft.metaOff >= uint64(r.size) || ft.frameIndexOff > ft.metaOff {
		return errors.New("era: footer offsets out of range")
	}
	idxBytes := make([]byte, ft.metaOff-ft.frameIndexOff)
	if _, err := r.file.ReadAt(idxBytes, int64(ft.frameIndexOff)); err != nil {
		return err
	}
	if xxhash.Sum64(idxBytes) != ft.indexXxh {
		return errors.New("era: frame index checksum mismatch")
	}
	r.frames, err = decodeFrameIndex(idxBytes)
	if err != nil {
		return err
	}
	// meta: u32 len | JSON
	var lenBuf [4]byte
	if _, err := r.file.ReadAt(lenBuf[:], int64(ft.metaOff)); err != nil {
		return err
	}
	metaLen := binary.BigEndian.Uint32(lenBuf[:])
	if metaLen > maxMetaSize || int64(ft.metaOff)+4+int64(metaLen) > r.size-footerSize {
		return errors.New("era: meta length out of range")
	}
	metaBytes := make([]byte, metaLen)
	if _, err := r.file.ReadAt(metaBytes, int64(ft.metaOff)+4); err != nil {
		return err
	}
	if xxhash.Sum64(metaBytes) != ft.metaXxh {
		return errors.New("era: meta checksum mismatch")
	}
	meta := new(EraMeta)
	if err := json.Unmarshal(metaBytes, meta); err != nil {
		return fmt.Errorf("era: meta parse: %w", err)
	}
	if meta.FormatVersion != FormatVersion {
		return fmt.Errorf("era: unsupported meta format version %d", meta.FormatVersion)
	}
	cl, err := ParseClass(meta.Class)
	if err != nil {
		return err
	}
	if meta.Span == 0 || meta.Span%FrameBlocks != 0 ||
		uint64(len(r.frames)) != meta.Span/FrameBlocks ||
		meta.EndBlock != meta.StartBlock+meta.Span || meta.StartBlock != meta.Era*meta.Span {
		return errors.New("era: meta/index geometry mismatch")
	}
	if want := "0x" + hex.EncodeToString(ft.payloadBlake3[:]); meta.PayloadBlake3 != want {
		return errors.New("era: meta payload hash disagrees with footer")
	}
	r.Meta, r.class, r.start, r.span = meta, cl, meta.StartBlock, meta.Span
	r.dec, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	return err
}

// Class returns the content class.
func (r *Reader) Class() Class { return r.class }

// Contains reports whether the era covers block num.
func (r *Reader) Contains(num uint64) bool {
	return num >= r.start && num < r.start+r.span
}

// frameForBlock returns the frame ordinal for a covered block.
func (r *Reader) frameForBlock(num uint64) int {
	return int((num - r.start) / FrameBlocks)
}

// ReadFrame reads, checksum-verifies and decompresses frame i.
func (r *Reader) ReadFrame(i int) ([]byte, error) {
	if i < 0 || i >= len(r.frames) {
		return nil, errors.New("era: frame out of range")
	}
	fe := r.frames[i]
	comp := make([]byte, fe.compLen)
	if _, err := r.file.ReadAt(comp, int64(fe.offset)); err != nil {
		return nil, err
	}
	if xxhash.Sum64(comp) != fe.xxh {
		return nil, fmt.Errorf("era: frame %d checksum mismatch (%s)", i, r.path)
	}
	raw, err := r.dec.DecodeAll(comp, make([]byte, 0, fe.rawLen))
	if err != nil {
		return nil, fmt.Errorf("era: frame %d decompress: %w", i, err)
	}
	if uint32(len(raw)) != fe.rawLen {
		return nil, fmt.Errorf("era: frame %d raw length mismatch", i)
	}
	return raw, nil
}

// entryFromFrame walks the decompressed frame to the block's record.
func (r *Reader) entryFromFrame(raw []byte, num uint64) (BlockEntry, error) {
	skip := int((num - r.start) % FrameBlocks)
	off := 0
	var err error
	for i := 0; i < skip; i++ {
		if off, err = skipBlockEntry(raw, off); err != nil {
			return nil, err
		}
	}
	e, _, err := decodeBlockEntry(raw, off)
	return e, err
}

// Block returns the record for a block, reading its frame uncached.
func (r *Reader) Block(num uint64) (BlockEntry, error) {
	if !r.Contains(num) {
		return nil, fmt.Errorf("era: block %d outside [%d,%d)", num, r.start, r.start+r.span)
	}
	raw, err := r.ReadFrame(r.frameForBlock(num))
	if err != nil {
		return nil, err
	}
	return r.entryFromFrame(raw, num)
}

// VerifyPayload re-hashes the entire frame region and compares against
// the footer/meta payload hash, and checks every frame checksum. This is
// the scrub path (weekly gate / n42-ancient verify).
func (r *Reader) VerifyPayload() error {
	h := blake3.New(32, nil)
	for i, fe := range r.frames {
		comp := make([]byte, fe.compLen)
		if _, err := r.file.ReadAt(comp, int64(fe.offset)); err != nil {
			return fmt.Errorf("era: frame %d read: %w", i, err)
		}
		if xxhash.Sum64(comp) != fe.xxh {
			return fmt.Errorf("era: frame %d checksum mismatch", i)
		}
		h.Write(comp)
	}
	var got [32]byte
	h.Sum(got[:0])
	if want := "0x" + hex.EncodeToString(got[:]); want != r.Meta.PayloadBlake3 {
		return errors.New("era: payload blake3 mismatch")
	}
	return nil
}

// Close releases the file handle.
func (r *Reader) Close() error {
	if r.dec != nil {
		r.dec.Close()
	}
	return r.file.Close()
}
