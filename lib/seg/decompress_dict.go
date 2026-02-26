/*
   Copyright 2022 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package seg

import (
	"errors"
	"fmt"
)

type word []byte // plain text word associated with code from dictionary

type codeword struct {
	pattern *word         // Pattern corresponding to entries
	ptr     *patternTable // pointer to deeper level tables
	code    uint16        // code associated with that word
	len     byte          // Number of bits in the codes
}

type patternTable struct {
	patterns []*codeword
	bitLen   int // Number of bits to lookup in the table
}

func newPatternTable(bitLen int) *patternTable {
	pt := &patternTable{
		bitLen: bitLen,
	}
	if bitLen <= condensePatternTableBitThreshold {
		pt.patterns = make([]*codeword, 1<<pt.bitLen)
	}
	return pt
}

func (pt *patternTable) insertWord(cw *codeword) {
	if pt.bitLen <= condensePatternTableBitThreshold {
		codeStep := uint16(1) << uint16(cw.len)
		codeFrom, codeTo := cw.code, cw.code+codeStep
		if pt.bitLen != int(cw.len) && cw.len > 0 {
			codeTo = codeFrom | (uint16(1) << pt.bitLen)
		}

		for c := codeFrom; c < codeTo; c += codeStep {
			pt.patterns[c] = cw
		}
		return
	}

	pt.patterns = append(pt.patterns, cw)
}

func (pt *patternTable) condensedTableSearch(code uint16) *codeword {
	if pt.bitLen <= condensePatternTableBitThreshold {
		return pt.patterns[code]
	}
	for _, cur := range pt.patterns {
		if cur.code == code {
			return cur
		}
		d := code - cur.code
		if d&1 != 0 {
			continue
		}
		if checkDistance(int(cur.len), int(d)) {
			return cur
		}
	}
	return nil
}

type posTable struct {
	pos    []uint64
	lens   []byte
	ptrs   []*posTable
	bitLen int
}

var condensedWordDistances = buildCondensedWordDistances()

func checkDistance(power int, d int) bool {
	for _, dist := range condensedWordDistances[power] {
		if dist == d {
			return true
		}
	}
	return false
}

func buildCondensedWordDistances() [][]int {
	dist2 := make([][]int, 10)
	for i := 1; i <= 9; i++ {
		dl := make([]int, 0)
		for j := 1 << i; j < 512; j += 1 << i {
			dl = append(dl, j)
		}
		dist2[i] = dl
	}
	return dist2
}

func buildCondensedPatternTable(table *patternTable, depths []uint64, patterns [][]byte, code uint16, bits int, depth uint64, maxDepth uint64) (int, error) {
	if maxDepth > maxAllowedDepth {
		return 0, fmt.Errorf("buildCondensedPatternTable: maxDepth=%d is too deep", maxDepth)
	}

	if len(depths) == 0 {
		return 0, nil
	}
	if depth == depths[0] {
		pattern := word(patterns[0])
		cw := &codeword{code: code, pattern: &pattern, len: byte(bits), ptr: nil}
		table.insertWord(cw)
		return 1, nil
	}
	if bits == 9 {
		var bitLen int
		if maxDepth > 9 {
			bitLen = 9
		} else {
			bitLen = int(maxDepth)
		}
		cw := &codeword{code: code, pattern: nil, len: byte(0), ptr: newPatternTable(bitLen)}
		table.insertWord(cw)
		return buildCondensedPatternTable(cw.ptr, depths, patterns, 0, 0, depth, maxDepth)
	}
	if maxDepth == 0 {
		return 0, errors.New("buildCondensedPatternTable: maxDepth reached zero")
	}
	b0, err := buildCondensedPatternTable(table, depths, patterns, code, bits+1, depth+1, maxDepth-1)
	if err != nil {
		return 0, err
	}
	b1, err := buildCondensedPatternTable(table, depths[b0:], patterns[b0:], (uint16(1)<<bits)|code, bits+1, depth+1, maxDepth-1)
	return b0 + b1, err
}

func buildPosTable(depths []uint64, poss []uint64, table *posTable, code uint16, bits int, depth uint64, maxDepth uint64) (int, error) {
	if maxDepth > maxAllowedDepth {
		return 0, fmt.Errorf("buildPosTable: maxDepth=%d is too deep", maxDepth)
	}
	if len(depths) == 0 {
		return 0, nil
	}
	if depth == depths[0] {
		p := poss[0]
		if table.bitLen == bits {
			table.pos[code] = p
			table.lens[code] = byte(bits)
			table.ptrs[code] = nil
		} else {
			codeStep := uint16(1) << bits
			codeFrom := code
			codeTo := code | (uint16(1) << table.bitLen)
			for c := codeFrom; c < codeTo; c += codeStep {
				table.pos[c] = p
				table.lens[c] = byte(bits)
				table.ptrs[c] = nil
			}
		}
		return 1, nil
	}
	if bits == 9 {
		var bitLen int
		if maxDepth > 9 {
			bitLen = 9
		} else {
			bitLen = int(maxDepth)
		}
		tableSize := 1 << bitLen
		newTable := &posTable{
			bitLen: bitLen,
			pos:    make([]uint64, tableSize),
			lens:   make([]byte, tableSize),
			ptrs:   make([]*posTable, tableSize),
		}
		table.pos[code] = 0
		table.lens[code] = byte(0)
		table.ptrs[code] = newTable
		return buildPosTable(depths, poss, newTable, 0, 0, depth, maxDepth)
	}
	if maxDepth == 0 {
		return 0, errors.New("buildPosTable: maxDepth reached zero")
	}
	b0, err := buildPosTable(depths, poss, table, code, bits+1, depth+1, maxDepth-1)
	if err != nil {
		return 0, err
	}
	b1, err := buildPosTable(depths[b0:], poss[b0:], table, (uint16(1)<<bits)|code, bits+1, depth+1, maxDepth-1)
	return b0 + b1, err
}
