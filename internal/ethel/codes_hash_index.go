// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// codes_hash_index.go — content-addressed lookup for the codes freezer.
//
// Bytecode is identified by keccak(code), which is what an account stores and
// what every caller verifies after the read. The address index this sits
// beside exists only because the reader used to ask by address, and paying for
// it is expensive on the writing side: the exporter must join reth's Bytecodes
// table against the entire PlainAccountState (405M accounts, ~85M of them with
// code) to learn which addresses reference which hash — an in-memory
// codeHash→[]address map that costs tens of GB to answer a question the caller
// never actually had.
//
// Keying by hash removes the join, and it also removes the keys from the index.
// A minimal perfect hash maps the N hashes in the build set to the N slots
// [0,N) with no collisions, but returns an arbitrary slot for anything outside
// that set. Normally you would store the keys to tell the two apart; here the
// caller's keccak(code) == codeHash check already does, so the keys can be
// dropped: ~1.8 bits per key instead of 32 bytes.
//
// Layout, both optional and additive — absent files simply mean "no hash
// lookup", and CodesFreezerReader falls back to the address path:
//
//	codes.hidx — RecSplit MPHF over the 32B code hashes (NoValues: Lookup→slot)
//	codes.hoff — slot-ordered [fileNum:2 LE][offset:4 LE][length:4 LE], 10 B per
//	             slot, pointing into the same codes.NNNN.cdat blobs the address
//	             index uses, so the two indexes share one payload.
//
// The length is stored rather than derived from the next entry (which is how
// the address index sizes a blob) so this path does not depend on the address
// index existing at all — the point of the exercise is to be able to stop
// emitting that index, not to add a second one beside it.

package ethel

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/mmap"
	"github.com/n42blockchain/N42/lib/recsplit"
)

const (
	// CodesHashIndexFile is the MPHF over code hashes.
	CodesHashIndexFile = "codes.hidx"
	// CodesHashOffsetsFile is the slot→(fileNum, offset) table.
	CodesHashOffsetsFile = "codes.hoff"
	// codesHoffEntrySize is [fileNum:2][offset:4][length:4].
	codesHoffEntrySize = 10
)

// codesHashIndex resolves codeHash → (fileNum, offset) with no stored keys.
type codesHashIndex struct {
	idx    *recsplit.Index
	reader *recsplit.IndexReader
	offs   []byte // slot-ordered, codesHoffEntrySize per slot
	slots  uint64

	// mmap keep-alives for offs; nil when it fell back to a heap read.
	offsF  *os.File
	offsM2 *[mmap.MaxMapSize]byte
}

// openCodesHashIndex loads the optional hash index from dir. Returns
// (nil, nil) when the files are absent — the caller then has only the
// address path, which is the pre-existing behaviour.
func openCodesHashIndex(dir string) (*codesHashIndex, error) {
	idxPath := filepath.Join(dir, CodesHashIndexFile)
	offPath := filepath.Join(dir, CodesHashOffsetsFile)
	if _, err := os.Stat(idxPath); err != nil {
		return nil, nil
	}
	// Map rather than read: at 10 B per code this is ~25 MB for mainnet, and a
	// heap copy is private, pagefile-backed memory the OS cannot reclaim. The
	// snapshot reader maps its .ef/.val for exactly this reason (a heap read
	// there is what drove a 37 GB minimal client). Same escape hatch:
	// N42_SNAP_MMAP=0 forces the heap path.
	offs, offsF, offsM2, err := mapOrReadFile(offPath)
	if err != nil {
		// The MPHF alone cannot answer anything: without offsets a slot is
		// just a number. Treat a half-written pair as absent rather than
		// failing the whole reader.
		return nil, nil
	}
	if len(offs)%codesHoffEntrySize != 0 {
		closeFileMapping(offs, offsF, offsM2)
		return nil, fmt.Errorf("codes-freezer: %s size %d is not a multiple of %d",
			CodesHashOffsetsFile, len(offs), codesHoffEntrySize)
	}
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		closeFileMapping(offs, offsF, offsM2)
		return nil, fmt.Errorf("codes-freezer: open %s: %w", CodesHashIndexFile, err)
	}
	return &codesHashIndex{
		idx:    idx,
		reader: recsplit.NewIndexReader(idx),
		offs:   offs,
		slots:  uint64(len(offs) / codesHoffEntrySize),
		offsF:  offsF,
		offsM2: offsM2,
	}, nil
}

// mapOrReadFile returns path's bytes as a read-only mmap with its keep-alive
// handles, falling back to a heap read when mapping is disabled or fails.
// Mirrors snapshotreader.mapOrRead, which is unexported there.
func mapOrReadFile(path string) (data []byte, f *os.File, m2 *[mmap.MaxMapSize]byte, err error) {
	if os.Getenv("N42_SNAP_MMAP") != "0" {
		if f, err = os.Open(path); err == nil {
			if st, serr := f.Stat(); serr == nil && st.Size() > 0 {
				if h1, h2, merr := mmap.Mmap(f, int(st.Size())); merr == nil {
					return h1[:st.Size()], f, h2, nil
				}
			}
			f.Close()
		}
	}
	data, err = os.ReadFile(path)
	return data, nil, nil, err
}

// closeFileMapping releases one mapOrReadFile result (no-op for heap buffers).
func closeFileMapping(data []byte, f *os.File, m2 *[mmap.MaxMapSize]byte) {
	if m2 != nil {
		_ = mmap.Munmap(data, m2)
	}
	if f != nil {
		f.Close()
	}
}

// lookup returns the cdat location and blob length for codeHash. ok is false when the hash is
// outside the build set — but note that an out-of-set hash can also produce a
// wrong (fileNum, offset) here rather than ok=false, which is exactly what the
// caller's keccak verification is for.
func (h *codesHashIndex) lookup(codeHash types.Hash) (fileNum uint16, offset, length uint32, ok bool) {
	if h == nil || h.reader == nil {
		return 0, 0, 0, false
	}
	slot, found := h.reader.Lookup(codeHash[:])
	if !found || slot >= h.slots {
		return 0, 0, 0, false
	}
	rec := h.offs[slot*codesHoffEntrySize:]
	return binary.LittleEndian.Uint16(rec[0:2]),
		binary.LittleEndian.Uint32(rec[2:6]),
		binary.LittleEndian.Uint32(rec[6:10]), true
}

func (h *codesHashIndex) close() {
	if h == nil {
		return
	}
	if h.idx != nil {
		h.idx.Close()
	}
	closeFileMapping(h.offs, h.offsF, h.offsM2)
	h.offs, h.offsF, h.offsM2 = nil, nil, nil
}
