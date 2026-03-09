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

package rlp

import (
	"bytes"
	"testing"
)

// FuzzRLPDecodeEncodeRoundtrip encodes various Go types to RLP and decodes
// them back, verifying the roundtrip produces identical results.
func FuzzRLPDecodeEncodeRoundtrip(f *testing.F) {
	// Seed corpus with representative values
	f.Add(uint64(0))
	f.Add(uint64(1))
	f.Add(uint64(127))
	f.Add(uint64(128))
	f.Add(uint64(256))
	f.Add(uint64(0xFFFFFFFF))
	f.Add(uint64(0xFFFFFFFFFFFFFFFF))

	f.Fuzz(func(t *testing.T, val uint64) {
		// Encode uint64
		encoded, err := EncodeToBytes(val)
		if err != nil {
			t.Fatalf("failed to encode uint64 %d: %v", val, err)
		}

		// Decode back
		var decoded uint64
		err = DecodeBytes(encoded, &decoded)
		if err != nil {
			t.Fatalf("failed to decode uint64 from encoded bytes: %v", err)
		}

		if val != decoded {
			t.Fatalf("roundtrip mismatch: original=%d, decoded=%d", val, decoded)
		}
	})
}

// FuzzRLPDecodeArbitrary feeds random bytes to the RLP decoder, ensuring it
// never panics regardless of input. Errors are expected and acceptable.
func FuzzRLPDecodeArbitrary(f *testing.F) {
	// Seed with various RLP byte patterns
	f.Add([]byte{})                       // empty
	f.Add([]byte{0x00})                   // single byte 0
	f.Add([]byte{0x7f})                   // max single byte
	f.Add([]byte{0x80})                   // empty string
	f.Add([]byte{0xc0})                   // empty list
	f.Add([]byte{0x83, 0x64, 0x6f, 0x67}) // "dog"
	f.Add([]byte{0xc8, 0x83, 0x63, 0x61, 0x74, 0x83, 0x64, 0x6f, 0x67}) // ["cat","dog"]
	f.Add([]byte{0xb8, 0x38})             // long string header (56 bytes)
	f.Add([]byte{0xf8, 0x38})             // long list header (56 bytes)
	f.Add([]byte{0xff})                   // max byte
	f.Add([]byte{0xbf, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) // huge string length

	f.Fuzz(func(t *testing.T, data []byte) {
		// Attempt to decode as various types. None should panic.

		// Decode as []byte
		var bs []byte
		_ = DecodeBytes(data, &bs)

		// Decode as string
		var s string
		_ = DecodeBytes(data, &s)

		// Decode as uint64
		var u uint64
		_ = DecodeBytes(data, &u)

		// Decode as bool
		var b bool
		_ = DecodeBytes(data, &b)

		// Decode as [][]byte (nested)
		var nested [][]byte
		_ = DecodeBytes(data, &nested)

		// Decode as []string
		var ss []string
		_ = DecodeBytes(data, &ss)
	})
}

// FuzzRLPDecodeString feeds random bytes to the Stream decoder for string
// operations, ensuring no panics on arbitrary input.
func FuzzRLPDecodeString(f *testing.F) {
	f.Add([]byte{0x80})                   // empty string
	f.Add([]byte{0x83, 0x64, 0x6f, 0x67}) // "dog"
	f.Add([]byte{0x01})                   // single byte 1
	f.Add([]byte{0x7f})                   // single byte 127
	f.Add([]byte{0xb8, 0x00})             // long string header, length 0 (non-canonical)
	f.Add([]byte{})                       // empty input

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		s := NewStream(r, uint64(len(data)))

		// Try reading as bytes
		_, _ = s.Bytes()

		// Reset and try reading as uint
		r.Reset(data)
		s.Reset(r, uint64(len(data)))
		_, _ = s.Uint()

		// Reset and try reading as bool
		r.Reset(data)
		s.Reset(r, uint64(len(data)))
		_, _ = s.Bool()

		// Reset and try reading as Raw
		r.Reset(data)
		s.Reset(r, uint64(len(data)))
		_, _ = s.Raw()
	})
}

// FuzzRLPDecodeList feeds random bytes to the Stream decoder for list
// operations, ensuring no panics on arbitrary input.
func FuzzRLPDecodeList(f *testing.F) {
	f.Add([]byte{0xc0})                                                                     // empty list
	f.Add([]byte{0xc8, 0x83, 0x63, 0x61, 0x74, 0x83, 0x64, 0x6f, 0x67})                   // ["cat","dog"]
	f.Add([]byte{0xc1, 0x80})                                                               // [""]
	f.Add([]byte{0xc3, 0xc1, 0x80, 0x80})                                                   // [[""],0x80]
	f.Add([]byte{0xf8, 0x02, 0x01, 0x02})                                                   // long list [1, 2]
	f.Add([]byte{0xc4, 0x01, 0x02, 0x03, 0x04})                                             // [1,2,3,4]

	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		s := NewStream(r, uint64(len(data)))

		// Try to open as list and read elements
		_, err := s.List()
		if err != nil {
			return
		}

		// Read up to 100 elements to avoid infinite loops on malicious input
		for i := 0; i < 100; i++ {
			_, err := s.Bytes()
			if err != nil {
				break
			}
		}
		_ = s.ListEnd()
	})
}

// FuzzRLPByteSliceRoundtrip encodes byte slices to RLP and decodes them back,
// verifying the roundtrip produces identical byte slices.
func FuzzRLPByteSliceRoundtrip(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x7f})
	f.Add([]byte{0x80})
	f.Add([]byte("hello"))
	f.Add(bytes.Repeat([]byte{0xAB}, 55))
	f.Add(bytes.Repeat([]byte{0xCD}, 56))
	f.Add(bytes.Repeat([]byte{0xEF}, 1024))

	f.Fuzz(func(t *testing.T, data []byte) {
		encoded, err := EncodeToBytes(data)
		if err != nil {
			t.Fatalf("failed to encode []byte: %v", err)
		}

		var decoded []byte
		err = DecodeBytes(encoded, &decoded)
		if err != nil {
			t.Fatalf("failed to decode []byte: %v", err)
		}

		if !bytes.Equal(data, decoded) {
			t.Fatalf("roundtrip mismatch: original=%x, decoded=%x", data, decoded)
		}
	})
}

// FuzzRLPStringSliceRoundtrip encodes string slices to RLP and decodes them
// back, verifying the roundtrip produces identical results.
func FuzzRLPStringSliceRoundtrip(f *testing.F) {
	f.Add("hello", "world")
	f.Add("", "")
	f.Add("a", "bb")
	f.Add("test", "fuzz")

	f.Fuzz(func(t *testing.T, s1, s2 string) {
		// Limit string size to avoid excessive memory allocation
		if len(s1) > 1024 || len(s2) > 1024 {
			return
		}

		original := []string{s1, s2}
		encoded, err := EncodeToBytes(original)
		if err != nil {
			t.Fatalf("failed to encode []string: %v", err)
		}

		var decoded []string
		err = DecodeBytes(encoded, &decoded)
		if err != nil {
			t.Fatalf("failed to decode []string: %v", err)
		}

		if len(decoded) != len(original) {
			t.Fatalf("length mismatch: original=%d, decoded=%d", len(original), len(decoded))
		}
		for i := range original {
			if original[i] != decoded[i] {
				t.Fatalf("element %d mismatch: original=%q, decoded=%q", i, original[i], decoded[i])
			}
		}
	})
}
