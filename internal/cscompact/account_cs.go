// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// account_cs.go — columnar compression for AccountChangeSet.
//
// Reads Erigon v2 AccountChangeSet (DupSort: blockNum(8B) → addr(20B)+oldValue)
// and writes segment files with:
//   - Per-block entry counts (varint)
//   - Address dictionary (20B addr → varint index)
//   - Old value column (1B len + trimmed V2 bytes)
// Segments are zstd-compressed. 1M blocks per segment.

package cscompact

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

const (
	// CSSegmentSize aligns with header/body compact segments.
	CSSegmentSize = 8192
	csMaxFileSize = 2_000_000_000 // 2 GB
)

// AccountCSEntry is one decoded changeset entry.
type AccountCSEntry struct {
	Address  types.Address
	OldValue []byte // nil = account didn't exist before
}

// AccountCSCompactor reads Erigon AccountChangeSet and writes compressed segments.
type AccountCSCompactor struct {
	db        kv.RoDB
	outputDir string
}

func NewAccountCSCompactor(db kv.RoDB, outputDir string) *AccountCSCompactor {
	return &AccountCSCompactor{db: db, outputDir: outputDir}
}

func (c *AccountCSCompactor) Run(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(c.outputDir, 0755); err != nil {
		return err
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	// Open idx file.
	idxPath := filepath.Join(c.outputDir, "account_cs.idx")
	idxFile, err := os.OpenFile(idxPath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer idxFile.Close()

	// Determine resume point from idx.
	idxInfo, _ := idxFile.Stat()
	existingSegs := uint64(idxInfo.Size()) / 8
	resumeBlock := existingSegs * CSSegmentSize
	if resumeBlock > startBlock {
		startBlock = resumeBlock
		log.Info("Resuming from existing segments", "startBlock", startBlock, "segments", existingSegs)
	}
	idxFile.Seek(0, 2) // seek to end

	// Determine dat file number from last idx entry.
	var headFile uint16
	var headSize int64
	if existingSegs > 0 {
		var lastEntry [8]byte
		idxFile.ReadAt(lastEntry[:], int64(existingSegs-1)*8)
		headFile = binary.LittleEndian.Uint16(lastEntry[0:2])
		datPath := filepath.Join(c.outputDir, fmt.Sprintf("account_cs.%04d.dat", headFile))
		if fi, err := os.Stat(datPath); err == nil {
			headSize = fi.Size()
		}
	}

	t0 := time.Now()
	var totalIn, totalOut int64
	var segCount int

	for segStart := startBlock; segStart < endBlock; segStart += CSSegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		segEnd := segStart + CSSegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}

		// Read changeset entries for this segment.
		entries, entriesPerBlock, totalBytes, err := c.readSegment(segStart, segEnd)
		if err != nil {
			return fmt.Errorf("read segment %d-%d: %w", segStart, segEnd, err)
		}
		totalIn += totalBytes

		// Encode segment.
		compressed := encodeAccountCSSegment(entries, entriesPerBlock, enc)

		// File rotation.
		segSize := int64(4 + len(compressed))
		if headSize+segSize > csMaxFileSize {
			headFile++
			headSize = 0
		}

		// Write dat.
		datPath := filepath.Join(c.outputDir, fmt.Sprintf("account_cs.%04d.dat", headFile))
		datFile, err := os.OpenFile(datPath, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		datFile.Seek(0, 2)
		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(compressed)))
		datFile.Write(sizeBuf[:])
		datFile.Write(compressed)
		datFile.Close()

		// Write idx entry.
		var idxEntry [8]byte
		binary.LittleEndian.PutUint16(idxEntry[0:2], headFile)
		binary.LittleEndian.PutUint32(idxEntry[4:8], uint32(headSize))
		idxFile.Write(idxEntry[:])

		headSize += segSize
		totalOut += segSize
		segCount++

		if segCount%5 == 0 || segStart+CSSegmentSize >= endBlock {
			elapsed := time.Since(t0)
			ratio := float64(0)
			if totalIn > 0 {
				ratio = float64(totalOut) / float64(totalIn) * 100
			}
			log.Info("AccountCS compact",
				"block", segEnd-1,
				"segments", segCount,
				"in", fmt.Sprintf("%.1f GiB", float64(totalIn)/1e9),
				"out", fmt.Sprintf("%.1f GiB", float64(totalOut)/1e9),
				"ratio", fmt.Sprintf("%.1f%%", ratio),
				"elapsed", elapsed.Truncate(time.Second))
		}
	}

	return nil
}

// readSegment reads all AccountChangeSet entries for blocks [start, end).
func (c *AccountCSCompactor) readSegment(startBlock, endBlock uint64) (
	entries []AccountCSEntry, entriesPerBlock []uint32, totalBytes int64, err error,
) {
	tx, err := c.db.BeginRo(context.Background())
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(ErigonAccountChangeSet)
	if err != nil {
		return nil, nil, 0, err
	}
	defer cursor.Close()

	blockCount := endBlock - startBlock
	entriesPerBlock = make([]uint32, blockCount)

	// Seek to startBlock.
	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		return nil, nil, 0, err
	}

	for k != nil {
		if len(k) < 8 {
			k, v, err = cursor.Next()
			if err != nil {
				return nil, nil, 0, err
			}
			continue
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= endBlock {
			break
		}

		totalBytes += int64(len(k) + len(v))

		if blockNum >= startBlock {
			relBlock := blockNum - startBlock
			entriesPerBlock[relBlock]++

			var entry AccountCSEntry
			if len(v) >= 20 {
				copy(entry.Address[:], v[:20])
				if len(v) > 20 {
					entry.OldValue = make([]byte, len(v)-20)
					copy(entry.OldValue, v[20:])
				}
			}
			entries = append(entries, entry)
		}

		k, v, err = cursor.Next()
		if err != nil {
			return nil, nil, 0, err
		}
	}

	return entries, entriesPerBlock, totalBytes, nil
}

// encodeAccountCSSegment encodes entries in columnar format, returns zstd-compressed data.
func encodeAccountCSSegment(entries []AccountCSEntry, entriesPerBlock []uint32, enc *zstd.Encoder) []byte {
	blockCount := len(entriesPerBlock)
	totalEntries := len(entries)

	buf := make([]byte, 0, totalEntries*12)

	// Block count.
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(blockCount))
	buf = append(buf, tmp[:]...)

	// Entries per block (varint).
	for _, count := range entriesPerBlock {
		buf = appendVarint(buf, uint64(count))
	}

	// Address dictionary.
	addrMap := map[types.Address]int{}
	var dict []types.Address
	earlyRaw := false
	for i, e := range entries {
		if _, ok := addrMap[e.Address]; !ok {
			addrMap[e.Address] = len(dict)
			dict = append(dict, e.Address)
		}
		if i == 255 && len(dict) > 128 {
			earlyRaw = true
			break
		}
	}

	dictCost := len(dict)*20 + totalEntries*2
	rawCost := totalEntries * 20
	if !earlyRaw && dictCost < rawCost {
		buf = append(buf, 1) // dict mode
		buf = appendVarint(buf, uint64(len(dict)))
		for _, a := range dict {
			buf = append(buf, a[:]...)
		}
		for _, e := range entries {
			buf = appendVarint(buf, uint64(addrMap[e.Address]))
		}
	} else {
		buf = append(buf, 0) // raw mode
		for _, e := range entries {
			buf = append(buf, e.Address[:]...)
		}
	}

	// Old value column: [1B len][value bytes].
	for _, e := range entries {
		if e.OldValue == nil || len(e.OldValue) == 0 {
			buf = append(buf, 0) // didn't exist
		} else {
			buf = append(buf, byte(len(e.OldValue)))
			buf = append(buf, e.OldValue...)
		}
	}

	return enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
}

func appendVarint(buf []byte, v uint64) []byte {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}

// DecodeAccountCSSegment decompresses and decodes a segment.
// Returns entries in original order + per-block entry counts.
func DecodeAccountCSSegment(compressed []byte, dec *zstd.Decoder) (
	[]AccountCSEntry, []uint32, error,
) {
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("zstd decompress: %w", err)
	}
	if len(raw) < 4 {
		return nil, nil, fmt.Errorf("segment too short: %d", len(raw))
	}
	pos := 0

	// Block count.
	blockCount := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += 4

	// Entries per block.
	entriesPerBlock := make([]uint32, blockCount)
	totalEntries := 0
	for i := 0; i < blockCount; i++ {
		v, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, nil, fmt.Errorf("bad varint at block %d", i)
		}
		pos += n
		entriesPerBlock[i] = uint32(v)
		totalEntries += int(v)
	}

	entries := make([]AccountCSEntry, totalEntries)

	// Address column.
	if pos >= len(raw) {
		return nil, nil, fmt.Errorf("truncated at addr mode")
	}
	mode := raw[pos]
	pos++

	if mode == 1 {
		// Dictionary mode.
		dictLen, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, nil, fmt.Errorf("bad dict len")
		}
		pos += n
		dict := make([]types.Address, dictLen)
		for i := uint64(0); i < dictLen; i++ {
			if pos+20 > len(raw) {
				return nil, nil, fmt.Errorf("truncated dict")
			}
			copy(dict[i][:], raw[pos:pos+20])
			pos += 20
		}
		for i := 0; i < totalEntries; i++ {
			idx, n := binary.Uvarint(raw[pos:])
			if n <= 0 {
				return nil, nil, fmt.Errorf("bad addr index")
			}
			pos += n
			if idx >= dictLen {
				return nil, nil, fmt.Errorf("addr index out of range")
			}
			entries[i].Address = dict[idx]
		}
	} else {
		// Raw mode.
		for i := 0; i < totalEntries; i++ {
			if pos+20 > len(raw) {
				return nil, nil, fmt.Errorf("truncated raw addr")
			}
			copy(entries[i].Address[:], raw[pos:pos+20])
			pos += 20
		}
	}

	// Old value column.
	for i := 0; i < totalEntries; i++ {
		if pos >= len(raw) {
			return nil, nil, fmt.Errorf("truncated at value %d", i)
		}
		valLen := int(raw[pos])
		pos++
		if valLen > 0 {
			if pos+valLen > len(raw) {
				return nil, nil, fmt.Errorf("truncated value %d", i)
			}
			entries[i].OldValue = make([]byte, valLen)
			copy(entries[i].OldValue, raw[pos:pos+valLen])
			pos += valLen
		}
	}

	return entries, entriesPerBlock, nil
}

// AccountCSReader provides segment-cached access to compressed changesets.
type AccountCSReader struct {
	dir       string
	idxFile   *os.File
	dec       *zstd.Decoder
	segments  uint64
	dataFiles map[uint16]*os.File

	cachedSeg     int64
	cachedEntries []AccountCSEntry
	cachedPerBlk  []uint32
}

func OpenAccountCS(dir string) (*AccountCSReader, error) {
	idxPath := filepath.Join(dir, "account_cs.idx")
	idf, err := os.Open(idxPath)
	if err != nil {
		return nil, err
	}
	fi, _ := idf.Stat()
	dec, err := zstd.NewReader(nil)
	if err != nil {
		idf.Close()
		return nil, err
	}
	return &AccountCSReader{
		dir:       dir,
		idxFile:   idf,
		dec:       dec,
		segments:  uint64(fi.Size()) / 8,
		dataFiles: make(map[uint16]*os.File),
		cachedSeg: -1,
	}, nil
}

func (r *AccountCSReader) Close() {
	r.dec.Close()
	r.idxFile.Close()
	for _, f := range r.dataFiles {
		f.Close()
	}
}

// ReadBlock returns changeset entries for a specific block.
func (r *AccountCSReader) ReadBlock(blockNum uint64) ([]AccountCSEntry, error) {
	segNum := int64(blockNum / CSSegmentSize)
	relBlock := int(blockNum % CSSegmentSize)

	if segNum != r.cachedSeg {
		if err := r.loadSegment(segNum); err != nil {
			return nil, err
		}
	}

	// Find entries for this block using entriesPerBlock.
	offset := 0
	for i := 0; i < relBlock && i < len(r.cachedPerBlk); i++ {
		offset += int(r.cachedPerBlk[i])
	}
	if relBlock >= len(r.cachedPerBlk) {
		return nil, nil
	}
	count := int(r.cachedPerBlk[relBlock])
	if offset+count > len(r.cachedEntries) {
		return nil, fmt.Errorf("entry range out of bounds")
	}
	return r.cachedEntries[offset : offset+count], nil
}

func (r *AccountCSReader) loadSegment(segNum int64) error {
	if uint64(segNum) >= r.segments {
		return fmt.Errorf("segment %d out of range (%d)", segNum, r.segments)
	}

	var entryBuf [8]byte
	if _, err := r.idxFile.ReadAt(entryBuf[:], segNum*8); err != nil {
		return err
	}
	fileNum := binary.LittleEndian.Uint16(entryBuf[0:2])
	offset := binary.LittleEndian.Uint32(entryBuf[4:8])

	df, ok := r.dataFiles[fileNum]
	if !ok {
		path := filepath.Join(r.dir, fmt.Sprintf("account_cs.%04d.dat", fileNum))
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		r.dataFiles[fileNum] = f
		df = f
	}

	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(offset)); err != nil {
		return err
	}
	compSize := binary.LittleEndian.Uint32(sizeBuf[:])

	compressed := make([]byte, compSize)
	if _, err := df.ReadAt(compressed, int64(offset)+4); err != nil {
		return err
	}

	entries, perBlock, err := DecodeAccountCSSegment(compressed, r.dec)
	if err != nil {
		return err
	}

	r.cachedSeg = segNum
	r.cachedEntries = entries
	r.cachedPerBlk = perBlock
	return nil
}
