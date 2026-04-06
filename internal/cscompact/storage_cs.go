// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// storage_cs.go — columnar compression for StorageChangeSet.
//
// Erigon v2 format: key = blockNum(8B) + addr(20B) + incarnation(8B),
//                   value = slot(32B) + oldValue(variable)
//
// Columnar layout per segment (8192 blocks, zstd compressed):
//   [4B blockCount][varint × blockCount: entriesPerBlock]
//   [address dict: mode + dict + varint indices]
//   [incarnation column: varint per entry]
//   [slot column: 32B × entries]
//   [has-value bitpack: ceil(entries/8)]
//   [value column: per present entry: 1B len + trimmed bytes]

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

type StorageCSEntry struct {
	Address     types.Address
	Incarnation uint64
	Slot        types.Hash
	OldValue    []byte // nil = slot didn't exist
}

type StorageCSCompactor struct {
	db        kv.RoDB
	outputDir string
	tableName string
}

func NewStorageCSCompactor(db kv.RoDB, outputDir string) *StorageCSCompactor {
	return &StorageCSCompactor{db: db, outputDir: outputDir, tableName: ErigonStorageChangeSet}
}

func (c *StorageCSCompactor) SetTableName(name string) { c.tableName = name }

func (c *StorageCSCompactor) Run(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(c.outputDir, 0755); err != nil {
		return err
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	idxPath := filepath.Join(c.outputDir, "storcs.cidx")
	flags := os.O_RDWR | os.O_CREATE
	if startBlock == 0 {
		flags |= os.O_TRUNC
	}
	idxFile, err := os.OpenFile(idxPath, flags, 0644)
	if err != nil {
		return err
	}
	defer idxFile.Close()

	idxInfo, _ := idxFile.Stat()
	existingSegs := uint64(idxInfo.Size()) / 8
	resumeBlock := existingSegs * CSSegmentSize
	if resumeBlock > startBlock {
		startBlock = resumeBlock
	}
	idxFile.Seek(0, 2)

	var headFile uint16
	var headSize int64
	if existingSegs > 0 {
		var lastEntry [8]byte
		idxFile.ReadAt(lastEntry[:], int64(existingSegs-1)*8)
		headFile = binary.LittleEndian.Uint16(lastEntry[0:2])
		datPath := filepath.Join(c.outputDir, fmt.Sprintf("storcs.%04d.cdat", headFile))
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

		entries, entriesPerBlock, totalBytes, err := c.readSegment(segStart, segEnd)
		if err != nil {
			return fmt.Errorf("read segment %d-%d: %w", segStart, segEnd, err)
		}
		totalIn += totalBytes

		compressed := encodeStorageCSSegment(entries, entriesPerBlock, enc)

		segSize := int64(4 + len(compressed))
		if headSize+segSize > csMaxFileSize {
			headFile++
			headSize = 0
		}

		datPath := filepath.Join(c.outputDir, fmt.Sprintf("storcs.%04d.cdat", headFile))
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

		var idxEntry [8]byte
		binary.LittleEndian.PutUint16(idxEntry[0:2], headFile)
		binary.LittleEndian.PutUint32(idxEntry[4:8], uint32(headSize))
		idxFile.Write(idxEntry[:])

		headSize += segSize
		totalOut += segSize
		segCount++

		if segCount%500 == 0 || segStart+CSSegmentSize >= endBlock {
			elapsed := time.Since(t0)
			ratio := float64(0)
			if totalIn > 0 {
				ratio = float64(totalOut) / float64(totalIn) * 100
			}
			log.Info("StorageCS compact",
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

func (c *StorageCSCompactor) readSegment(startBlock, endBlock uint64) (
	entries []StorageCSEntry, entriesPerBlock []uint32, totalBytes int64, err error,
) {
	tx, err := c.db.BeginRo(context.Background())
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(c.tableName)
	if err != nil {
		return nil, nil, 0, err
	}
	defer cursor.Close()

	blockCount := endBlock - startBlock
	entriesPerBlock = make([]uint32, blockCount)

	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		return nil, nil, 0, err
	}

	for k != nil {
		if len(k) < 36 { // blockNum(8) + addr(20) + incarnation(8)
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

			var entry StorageCSEntry
			copy(entry.Address[:], k[8:28])
			entry.Incarnation = binary.BigEndian.Uint64(k[28:36])

			if len(v) >= 32 {
				copy(entry.Slot[:], v[:32])
				if len(v) > 32 {
					entry.OldValue = make([]byte, len(v)-32)
					copy(entry.OldValue, v[32:])
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

func encodeStorageCSSegment(entries []StorageCSEntry, entriesPerBlock []uint32, enc *zstd.Encoder) []byte {
	blockCount := len(entriesPerBlock)
	totalEntries := len(entries)

	buf := make([]byte, 0, totalEntries*40)

	// Block count.
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(blockCount))
	buf = append(buf, tmp[:]...)

	// Entries per block.
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
		buf = append(buf, 1)
		buf = appendVarint(buf, uint64(len(dict)))
		for _, a := range dict {
			buf = append(buf, a[:]...)
		}
		for _, e := range entries {
			buf = appendVarint(buf, uint64(addrMap[e.Address]))
		}
	} else {
		buf = append(buf, 0)
		for _, e := range entries {
			buf = append(buf, e.Address[:]...)
		}
	}

	// Incarnation column (varint — usually 1).
	for _, e := range entries {
		buf = appendVarint(buf, e.Incarnation)
	}

	// Slot column (32B each — let zstd find patterns).
	for _, e := range entries {
		buf = append(buf, e.Slot[:]...)
	}

	// Has-value bitpack (49% are false → good compression).
	hasValue := make([]bool, totalEntries)
	for i, e := range entries {
		hasValue[i] = len(e.OldValue) > 0
	}
	n := (totalEntries + 7) / 8
	packed := make([]byte, n)
	for i, has := range hasValue {
		if has {
			packed[i/8] |= 1 << (uint(i) % 8)
		}
	}
	buf = append(buf, packed...)

	// Value column (only for entries with has=true).
	for _, e := range entries {
		if len(e.OldValue) > 0 {
			buf = append(buf, byte(len(e.OldValue)))
			buf = append(buf, e.OldValue...)
		}
	}

	return enc.EncodeAll(buf, make([]byte, 0, len(buf)/2))
}

// DecodeStorageCSSegment decompresses and decodes a storage changeset segment.
func DecodeStorageCSSegment(compressed []byte, dec *zstd.Decoder) (
	[]StorageCSEntry, []uint32, error,
) {
	raw, err := dec.DecodeAll(compressed, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("zstd: %w", err)
	}
	if len(raw) < 4 {
		return nil, nil, fmt.Errorf("too short")
	}
	pos := 0

	blockCount := int(binary.LittleEndian.Uint32(raw[pos : pos+4]))
	pos += 4

	entriesPerBlock := make([]uint32, blockCount)
	totalEntries := 0
	for i := 0; i < blockCount; i++ {
		v, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, nil, fmt.Errorf("bad varint block %d", i)
		}
		pos += n
		entriesPerBlock[i] = uint32(v)
		totalEntries += int(v)
	}

	entries := make([]StorageCSEntry, totalEntries)

	// Address column.
	if pos >= len(raw) {
		return nil, nil, fmt.Errorf("truncated addr mode")
	}
	mode := raw[pos]
	pos++

	if mode == 1 {
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
				return nil, nil, fmt.Errorf("bad addr idx")
			}
			pos += n
			if idx >= dictLen {
				return nil, nil, fmt.Errorf("addr idx OOB")
			}
			entries[i].Address = dict[idx]
		}
	} else {
		for i := 0; i < totalEntries; i++ {
			if pos+20 > len(raw) {
				return nil, nil, fmt.Errorf("truncated addr")
			}
			copy(entries[i].Address[:], raw[pos:pos+20])
			pos += 20
		}
	}

	// Incarnation column.
	for i := 0; i < totalEntries; i++ {
		v, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, nil, fmt.Errorf("bad inc %d", i)
		}
		pos += n
		entries[i].Incarnation = v
	}

	// Slot column.
	for i := 0; i < totalEntries; i++ {
		if pos+32 > len(raw) {
			return nil, nil, fmt.Errorf("truncated slot %d", i)
		}
		copy(entries[i].Slot[:], raw[pos:pos+32])
		pos += 32
	}

	// Has-value bitpack.
	bpLen := (totalEntries + 7) / 8
	if pos+bpLen > len(raw) {
		return nil, nil, fmt.Errorf("truncated bitpack")
	}
	bitpack := raw[pos : pos+bpLen]
	pos += bpLen

	// Value column.
	for i := 0; i < totalEntries; i++ {
		has := bitpack[i/8]&(1<<(uint(i)%8)) != 0
		if has {
			if pos >= len(raw) {
				return nil, nil, fmt.Errorf("truncated val %d", i)
			}
			valLen := int(raw[pos])
			pos++
			if pos+valLen > len(raw) {
				return nil, nil, fmt.Errorf("truncated val data %d", i)
			}
			entries[i].OldValue = make([]byte, valLen)
			copy(entries[i].OldValue, raw[pos:pos+valLen])
			pos += valLen
		}
	}

	return entries, entriesPerBlock, nil
}
