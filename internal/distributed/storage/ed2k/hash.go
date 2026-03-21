// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ed2k

import (
	"fmt"
	"io"
)

// ChunkSize is the standard ed2k chunk size: 9500 KiB = 9728000 bytes.
const ChunkSize = 9728000

// HashSize is the size of an ed2k hash (MD4 = 16 bytes).
const HashSize = 16

// Hash computes the ed2k hash of data using the standard algorithm:
//  1. Split data into ChunkSize (9728000 byte) chunks.
//  2. Compute MD4 of each chunk.
//  3. If only one chunk, that MD4 is the file hash.
//  4. If multiple chunks, concatenate all MD4 hashes and compute MD4 of the result.
func Hash(data []byte) [HashSize]byte {
	if len(data) == 0 {
		// Empty file: MD4 of empty input
		h := NewMD4()
		var result [HashSize]byte
		copy(result[:], h.Sum(nil))
		return result
	}

	numChunks := (len(data) + ChunkSize - 1) / ChunkSize
	chunkHashes := make([]byte, 0, numChunks*HashSize)
	for offset := 0; offset < len(data); offset += ChunkSize {
		end := offset + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		h := NewMD4()
		h.Write(data[offset:end])
		chunkHashes = append(chunkHashes, h.Sum(nil)...)
	}

	// Single chunk: use it directly
	if len(chunkHashes) == HashSize {
		var result [HashSize]byte
		copy(result[:], chunkHashes)
		return result
	}

	// Multiple chunks: hash the concatenated chunk hashes
	h := NewMD4()
	h.Write(chunkHashes)
	var result [HashSize]byte
	copy(result[:], h.Sum(nil))
	return result
}

// HashReader computes the ed2k hash from an io.Reader, streaming data
// through ChunkSize buffers without loading the entire file into memory.
func HashReader(r io.Reader) ([HashSize]byte, int64, error) {
	buf := make([]byte, ChunkSize)
	var chunkHashes []byte
	var totalSize int64

	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			totalSize += int64(n)
			h := NewMD4()
			h.Write(buf[:n])
			chunkHashes = append(chunkHashes, h.Sum(nil)...)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return [HashSize]byte{}, 0, fmt.Errorf("ed2k hash read: %w", err)
		}
	}

	if len(chunkHashes) == 0 {
		// Empty input
		h := NewMD4()
		var result [HashSize]byte
		copy(result[:], h.Sum(nil))
		return result, 0, nil
	}

	if len(chunkHashes) == HashSize {
		var result [HashSize]byte
		copy(result[:], chunkHashes)
		return result, totalSize, nil
	}

	h := NewMD4()
	h.Write(chunkHashes)
	var result [HashSize]byte
	copy(result[:], h.Sum(nil))
	return result, totalSize, nil
}
