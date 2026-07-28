// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// codes_freezer_reader.go — read-only access to the codes.cidx +
// codes.NNNN.cdat layout produced by code-import2fz. The freezer is
// address-indexed (binary search by 20B address) with each bytecode
// individually zstd-compressed. Use as a self-contained code source
// for witness-replay so the worker doesn't need an MDBX with the
// (often-incomplete) Code table populated.

package ethel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// maxCodesFiles caps the number of cdat segments. Each entry is a
// fileNum (uint16, 0..65535) but in practice the writer rotates at
// 2 GB per file so a 24M-block mainnet snapshot fits in <10 files.
// Sized to 256 to leave headroom without blowing up the array.
const maxCodesFiles = 256

// codesCidxHeaderSize is the on-disk header size shared with the writer
// in cmd/code-import2fz. Magic + per-entry width come from the freezer
// package so reader and writer can't drift.
const codesCidxHeaderSize = 16

type codesEntry struct {
	addr    [20]byte
	fileNum uint16
	offset  uint32
}

// CodesFreezerReader reads bytecode by address from a codes.cidx +
// codes.NNNN.cdat layout.
//
// Hot-path lookups are lock-free: file handles and sizes live in
// fixed-size atomic.Pointer / atomic.Int64 arrays indexed by fileNum
// (writer rotates at 2 GB so 24M-block mainnet fits in <10 files).
// openMu serialises the rare cold-open path. zstd.Decoder.DecodeAll
// is goroutine-safe.
type CodesFreezerReader struct {
	dir     string
	entries []codesEntry // sorted by addr ascending
	files   [maxCodesFiles]atomic.Pointer[os.File]
	sizes   [maxCodesFiles]atomic.Int64
	openMu  sync.Mutex // guards the cold-open path only
	zstdDec *zstd.Decoder
	hashIdx *codesHashIndex // optional content-addressed index; nil when absent
}

// NewCodesFreezerReader opens the codes.cidx file in dir and loads its
// entire address index into memory (one entry = 26 B; 1M contracts ≈
// 26 MB resident). Bytecode files codes.NNNN.cdat are opened lazily on
// first read.
func NewCodesFreezerReader(dir string) (*CodesFreezerReader, error) {
	cidxPath := filepath.Join(dir, "codes.cidx")
	f, err := os.Open(cidxPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", cidxPath, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < codesCidxHeaderSize {
		return nil, fmt.Errorf("codes.cidx too small: %d bytes", info.Size())
	}
	var header [codesCidxHeaderSize]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if [4]byte{header[0], header[1], header[2], header[3]} != freezer.CidxMagic {
		return nil, fmt.Errorf("codes.cidx: bad magic %q (expected %q)", string(header[:4]), string(freezer.CidxMagic[:]))
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read entries: %w", err)
	}
	if len(body)%freezer.CidxAddrEntrySize != 0 {
		return nil, fmt.Errorf("codes.cidx: body size %d not multiple of %d", len(body), freezer.CidxAddrEntrySize)
	}
	n := len(body) / freezer.CidxAddrEntrySize
	entries := make([]codesEntry, n)
	for i := 0; i < n; i++ {
		off := i * freezer.CidxAddrEntrySize
		copy(entries[i].addr[:], body[off:off+20])
		entries[i].fileNum = binary.BigEndian.Uint16(body[off+20 : off+22])
		entries[i].offset = binary.BigEndian.Uint32(body[off+22 : off+26])
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("zstd reader: %w", err)
	}
	// Optional content-addressed index. Absent files are not an error: the
	// reader then behaves exactly as before this index existed.
	hashIdx, err := openCodesHashIndex(dir)
	if err != nil {
		dec.Close()
		return nil, err
	}
	return &CodesFreezerReader{
		dir:     dir,
		entries: entries,
		zstdDec: dec,
		hashIdx: hashIdx,
	}, nil
}

// Coverage returns the highest block whose codes this store is guaranteed to
// contain (and whether a coverage record exists). Bytecode is content-addressed,
// so individual codes carry no height; this single sidecar value (codes.coverage)
// is the store's boundary. A consumer replaying block N should require
// covered >= N before trusting the store. See freezer.CodesCoverageFile.
func (r *CodesFreezerReader) Coverage() (uint64, bool) {
	return freezer.ReadCodesCoverage(r.dir)
}

// ContractCount reports the number of indexed contract addresses (diagnostics).
func (r *CodesFreezerReader) ContractCount() int { return len(r.entries) }

// Close releases all open data files and the zstd decoder.
func (r *CodesFreezerReader) Close() {
	for i := range r.files {
		if f := r.files[i].Swap(nil); f != nil {
			f.Close()
		}
	}
	if r.zstdDec != nil {
		r.zstdDec.Close()
		r.zstdDec = nil
	}
	r.hashIdx.close()
	r.hashIdx = nil
}

// Items returns the number of contracts in the index (for diagnostics).
func (r *CodesFreezerReader) Items() int { return len(r.entries) }

// GetCode is the modules/state.CodeSource adapter: same lookup as
// LookupByAddress but with the interface-mandated method name. Lets
// PlainStateReader.SetCodeSource accept *CodesFreezerReader directly
// without an external wrapper.
func (r *CodesFreezerReader) GetCode(addr types.Address) ([]byte, error) {
	return r.LookupByAddress(addr)
}

// GetCodeByHash implements modules/state.CodeByHashSource: the
// content-addressed path, served from the optional codes.hidx MPHF. Returns
// (nil, nil) when the hash index is absent or the hash is not in it, so the
// caller falls back to the address index and then to MDBX.
//
// A hash outside the build set can land on another entry's slot rather than
// missing cleanly — the MPHF stores no keys. That is safe here only because
// every caller verifies keccak(code) == codeHash; do not use this as a
// membership test.
func (r *CodesFreezerReader) GetCodeByHash(codeHash types.Hash) ([]byte, error) {
	compressed, err := r.LookupCompressedByHash(codeHash)
	if err != nil || compressed == nil {
		return nil, err
	}
	decoded, err := r.zstdDec.DecodeAll(compressed, nil)
	if err != nil {
		// A wrong slot yields bytes that are not a valid zstd frame. That is
		// an expected outcome for an out-of-set hash, not a corruption
		// signal, so report a miss and let the caller fall through.
		return nil, nil
	}
	return decoded, nil
}

// LookupCompressedByHash is GetCodeByHash without the decompression: the raw
// single-frame zstd blob, for servers that ship compressed code over the wire.
// Returns (nil, nil) on a miss.
func (r *CodesFreezerReader) LookupCompressedByHash(codeHash types.Hash) ([]byte, error) {
	fileNum, offset, length, ok := r.hashIdx.lookup(codeHash)
	if !ok || length == 0 {
		return nil, nil
	}
	f, err := r.openFile(fileNum)
	if err != nil {
		return nil, err
	}
	compressed := make([]byte, length)
	if _, err := f.ReadAt(compressed, int64(offset)); err != nil {
		return nil, fmt.Errorf("codes-freezer: read cdat %d at %d (hash %x): %w",
			fileNum, offset, codeHash[:6], err)
	}
	return compressed, nil
}

// HasHashIndex reports whether the content-addressed index is present.
func (r *CodesFreezerReader) HasHashIndex() bool { return r.hashIdx != nil }

// LookupByAddress returns the bytecode for addr, or nil if not present.
func (r *CodesFreezerReader) LookupByAddress(addr types.Address) ([]byte, error) {
	compressed, err := r.LookupCompressedByAddress(addr)
	if err != nil || compressed == nil {
		return nil, err
	}
	// Decompress. zstd.Decoder.DecodeAll is goroutine-safe.
	decoded, err := r.zstdDec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("codes-freezer: decompress addr %x: %w", addr[:], err)
	}
	return decoded, nil
}

// LookupCompressedByAddress returns the RAW zstd-framed blob for addr (one
// independent zstd frame), skipping decompression — so a server can ship the
// compressed code over the wire directly (the client decompresses), saving
// ~55% bandwidth and the decompress+recompress round-trip. Returns nil if absent.
func (r *CodesFreezerReader) LookupCompressedByAddress(addr types.Address) ([]byte, error) {
	idx := sort.Search(len(r.entries), func(i int) bool {
		return bytes.Compare(r.entries[i].addr[:], addr[:]) >= 0
	})
	if idx >= len(r.entries) {
		return nil, nil
	}
	e := r.entries[idx]
	if e.addr != addr {
		return nil, nil
	}
	var endOffset int64
	if idx+1 < len(r.entries) && r.entries[idx+1].fileNum == e.fileNum {
		endOffset = int64(r.entries[idx+1].offset)
	} else {
		sz, err := r.fileSize(e.fileNum)
		if err != nil {
			return nil, err
		}
		endOffset = sz
	}
	size := endOffset - int64(e.offset)
	if size <= 0 {
		return nil, fmt.Errorf("codes-freezer: bad size %d for addr %x (file=%d offset=%d end=%d)",
			size, addr[:], e.fileNum, e.offset, endOffset)
	}
	f, err := r.openFile(e.fileNum)
	if err != nil {
		return nil, err
	}
	compressed := make([]byte, size)
	if _, err := f.ReadAt(compressed, int64(e.offset)); err != nil {
		return nil, fmt.Errorf("codes-freezer: read cdat %d at %d: %w", e.fileNum, e.offset, err)
	}
	return compressed, nil
}

func (r *CodesFreezerReader) openFile(num uint16) (*os.File, error) {
	if int(num) >= maxCodesFiles {
		return nil, fmt.Errorf("codes-freezer: fileNum %d exceeds maxCodesFiles=%d", num, maxCodesFiles)
	}
	if f := r.files[num].Load(); f != nil {
		return f, nil
	}
	r.openMu.Lock()
	defer r.openMu.Unlock()
	if f := r.files[num].Load(); f != nil {
		return f, nil
	}
	name := filepath.Join(r.dir, fmt.Sprintf("codes.%04d.cdat", num))
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	r.files[num].Store(f)
	return f, nil
}

func (r *CodesFreezerReader) fileSize(num uint16) (int64, error) {
	if int(num) >= maxCodesFiles {
		return 0, fmt.Errorf("codes-freezer: fileNum %d exceeds maxCodesFiles=%d", num, maxCodesFiles)
	}
	if sz := r.sizes[num].Load(); sz > 0 {
		return sz, nil
	}
	name := filepath.Join(r.dir, fmt.Sprintf("codes.%04d.cdat", num))
	info, err := os.Stat(name)
	if err != nil {
		return 0, err
	}
	r.sizes[num].Store(info.Size())
	return info.Size(), nil
}
