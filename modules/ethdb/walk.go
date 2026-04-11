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
// Prefix-bounded cursor walk helpers.
// Walk iterates a kv.Cursor starting at startkey while the current key
// matches a (fixedbits, mask) prefix constraint, invoking walker(k,v)
// until it returns false or the prefix is exited.
// Bytesmask computes the (fixedbytes, trailingMask) pair used to honor
// non-byte-aligned fixedbits prefixes.

package ethdb

import (
	"bytes"

	"github.com/n42blockchain/N42/lib/kv"
)

// splitCursor implements a cursor with split keys.
// It is used to ignore incarnations in the middle of composite storage keys
// without reconstructing the key. The key is split into parts, and Seek/Next
// deliver all parts along with the corresponding value.
type splitCursor struct {
	c          kv.Cursor // Underlying cursor
	startkey   []byte    // Starting key (also contains bits that need to be preserved)
	matchBytes int
	mask       uint8
	part1end   int // Position in the key where the first part ends
	part2start int // Position in the key where the second part starts
	part3start int // Position in the key where the third part starts
}

func NewSplitCursor(c kv.Cursor, startkey []byte, matchBits int, part1end, part2start, part3start int) *splitCursor {
	var sc splitCursor
	sc.c = c
	sc.startkey = startkey
	sc.part1end = part1end
	sc.part2start = part2start
	sc.part3start = part3start
	sc.matchBytes, sc.mask = Bytesmask(matchBits)
	return &sc
}

func (sc *splitCursor) matchKey(k []byte) bool {
	if k == nil {
		return false
	}
	if sc.matchBytes == 0 {
		return true
	}
	if len(k) < sc.matchBytes {
		return false
	}
	if !bytes.Equal(k[:sc.matchBytes-1], sc.startkey[:sc.matchBytes-1]) {
		return false
	}
	return (k[sc.matchBytes-1] & sc.mask) == (sc.startkey[sc.matchBytes-1] & sc.mask)
}

func (sc *splitCursor) Seek() (key1, key2, key3, val []byte, err error) {
	k, v, err1 := sc.c.Seek(sc.startkey)
	if err1 != nil {
		return nil, nil, nil, nil, err1
	}
	if !sc.matchKey(k) {
		return nil, nil, nil, nil, nil
	}
	// Bounds check to prevent slice out of range
	if len(k) < sc.part1end || len(k) < sc.part2start || len(k) < sc.part3start {
		return nil, nil, nil, nil, nil
	}
	return k[:sc.part1end], k[sc.part2start:sc.part3start], k[sc.part3start:], v, nil
}

func (sc *splitCursor) Next() (key1, key2, key3, val []byte, err error) {
	k, v, err1 := sc.c.Next()
	if err1 != nil {
		return nil, nil, nil, nil, err1
	}
	if !sc.matchKey(k) {
		return nil, nil, nil, nil, nil
	}
	// Bounds check to prevent slice out of range
	if len(k) < sc.part1end || len(k) < sc.part2start || len(k) < sc.part3start {
		return nil, nil, nil, nil, nil
	}
	return k[:sc.part1end], k[sc.part2start:sc.part3start], k[sc.part3start:], v, nil
}

var EndSuffix = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
