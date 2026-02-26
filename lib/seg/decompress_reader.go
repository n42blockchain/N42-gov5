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
	"bytes"
	"fmt"

	"github.com/n42blockchain/N42/lib/common/dbg"
)

// Getter represent "reader" or "iterator" that can move across the data of the decompressor
// The full state of the getter can be captured by saving dataP, and dataBit
type Getter struct {
	patternDict *patternTable
	posDict     *posTable
	fName       string
	data        []byte
	dataP       uint64
	dataBit     int // Value 0..7 - position of the bit
	trace       bool
}

func (g *Getter) Trace(t bool)     { g.trace = t }
func (g *Getter) FileName() string { return g.fName }

func (g *Getter) nextPos(clean bool) (pos uint64) {
	if clean && g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	table := g.posDict
	if table.bitLen == 0 {
		return table.pos[0]
	}
	for l := byte(0); l == 0; {
		code := uint16(g.data[g.dataP]) >> g.dataBit
		if 8-g.dataBit < table.bitLen && int(g.dataP)+1 < len(g.data) {
			code |= uint16(g.data[g.dataP+1]) << (8 - g.dataBit)
		}
		code &= (uint16(1) << table.bitLen) - 1
		l = table.lens[code]
		if l == 0 {
			table = table.ptrs[code]
			g.dataBit += 9
		} else {
			g.dataBit += int(l)
			pos = table.pos[code]
		}
		g.dataP += uint64(g.dataBit / 8)
		g.dataBit %= 8
	}
	return pos
}

func (g *Getter) nextPattern() []byte {
	table := g.patternDict

	if table.bitLen == 0 {
		return *table.patterns[0].pattern
	}

	var l byte
	var pattern []byte
	for l == 0 {
		code := uint16(g.data[g.dataP]) >> g.dataBit
		if 8-g.dataBit < table.bitLen && int(g.dataP)+1 < len(g.data) {
			code |= uint16(g.data[g.dataP+1]) << (8 - g.dataBit)
		}
		code &= (uint16(1) << table.bitLen) - 1

		cw := table.condensedTableSearch(code)
		l = cw.len
		if l == 0 {
			table = cw.ptr
			g.dataBit += 9
		} else {
			g.dataBit += int(l)
			pattern = *cw.pattern
		}
		g.dataP += uint64(g.dataBit / 8)
		g.dataBit %= 8
	}
	return pattern
}

func (g *Getter) Size() int {
	return len(g.data)
}

func (g *Getter) Reset(offset uint64) {
	g.dataP = offset
	g.dataBit = 0
}

func (g *Getter) HasNext() bool {
	return g.dataP < uint64(len(g.data))
}

// Next extracts a compressed word from current offset in the file
// and appends it to the given buf, returning the result of appending
// After extracting next word, it moves to the beginning of the next one
func (g *Getter) Next(buf []byte) ([]byte, uint64) {
	savePos := g.dataP
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	if wordLen == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		if buf == nil { // wordLen == 0, means we have valid record of 0 size. nil - is the marker of "something not found"
			buf = []byte{}
		}
		return buf, g.dataP
	}

	bufOffset := len(buf)
	if len(buf)+int(wordLen) > cap(buf) {
		newBuf := make([]byte, len(buf)+int(wordLen))
		copy(newBuf, buf)
		buf = newBuf
	} else {
		buf = buf[:len(buf)+int(wordLen)]
	}

	// Loop below fills in the patterns
	bufPos := bufOffset
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		pt := g.nextPattern()
		copy(buf[bufPos:], pt)
	}
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP = savePos
	g.dataBit = 0
	g.nextPos(true) // Reset the state of huffman reader

	// Restore to the beginning of buf
	bufPos = bufOffset
	lastUncovered := bufOffset

	// Loop below fills the data which is not in the patterns
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			copy(buf[lastUncovered:bufPos], g.data[postLoopPos:postLoopPos+dif])
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if bufOffset+int(wordLen) > lastUncovered {
		dif := uint64(bufOffset + int(wordLen) - lastUncovered)
		copy(buf[lastUncovered:lastUncovered+int(dif)], g.data[postLoopPos:postLoopPos+dif])
		postLoopPos += dif
	}
	g.dataP = postLoopPos
	g.dataBit = 0
	return buf, postLoopPos
}

func (g *Getter) NextUncompressed() ([]byte, uint64) {
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	if wordLen == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		return g.data[g.dataP:g.dataP], g.dataP
	}
	g.nextPos(false)
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	pos := g.dataP
	g.dataP += wordLen
	return g.data[pos:g.dataP], g.dataP
}

// Skip moves offset to the next word and returns the new offset and the length of the word.
func (g *Getter) Skip() (uint64, int) {
	l := g.nextPos(true)
	l-- // because when create huffman tree we do ++ , because 0 is terminator
	if l == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		return g.dataP, 0
	}
	wordLen := int(l)

	var add uint64
	var bufPos int
	var lastUncovered int
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if wordLen < bufPos {
			panic(fmt.Sprintf("likely .idx is invalid: %s", g.fName))
		}
		if bufPos > lastUncovered {
			add += uint64(bufPos - lastUncovered)
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	if int(l) > lastUncovered {
		add += l - uint64(lastUncovered)
	}
	// Uncovered characters
	g.dataP += add
	return g.dataP, wordLen
}

func (g *Getter) SkipUncompressed() (uint64, int) {
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	if wordLen == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		return g.dataP, 0
	}
	g.nextPos(false)
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	g.dataP += wordLen
	return g.dataP, int(wordLen)
}

// Match returns true and next offset if the word at current offset fully matches the buf
// returns false and current offset otherwise.
func (g *Getter) Match(buf []byte) (bool, uint64) {
	savePos := g.dataP
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	lenBuf := len(buf)
	if wordLen == 0 || int(wordLen) != lenBuf {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		if lenBuf != 0 {
			g.dataP, g.dataBit = savePos, 0
		}
		return lenBuf == int(wordLen), g.dataP
	}

	var bufPos int
	// In the first pass, we only check patterns
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		pattern := g.nextPattern()
		if lenBuf < bufPos+len(pattern) || !bytes.Equal(buf[bufPos:bufPos+len(pattern)], pattern) {
			g.dataP, g.dataBit = savePos, 0
			return false, savePos
		}
	}
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP, g.dataBit = savePos, 0
	g.nextPos(true) // Reset the state of huffman decoder
	// Second pass - we check spaces not covered by the patterns
	var lastUncovered int
	bufPos = 0
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			if lenBuf < bufPos || !bytes.Equal(buf[lastUncovered:bufPos], g.data[postLoopPos:postLoopPos+dif]) {
				g.dataP, g.dataBit = savePos, 0
				return false, savePos
			}
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if int(wordLen) > lastUncovered {
		dif := wordLen - uint64(lastUncovered)
		if lenBuf < int(wordLen) || !bytes.Equal(buf[lastUncovered:wordLen], g.data[postLoopPos:postLoopPos+dif]) {
			g.dataP, g.dataBit = savePos, 0
			return false, savePos
		}
		postLoopPos += dif
	}
	if lenBuf != int(wordLen) {
		g.dataP, g.dataBit = savePos, 0
		return false, savePos
	}
	g.dataP, g.dataBit = postLoopPos, 0
	return true, postLoopPos
}

// MatchPrefix only checks if the word at the current offset has a buf prefix. Does not move offset to the next word.
func (g *Getter) MatchPrefix(prefix []byte) bool {
	savePos := g.dataP
	defer func() {
		g.dataP, g.dataBit = savePos, 0
	}()

	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	prefixLen := len(prefix)
	if wordLen == 0 || int(wordLen) < prefixLen {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		if prefixLen != 0 {
			g.dataP, g.dataBit = savePos, 0
		}
		return prefixLen == int(wordLen)
	}

	var bufPos int
	// In the first pass, we only check patterns
	// Only run this loop as far as the prefix goes, there is no need to check further
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		pattern := g.nextPattern()
		var comparisonLen int
		if prefixLen < bufPos+len(pattern) {
			comparisonLen = prefixLen - bufPos
		} else {
			comparisonLen = len(pattern)
		}
		if bufPos < prefixLen {
			if !bytes.Equal(prefix[bufPos:bufPos+comparisonLen], pattern[:comparisonLen]) {
				return false
			}
		}
	}

	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP, g.dataBit = savePos, 0
	g.nextPos(true) // Reset the state of huffman decoder
	// Second pass - we check spaces not covered by the patterns
	var lastUncovered int
	bufPos = 0
	for pos := g.nextPos(false); pos != 0 && lastUncovered < prefixLen; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			var comparisonLen int
			if prefixLen < lastUncovered+int(dif) {
				comparisonLen = prefixLen - lastUncovered
			} else {
				comparisonLen = int(dif)
			}
			if !bytes.Equal(prefix[lastUncovered:lastUncovered+comparisonLen], g.data[postLoopPos:postLoopPos+uint64(comparisonLen)]) {
				return false
			}
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if prefixLen > lastUncovered && int(wordLen) > lastUncovered {
		dif := wordLen - uint64(lastUncovered)
		var comparisonLen int
		if prefixLen < int(wordLen) {
			comparisonLen = prefixLen - lastUncovered
		} else {
			comparisonLen = int(dif)
		}
		if !bytes.Equal(prefix[lastUncovered:lastUncovered+comparisonLen], g.data[postLoopPos:postLoopPos+uint64(comparisonLen)]) {
			return false
		}
	}
	return true
}

// MatchCmp lexicographically compares given buf with the word at the current offset in the file.
// returns 0 if buf == word, -1 if buf < word, 1 if buf > word
func (g *Getter) MatchCmp(buf []byte) int {
	savePos := g.dataP
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	lenBuf := len(buf)
	if wordLen == 0 && lenBuf != 0 {
		g.dataP, g.dataBit = savePos, 0
		return 1
	}
	if wordLen == 0 && lenBuf == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		return 0
	}

	decoded := make([]byte, wordLen)
	var bufPos int
	// In the first pass, we only check patterns
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		pattern := g.nextPattern()
		copy(decoded[bufPos:], pattern)
	}
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP, g.dataBit = savePos, 0
	g.nextPos(true) // Reset the state of huffman decoder
	// Second pass - we check spaces not covered by the patterns
	var lastUncovered int
	bufPos = 0
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			copy(decoded[lastUncovered:bufPos], g.data[postLoopPos:postLoopPos+dif])
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}

	if int(wordLen) > lastUncovered {
		dif := wordLen - uint64(lastUncovered)
		copy(decoded[lastUncovered:wordLen], g.data[postLoopPos:postLoopPos+dif])
		postLoopPos += dif
	}
	cmp := bytes.Compare(buf, decoded)
	if cmp == 0 {
		g.dataP, g.dataBit = postLoopPos, 0
	} else {
		g.dataP, g.dataBit = savePos, 0
	}
	return cmp
}

// MatchPrefixCmp lexicographically compares given prefix with the word at the current offset in the file.
// returns 0 if buf == word, -1 if buf < word, 1 if buf > word
func (g *Getter) MatchPrefixCmp(prefix []byte) int {
	savePos := g.dataP
	defer func() {
		g.dataP, g.dataBit = savePos, 0
	}()

	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	prefixLen := len(prefix)
	if wordLen == 0 && prefixLen != 0 {
		return 1
	}
	if prefixLen == 0 {
		return 0
	}

	decoded := make([]byte, wordLen)
	var bufPos int
	// In the first pass, we only check patterns
	// Only run this loop as far as the prefix goes, there is no need to check further
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > prefixLen {
			break
		}
		pattern := g.nextPattern()
		copy(decoded[bufPos:], pattern)
	}

	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP, g.dataBit = savePos, 0
	g.nextPos(true) // Reset the state of huffman decoder
	// Second pass - we check spaces not covered by the patterns
	var lastUncovered int
	bufPos = 0
	for pos := g.nextPos(false); pos != 0 && lastUncovered < prefixLen; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			copy(decoded[lastUncovered:bufPos], g.data[postLoopPos:postLoopPos+dif])
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if prefixLen > lastUncovered && int(wordLen) > lastUncovered {
		dif := wordLen - uint64(lastUncovered)
		copy(decoded[lastUncovered:wordLen], g.data[postLoopPos:postLoopPos+dif])
	}
	if prefixLen > int(wordLen) {
		return bytes.Compare(prefix, decoded)
	}
	return bytes.Compare(prefix, decoded[:prefixLen])
}

func (g *Getter) MatchPrefixUncompressed(prefix []byte) int {
	savePos := g.dataP
	defer func() {
		g.dataP, g.dataBit = savePos, 0
	}()

	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	prefixLen := len(prefix)
	if wordLen == 0 && prefixLen != 0 {
		return 1
	}
	if prefixLen == 0 {
		return 0
	}

	g.nextPos(true)

	return bytes.Compare(prefix, g.data[g.dataP:g.dataP+wordLen])
}

// FastNext extracts a compressed word from current offset in the file
// into the given buf, returning a new byte slice which contains extracted word.
// It is important to allocate enough buf size. Could throw an error if word in file is larger then the buf size.
// After extracting next word, it moves to the beginning of the next one
func (g *Getter) FastNext(buf []byte) ([]byte, uint64) {
	defer func() {
		if rec := recover(); rec != nil {
			panic(fmt.Sprintf("file: %s, %s, %s", g.fName, rec, dbg.Stack()))
		}
	}()

	savePos := g.dataP
	wordLen := g.nextPos(true)
	wordLen-- // because when create huffman tree we do ++ , because 0 is terminator
	if wordLen == 0 {
		if g.dataBit > 0 {
			g.dataP++
			g.dataBit = 0
		}
		return buf[:wordLen], g.dataP
	}
	bufPos := 0
	lastUncovered := 0

	// Loop below fills in the patterns
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		pt := g.nextPattern()
		copy(buf[bufPos:], pt)
	}
	if g.dataBit > 0 {
		g.dataP++
		g.dataBit = 0
	}
	postLoopPos := g.dataP
	g.dataP = savePos
	g.dataBit = 0
	g.nextPos(true)        // Reset the state of huffman reader
	bufPos = lastUncovered // Restore to the beginning of buf
	// Loop below fills the data which is not in the patterns
	for pos := g.nextPos(false); pos != 0; pos = g.nextPos(false) {
		bufPos += int(pos) - 1
		if bufPos > lastUncovered {
			dif := uint64(bufPos - lastUncovered)
			copy(buf[lastUncovered:bufPos], g.data[postLoopPos:postLoopPos+dif])
			postLoopPos += dif
		}
		lastUncovered = bufPos + len(g.nextPattern())
	}
	if int(wordLen) > lastUncovered {
		dif := wordLen - uint64(lastUncovered)
		copy(buf[lastUncovered:wordLen], g.data[postLoopPos:postLoopPos+dif])
		postLoopPos += dif
	}
	g.dataP = postLoopPos
	g.dataBit = 0
	return buf[:wordLen], postLoopPos
}
