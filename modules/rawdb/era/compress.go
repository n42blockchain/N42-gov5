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
//
// Zstd codec pools for EraE archive record compression.
// encoderPool and decoderPool reuse *zstd.Encoder/*zstd.Decoder
// instances at SpeedDefault (level 3), the speed/ratio sweet spot
// for protobuf-encoded block data (~2-3x compression). getEncoder
// and getDecoder provide the lazy-init pool accessors.

package era

import (
	"sync"

	"github.com/klauspost/compress/zstd"
)

// EraE records are Zstd-compressed by default. The record format stores the
// COMPRESSED data length, so readers must decompress after reading.
// Compression level 3 gives a good speed/ratio tradeoff for block data
// (typical ratio ~2-3x on protobuf-encoded blocks).

const zstdLevel = zstd.SpeedDefault // Level 3

var (
	encoderPool sync.Pool
	decoderPool sync.Pool
)

func getEncoder() *zstd.Encoder {
	if v := encoderPool.Get(); v != nil {
		return v.(*zstd.Encoder)
	}
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstdLevel))
	return enc
}

func putEncoder(enc *zstd.Encoder) { encoderPool.Put(enc) }

func getDecoder() *zstd.Decoder {
	if v := decoderPool.Get(); v != nil {
		return v.(*zstd.Decoder)
	}
	dec, _ := zstd.NewReader(nil)
	return dec
}

func putDecoder(dec *zstd.Decoder) { decoderPool.Put(dec) }

// compressRecord compresses data using Zstd. Returns nil input unchanged.
func compressRecord(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	enc := getEncoder()
	defer putEncoder(enc)
	return enc.EncodeAll(data, make([]byte, 0, len(data)))
}

// decompressRecord decompresses Zstd data. Returns error on invalid input.
func decompressRecord(compressed []byte) ([]byte, error) {
	if len(compressed) == 0 {
		return compressed, nil
	}
	dec := getDecoder()
	defer putDecoder(dec)
	return dec.DecodeAll(compressed, make([]byte, 0, len(compressed)*3))
}
