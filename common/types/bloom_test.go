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

package types

import (
	"hash"
	"testing"
)

var _ hash.Hash64 = newHasher(nil)

func TestBloom_Contain(t *testing.T) {
	bloom, _ := NewBloom(10)
	hashes := []Hash{{0x01}, {0x02}, {0x03}, {0x04}, {0x05}, {0x06}, {0x07}, {0x08}, {0x09}, {0x0a}}
	for _, hash := range hashes {
		bloom.Add(hash.Bytes())
	}

	searchHash := []Hash{{0x10}, {0x11}, {0x01}}

	for _, hash := range searchHash {
		if bloom.Contain(hash.Bytes()) {
			t.Logf("hash %d is in hashes %d", hash, hashes)
		} else {
			t.Logf("hash %d is not in hashes %d", hash, hashes)
		}
	}

	b, _ := bloom.Marshal()
	t.Logf("bloom Marshal: %+v", b)

}

func TestHasherMethods(t *testing.T) {
	h := newHasher([]byte{0x01, 0x02})
	if got := h.Sum64(); got != 0x0102 {
		t.Fatalf("Sum64() = %d, want %d", got, 0x0102)
	}

	sum := h.Sum(nil)
	if len(sum) != 8 {
		t.Fatalf("Sum() len = %d, want 8", len(sum))
	}

	h.Reset()
	if got := h.Sum64(); got != 0 {
		t.Fatalf("Sum64() after Reset = %d, want 0", got)
	}

	if _, err := h.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := h.Sum64(); got != 0x0102030405060708 {
		t.Fatalf("Sum64() = %x, want %x", got, uint64(0x0102030405060708))
	}
}
