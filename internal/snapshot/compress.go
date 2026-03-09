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

package snapshot

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Batch encoding format (before zstd compression):
//
//   [4 bytes] entry count (little-endian uint32)
//   For each entry:
//     [4 bytes] key length   (little-endian uint32)
//     [key...]
//     [4 bytes] value length (little-endian uint32)
//     [value...]
//
// The entire batch is then compressed with zstd (level 3 — fast, good ratio).
// Typical compression ratio for account/storage data: 2x-4x.

const (
	// zstdLevel is the compression level for snapshot data.
	// Level 3 offers a good balance between speed and compression ratio.
	zstdLevel = 3

	// maxDecompressedSize is the hard limit on decompressed data to prevent
	// OOM from malicious payloads (64 MB).
	maxDecompressedSize = 64 * 1024 * 1024

	// maxBatchEntries is the maximum number of entries in a batch.
	maxBatchEntries = 100000
)

// BatchEntry is a key-value pair in a compressed batch.
type BatchEntry struct {
	Key   []byte
	Value []byte
}

// Encoder/decoder pool to avoid repeated allocations.
var (
	encoderOnce sync.Once
	decoderOnce sync.Once
	sharedEnc   *zstd.Encoder
	sharedDec   *zstd.Decoder
)

func getEncoder() *zstd.Encoder {
	encoderOnce.Do(func() {
		var err error
		sharedEnc, err = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevel(zstdLevel)),
			zstd.WithEncoderConcurrency(1),
		)
		if err != nil {
			panic("snapshot: failed to create zstd encoder: " + err.Error())
		}
	})
	return sharedEnc
}

func getDecoder() *zstd.Decoder {
	decoderOnce.Do(func() {
		var err error
		sharedDec, err = zstd.NewReader(nil,
			zstd.WithDecoderMaxMemory(maxDecompressedSize),
			zstd.WithDecoderConcurrency(1),
		)
		if err != nil {
			panic("snapshot: failed to create zstd decoder: " + err.Error())
		}
	})
	return sharedDec
}

// EncodeBatch serializes a slice of key-value entries into binary format and
// compresses it with zstd. Returns the compressed payload.
func EncodeBatch(entries []BatchEntry) ([]byte, error) {
	if len(entries) > maxBatchEntries {
		return nil, fmt.Errorf("batch too large: %d entries (max %d)", len(entries), maxBatchEntries)
	}

	// Pre-calculate buffer size.
	size := 4 // entry count
	for _, e := range entries {
		size += 4 + len(e.Key) + 4 + len(e.Value)
	}

	buf := make([]byte, 0, size)

	// Entry count.
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(len(entries)))
	buf = append(buf, tmp[:]...)

	// Entries.
	for _, e := range entries {
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(e.Key)))
		buf = append(buf, tmp[:]...)
		buf = append(buf, e.Key...)
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(e.Value)))
		buf = append(buf, tmp[:]...)
		buf = append(buf, e.Value...)
	}

	// Compress.
	enc := getEncoder()
	compressed := enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
	return compressed, nil
}

// DecodeBatch decompresses a zstd payload and deserializes it into key-value entries.
func DecodeBatch(compressed []byte) ([]BatchEntry, error) {
	if len(compressed) == 0 {
		return nil, nil
	}

	dec := getDecoder()
	buf, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("zstd decompress: %w", err)
	}

	if len(buf) < 4 {
		return nil, errors.New("batch too short for entry count")
	}

	count := binary.LittleEndian.Uint32(buf[:4])
	if count > maxBatchEntries {
		return nil, fmt.Errorf("batch entry count %d exceeds max %d", count, maxBatchEntries)
	}
	off := 4

	entries := make([]BatchEntry, count)
	for i := uint32(0); i < count; i++ {
		if off+4 > len(buf) {
			return nil, errors.New("batch truncated at key length")
		}
		kLen := binary.LittleEndian.Uint32(buf[off : off+4])
		off += 4
		if off+int(kLen) > len(buf) {
			return nil, errors.New("batch truncated at key data")
		}
		key := make([]byte, kLen)
		copy(key, buf[off:off+int(kLen)])
		off += int(kLen)

		if off+4 > len(buf) {
			return nil, errors.New("batch truncated at value length")
		}
		vLen := binary.LittleEndian.Uint32(buf[off : off+4])
		off += 4
		if off+int(vLen) > len(buf) {
			return nil, errors.New("batch truncated at value data")
		}
		val := make([]byte, vLen)
		copy(val, buf[off:off+int(vLen)])
		off += int(vLen)

		entries[i] = BatchEntry{Key: key, Value: val}
	}

	return entries, nil
}

// CompressRaw compresses arbitrary data with zstd.
func CompressRaw(data []byte) []byte {
	enc := getEncoder()
	return enc.EncodeAll(data, make([]byte, 0, len(data)/2))
}

// DecompressRaw decompresses zstd-compressed data.
func DecompressRaw(compressed []byte) ([]byte, error) {
	dec := getDecoder()
	return dec.DecodeAll(compressed, nil)
}
