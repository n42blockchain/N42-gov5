// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package era

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

// Reader reads records from an EraE archive.
type Reader struct {
	file     *os.File
	header   Header
	index    []IndexEntry
	fileSize int64
}

// OpenReader opens an existing EraE archive for reading.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("era: open file: %w", err)
	}

	// Read header.
	headerBuf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, headerBuf); err != nil {
		f.Close()
		return nil, fmt.Errorf("era: read header: %w", err)
	}
	h, err := DecodeHeader(headerBuf)
	if err != nil {
		f.Close()
		return nil, err
	}

	// Read footer to get index entry count.
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("era: stat file: %w", err)
	}
	fileSize := fi.Size()
	if fileSize < int64(HeaderSize+FooterSize) {
		f.Close()
		return nil, ErrEmptyArchive
	}

	footerBuf := make([]byte, FooterSize)
	if _, err := f.ReadAt(footerBuf, fileSize-int64(FooterSize)); err != nil {
		f.Close()
		return nil, fmt.Errorf("era: read footer: %w", err)
	}
	count := binary.BigEndian.Uint64(footerBuf[0:8])

	if count == 0 {
		f.Close()
		return nil, ErrEmptyArchive
	}

	// Validate that the claimed index fits within the file.
	// The file must contain at least: Header + index entries + footer.
	maxPossibleEntries := uint64(fileSize-int64(HeaderSize+FooterSize)) / uint64(IndexEntrySize)
	if count > maxPossibleEntries {
		f.Close()
		return nil, fmt.Errorf("era: corrupt footer: index count %d exceeds file capacity %d", count, maxPossibleEntries)
	}

	// Read index entries.
	indexSize := int64(count) * int64(IndexEntrySize)
	indexOffset := fileSize - int64(FooterSize) - indexSize
	indexBuf := make([]byte, indexSize)
	if _, err := f.ReadAt(indexBuf, indexOffset); err != nil {
		f.Close()
		return nil, fmt.Errorf("era: read index: %w", err)
	}

	index := make([]IndexEntry, count)
	for i := uint64(0); i < count; i++ {
		off := i * uint64(IndexEntrySize)
		index[i] = IndexEntry{
			BlockNumber: binary.BigEndian.Uint64(indexBuf[off : off+8]),
			FileOffset:  int64(binary.BigEndian.Uint64(indexBuf[off+8 : off+16])),
		}
	}

	return &Reader{
		file:     f,
		header:   *h,
		index:    index,
		fileSize: fileSize,
	}, nil
}

// Header returns the archive header.
func (r *Reader) Header() Header {
	return r.header
}

// BlockRange returns the first and last block numbers in the archive.
func (r *Reader) BlockRange() (first, last uint64) {
	if len(r.index) == 0 {
		return 0, 0
	}
	return r.index[0].BlockNumber, r.index[len(r.index)-1].BlockNumber
}

// ReadRecord reads block data and receipts data for the given block number.
func (r *Reader) ReadRecord(blockNumber uint64) (blockData, receiptsData []byte, err error) {
	// Binary search for the block in the index.
	idx := sort.Search(len(r.index), func(i int) bool {
		return r.index[i].BlockNumber >= blockNumber
	})
	if idx >= len(r.index) || r.index[idx].BlockNumber != blockNumber {
		return nil, nil, fmt.Errorf("%w: block %d", ErrBlockNotFound, blockNumber)
	}

	offset := r.index[idx].FileOffset

	// Read block data length.
	var lenBuf [8]byte
	if _, err := r.file.ReadAt(lenBuf[:], offset); err != nil {
		return nil, nil, fmt.Errorf("era: read block data length: %w", err)
	}
	blockLen := binary.BigEndian.Uint64(lenBuf[:])
	if int64(blockLen) > r.fileSize-offset {
		return nil, nil, fmt.Errorf("era: block data length %d exceeds remaining file size", blockLen)
	}
	offset += 8

	// Read compressed block data.
	compressedBlock := make([]byte, blockLen)
	if _, err := r.file.ReadAt(compressedBlock, offset); err != nil {
		return nil, nil, fmt.Errorf("era: read block data: %w", err)
	}
	offset += int64(blockLen)

	// Read receipts data length.
	if _, err := r.file.ReadAt(lenBuf[:], offset); err != nil {
		return nil, nil, fmt.Errorf("era: read receipts data length: %w", err)
	}
	receiptsLen := binary.BigEndian.Uint64(lenBuf[:])
	if int64(receiptsLen) > r.fileSize-offset {
		return nil, nil, fmt.Errorf("era: receipts data length %d exceeds remaining file size", receiptsLen)
	}
	offset += 8

	// Read compressed receipts data.
	compressedReceipts := make([]byte, receiptsLen)
	if _, err := r.file.ReadAt(compressedReceipts, offset); err != nil {
		return nil, nil, fmt.Errorf("era: read receipts data: %w", err)
	}

	// Decompress both payloads.
	blockData, err = decompressRecord(compressedBlock)
	if err != nil {
		return nil, nil, fmt.Errorf("era: decompress block data: %w", err)
	}
	receiptsData, err = decompressRecord(compressedReceipts)
	if err != nil {
		return nil, nil, fmt.Errorf("era: decompress receipts data: %w", err)
	}

	return blockData, receiptsData, nil
}

// Count returns the number of records in the archive.
func (r *Reader) Count() int {
	return len(r.index)
}

// Close closes the reader.
func (r *Reader) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}
