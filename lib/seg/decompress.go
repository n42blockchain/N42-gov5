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
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/common/assert"
	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/mmap"
)

type ErrCompressedFileCorrupted struct {
	FileName string
	Reason   string
}

func (e ErrCompressedFileCorrupted) Error() string {
	return fmt.Sprintf("compressed file %q dictionary is corrupted: %s", e.FileName, e.Reason)
}

func (e ErrCompressedFileCorrupted) Is(err error) bool {
	var e1 *ErrCompressedFileCorrupted
	return errors.As(err, &e1)
}

// Decompressor provides access to the superstrings in a file produced by a compressor
type Decompressor struct {
	f               *os.File
	mmapHandle2     *[mmap.MaxMapSize]byte // mmap handle for windows (this is used to close mmap)
	dict            *patternTable
	posDict         *posTable
	mmapHandle1     []byte // mmap handle for unix (this is used to close mmap)
	data            []byte // slice of correct size for the decompressor to work with
	wordsStart      uint64 // Offset of whether the superstrings actually start
	size            int64
	modTime         time.Time
	wordsCount      uint64
	emptyWordsCount uint64

	filePath, fileName string
}

const (
	// Maximal Huffman tree depth
	// Note: mainnet has patternMaxDepth 31
	maxAllowedDepth = 50

	compressedMinSize = 32
)

// Tables with bitlen greater than threshold will be condensed.
// Condensing reduces size of decompression table but leads to slower reads.
// To disable condensing at all set to 9 (we dont use tables larger than 2^9)
// To enable condensing for tables of size larger 64 = 6
// for all tables                                    = 0
// There is no sense to condense tables of size [1 - 64] in terms of performance
//
// Should be set before calling NewDecompression.
var condensePatternTableBitThreshold = 9

func init() {
	v, _ := os.LookupEnv("DECOMPRESS_CONDENSITY")
	if v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			panic(err)
		}
		if i < 3 || i > 9 {
			panic("DECOMPRESS_CONDENSITY: only numbers in range 3-9 are acceptable ")
		}
		condensePatternTableBitThreshold = i
		fmt.Printf("set DECOMPRESS_CONDENSITY to %d\n", i)
	}
}

func SetDecompressionTableCondensity(fromBitSize int) {
	condensePatternTableBitThreshold = fromBitSize
}

func NewDecompressor(compressedFilePath string) (*Decompressor, error) {
	_, fName := filepath.Split(compressedFilePath)
	var err error
	var closeDecompressor = true
	d := &Decompressor{
		filePath: compressedFilePath,
		fileName: fName,
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("decompressing file: %s, %+v, trace: %s", compressedFilePath, rec, dbg.Stack())
		}
		if (err != nil || closeDecompressor) && d != nil {
			d.Close()
			d = nil
		}
	}()

	d.f, err = os.Open(compressedFilePath)
	if err != nil {
		return nil, err
	}

	var stat os.FileInfo
	if stat, err = d.f.Stat(); err != nil {
		return nil, err
	}
	d.size = stat.Size()
	if d.size < compressedMinSize {
		return nil, &ErrCompressedFileCorrupted{
			FileName: fName,
			Reason: fmt.Sprintf("invalid file size %s, expected at least %s",
				datasize.ByteSize(d.size).HR(), datasize.ByteSize(compressedMinSize).HR())}
	}

	d.modTime = stat.ModTime()
	if d.mmapHandle1, d.mmapHandle2, err = mmap.Mmap(d.f, int(d.size)); err != nil {
		return nil, err
	}
	d.data = d.mmapHandle1[:d.size]
	defer d.EnableReadAhead().DisableReadAhead() //speedup opening on slow drives

	// Detect file format version
	headerOffset := uint64(0)
	wordsCount := binary.BigEndian.Uint64(d.data[:8])
	emptyWordsCount := binary.BigEndian.Uint64(d.data[8:16])
	dictSize := binary.BigEndian.Uint64(d.data[16:24])

	// If dictSize is unreasonably large (> 1TB or > file size), it's likely V1.1 format
	// V1.1 format has a 32-byte header before the actual data
	if dictSize > uint64(d.size) || dictSize > 1<<40 || wordsCount > 1<<40 {
		headerOffset = 32
		if d.size < int64(headerOffset+24) {
			return nil, &ErrCompressedFileCorrupted{
				FileName: fName,
				Reason:   "file too small for V1.1 format"}
		}
		wordsCount = binary.BigEndian.Uint64(d.data[headerOffset : headerOffset+8])
		emptyWordsCount = binary.BigEndian.Uint64(d.data[headerOffset+8 : headerOffset+16])
		dictSize = binary.BigEndian.Uint64(d.data[headerOffset+16 : headerOffset+24])
	}

	d.wordsCount = wordsCount
	d.emptyWordsCount = emptyWordsCount

	pos := headerOffset + 24

	if pos+dictSize > uint64(d.size) {
		return nil, &ErrCompressedFileCorrupted{
			FileName: fName,
			Reason: fmt.Sprintf("invalid patterns dictSize=%s while file size is just %s",
				datasize.ByteSize(dictSize).HR(), datasize.ByteSize(d.size).HR())}
	}

	// Read pattern dictionary
	if err = d.readPatternDict(pos, dictSize, fName); err != nil {
		return nil, err
	}

	if assert.Enable && pos != headerOffset+24 {
		panic(fmt.Sprintf("pos != headerOffset+24: pos=%d, headerOffset=%d", pos, headerOffset))
	}
	pos += dictSize

	// Read position dictionary
	posDictSize := binary.BigEndian.Uint64(d.data[pos : pos+8])
	pos += 8

	if pos+posDictSize > uint64(d.size) {
		return nil, &ErrCompressedFileCorrupted{
			FileName: fName,
			Reason: fmt.Sprintf("invalid dictSize=%s overflows file size of %s",
				datasize.ByteSize(posDictSize).HR(), datasize.ByteSize(d.size).HR())}
	}

	if err = d.readPosDict(pos, posDictSize, fName); err != nil {
		return nil, err
	}

	d.wordsStart = pos + posDictSize

	if d.Count() == 0 && posDictSize == 0 && d.size > compressedMinSize {
		return nil, &ErrCompressedFileCorrupted{
			FileName: fName, Reason: fmt.Sprintf("size %v but no words in it", datasize.ByteSize(d.size).HR())}
	}
	closeDecompressor = false
	return d, nil
}

func (d *Decompressor) readPatternDict(pos, dictSize uint64, fName string) error {
	data := d.data[pos : pos+dictSize]

	var depths []uint64
	var patterns [][]byte
	var dictPos uint64
	var patternMaxDepth uint64

	for dictPos < dictSize {
		depth, ns := binary.Uvarint(data[dictPos:])
		if depth > maxAllowedDepth {
			return &ErrCompressedFileCorrupted{
				FileName: fName,
				Reason:   fmt.Sprintf("depth=%d > patternMaxDepth=%d ", depth, maxAllowedDepth)}
		}
		depths = append(depths, depth)
		if depth > patternMaxDepth {
			patternMaxDepth = depth
		}
		dictPos += uint64(ns)
		l, n := binary.Uvarint(data[dictPos:])
		dictPos += uint64(n)
		patterns = append(patterns, data[dictPos:dictPos+l])
		dictPos += l
	}

	if dictSize > 0 {
		var bitLen int
		if patternMaxDepth > 9 {
			bitLen = 9
		} else {
			bitLen = int(patternMaxDepth)
		}
		d.dict = newPatternTable(bitLen)
		if _, err := buildCondensedPatternTable(d.dict, depths, patterns, 0, 0, 0, patternMaxDepth); err != nil {
			return &ErrCompressedFileCorrupted{FileName: fName, Reason: err.Error()}
		}
	}
	return nil
}

func (d *Decompressor) readPosDict(pos, dictSize uint64, fName string) error {
	data := d.data[pos : pos+dictSize]

	var posDepths []uint64
	var poss []uint64
	var posMaxDepth uint64
	var dictPos uint64

	for dictPos < dictSize {
		depth, ns := binary.Uvarint(data[dictPos:])
		if depth > maxAllowedDepth {
			return &ErrCompressedFileCorrupted{FileName: fName, Reason: fmt.Sprintf("posMaxDepth=%d", depth)}
		}
		posDepths = append(posDepths, depth)
		if depth > posMaxDepth {
			posMaxDepth = depth
		}
		dictPos += uint64(ns)
		dp, n := binary.Uvarint(data[dictPos:])
		dictPos += uint64(n)
		poss = append(poss, dp)
	}

	if dictSize > 0 {
		var bitLen int
		if posMaxDepth > 9 {
			bitLen = 9
		} else {
			bitLen = int(posMaxDepth)
		}
		tableSize := 1 << bitLen
		d.posDict = &posTable{
			bitLen: bitLen,
			pos:    make([]uint64, tableSize),
			lens:   make([]byte, tableSize),
			ptrs:   make([]*posTable, tableSize),
		}
		if _, err := buildPosTable(posDepths, poss, d.posDict, 0, 0, 0, posMaxDepth); err != nil {
			return &ErrCompressedFileCorrupted{FileName: fName, Reason: err.Error()}
		}
	}
	return nil
}

func (d *Decompressor) DataHandle() unsafe.Pointer {
	return unsafe.Pointer(&d.data[0])
}

func (d *Decompressor) Size() int64 {
	return d.size
}

func (d *Decompressor) ModTime() time.Time {
	return d.modTime
}

func (d *Decompressor) IsOpen() bool {
	return d != nil && d.f != nil
}

func (d *Decompressor) Close() {
	if d.f != nil {
		if err := mmap.Munmap(d.mmapHandle1, d.mmapHandle2); err != nil {
			log.Log(dbg.FileCloseLogLevel, "unmap", "err", err, "file", d.FileName(), "stack", dbg.Stack())
		}
		if err := d.f.Close(); err != nil {
			log.Log(dbg.FileCloseLogLevel, "close", "err", err, "file", d.FileName(), "stack", dbg.Stack())
		}
		d.f = nil
		d.data = nil
		d.posDict = nil
		d.dict = nil
	}
}

func (d *Decompressor) FilePath() string { return d.filePath }
func (d *Decompressor) FileName() string { return d.fileName }

// WithReadAhead - Expect read in sequential order. (Hence, pages in the given range can be aggressively read ahead, and may be freed soon after they are accessed.)
func (d *Decompressor) WithReadAhead(f func() error) error {
	if d == nil || d.mmapHandle1 == nil {
		return nil
	}
	_ = mmap.MadviseSequential(d.mmapHandle1)
	defer mmap.MadviseRandom(d.mmapHandle1)
	return f()
}

// DisableReadAhead - usage: `defer d.EnableReadAhead().DisableReadAhead()`. Please don't use this funcs without `defer` to avoid leak.
func (d *Decompressor) DisableReadAhead() {
	if d == nil || d.mmapHandle1 == nil {
		return
	}
	_ = mmap.MadviseRandom(d.mmapHandle1)
}

func (d *Decompressor) EnableReadAhead() *Decompressor {
	if d == nil || d.mmapHandle1 == nil {
		return d
	}
	_ = mmap.MadviseSequential(d.mmapHandle1)
	return d
}

func (d *Decompressor) EnableMadvNormal() *Decompressor {
	if d == nil || d.mmapHandle1 == nil {
		return d
	}
	_ = mmap.MadviseNormal(d.mmapHandle1)
	return d
}

func (d *Decompressor) EnableMadvWillNeed() *Decompressor {
	if d == nil || d.mmapHandle1 == nil {
		return d
	}
	_ = mmap.MadviseWillNeed(d.mmapHandle1)
	return d
}

func (d *Decompressor) Count() int           { return int(d.wordsCount) }
func (d *Decompressor) EmptyWordsCount() int { return int(d.emptyWordsCount) }

// MakeGetter creates an object that can be used to access superstrings in the decompressor's file
// Getter is not thread-safe, but there can be multiple getters used simultaneously and concurrently
// for the same decompressor
func (d *Decompressor) MakeGetter() *Getter {
	return &Getter{
		posDict:     d.posDict,
		data:        d.data[d.wordsStart:],
		patternDict: d.dict,
		fName:       d.fileName,
	}
}

