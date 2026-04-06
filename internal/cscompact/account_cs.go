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
	// CSSegmentSize is the number of blocks per changeset segment.
	CSSegmentSize = 1_000_000
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
