// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_analysis.go analyzes history bitmap data patterns from Erigon MDBX.
// Key questions:
//   - What % of storage slots were modified only once? (single-write, never revisited)
//   - What's the distribution of modification counts per key?
//   - How much space is wasted on single-entry bitmaps?

package cscompact

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// HistoryAnalysis holds the results of history pattern analysis.
type HistoryAnalysis struct {
	TotalKeys       uint64
	TotalBitmapBits uint64 // total block numbers across all bitmaps
	TotalBytes      uint64

	// Modification count distribution.
	SingleWrite uint64 // keys modified exactly 1 time
	FewWrites   uint64 // 2-5 modifications
	MedWrites   uint64 // 6-20
	ManyWrites  uint64 // 21-100
	HotKeys     uint64 // 100+

	// Size distribution.
	SingleWriteBytes uint64 // bytes used by single-write entries
	FewWritesBytes   uint64
	HotKeysBytes     uint64

	// Top N hottest keys.
	TopKeys []HotKeyInfo
}

type HotKeyInfo struct {
	Key   []byte
	Count uint64
	Bytes uint64
}

// AnalyzeAccountHistory scans Erigon AccountHistory table.
// Erigon v2.7: key = addr(20B) + shardId(8B), value = roaring64 bitmap.
func AnalyzeAccountHistory(tx kv.Tx, sampleKeys uint64) (*HistoryAnalysis, error) {
	return analyzeHistory(tx, "AccountHistory", 20, sampleKeys)
}

// AnalyzeStorageHistory scans Erigon StorageHistory table.
// Erigon v2.7: key = addr(20B) + slot(32B) + shardId(8B), value = roaring64 bitmap.
func AnalyzeStorageHistory(tx kv.Tx, sampleKeys uint64) (*HistoryAnalysis, error) {
	return analyzeHistory(tx, "StorageHistory", 52, sampleKeys)
}

func analyzeHistory(tx kv.Tx, tableName string, prefixLen int, sampleKeys uint64) (*HistoryAnalysis, error) {
	cursor, err := tx.Cursor(tableName)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", tableName, err)
	}
	defer cursor.Close()

	result := &HistoryAnalysis{}

	// Erigon history format: key = addr(20B) [+ slot(32B)] + shardId(8B)
	// value = RoaringBitmap serialized
	// Multiple shards per key (shardId = chunk boundary).
	// We need to merge shards with same key prefix.

	type keyAgg struct {
		prefix    string
		totalBits uint64
		totalSize uint64
	}

	var current *keyAgg
	topHeap := make([]HotKeyInfo, 0, 20)

	processKey := func(ka *keyAgg) {
		if ka == nil {
			return
		}
		result.TotalKeys++
		result.TotalBitmapBits += ka.totalBits
		result.TotalBytes += ka.totalSize

		switch {
		case ka.totalBits == 1:
			result.SingleWrite++
			result.SingleWriteBytes += ka.totalSize
		case ka.totalBits <= 5:
			result.FewWrites++
			result.FewWritesBytes += ka.totalSize
		case ka.totalBits <= 20:
			result.MedWrites++
		case ka.totalBits <= 100:
			result.ManyWrites++
		default:
			result.HotKeys++
			result.HotKeysBytes += ka.totalSize
		}

		// Track top keys.
		if len(topHeap) < 20 || ka.totalBits > topHeap[len(topHeap)-1].Count {
			info := HotKeyInfo{
				Key:   []byte(ka.prefix),
				Count: ka.totalBits,
				Bytes: ka.totalSize,
			}
			if len(topHeap) < 20 {
				topHeap = append(topHeap, info)
			} else {
				// Replace smallest.
				minIdx := 0
				for i := 1; i < len(topHeap); i++ {
					if topHeap[i].Count < topHeap[minIdx].Count {
						minIdx = i
					}
				}
				if ka.totalBits > topHeap[minIdx].Count {
					topHeap[minIdx] = info
				}
			}
		}
	}

	var cursorEntries uint64
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		cursorEntries++

		// Extract key prefix (addr or addr+slot, without shardId suffix 8B).
		if len(k) < prefixLen+8 {
			continue
		}
		prefix := string(k[:prefixLen])

		if current == nil || current.prefix != prefix {
			processKey(current)
			current = &keyAgg{prefix: prefix}

			if sampleKeys > 0 && result.TotalKeys >= sampleKeys {
				break
			}
			// Also limit total cursor entries to avoid scanning millions of shards.
			if cursorEntries > 50_000_000 {
				break
			}
			if result.TotalKeys > 0 && result.TotalKeys%10000 == 0 {
				log.Info("  history scan progress",
					"keys", result.TotalKeys,
					"bytes", fmt.Sprintf("%.0f MiB", float64(result.TotalBytes)/1048576))
			}
		}

		// Erigon v2.7: value is roaring64.Bitmap serialized via WriteTo/ReadFrom.
		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err == nil {
			current.totalBits += bm.GetCardinality()
		} else {
			// Fallback: estimate from size.
			current.totalBits += uint64(len(v)) / 4
		}
		current.totalSize += uint64(len(k) + len(v))
	}
	processKey(current) // last key

	result.TopKeys = topHeap

	log.Info(fmt.Sprintf("%s analysis", tableName),
		"totalKeys", result.TotalKeys,
		"totalBits", result.TotalBitmapBits,
		"totalBytes", fmt.Sprintf("%.1f MiB", float64(result.TotalBytes)/1048576),
		"singleWrite", fmt.Sprintf("%d (%.1f%%)", result.SingleWrite,
			float64(result.SingleWrite)/float64(result.TotalKeys+1)*100),
		"fewWrites(2-5)", result.FewWrites,
		"medWrites(6-20)", result.MedWrites,
		"manyWrites(21-100)", result.ManyWrites,
		"hotKeys(100+)", result.HotKeys,
		"singleWriteBytes", fmt.Sprintf("%.1f MiB (%.1f%%)",
			float64(result.SingleWriteBytes)/1048576,
			float64(result.SingleWriteBytes)/float64(result.TotalBytes+1)*100))

	return result, nil
}

// byteReader wraps []byte for io.Reader interface needed by ReadFrom.
type byteReader []byte

func (b byteReader) Read(p []byte) (int, error) {
	n := copy(p, b)
	return n, nil
}

// Also try reading as Erigon's sharded format.
func (b byteReader) ReadByte() (byte, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("EOF")
	}
	return b[0], nil
}

// ParseErigonBitmapValue tries to count block numbers in an Erigon history value.
// Erigon uses different formats across versions:
//   - Roaring bitmap (most common)
//   - Raw sorted uint64 list
//   - Elias-Fano encoded
func ParseErigonBitmapValue(v []byte) uint64 {
	if len(v) == 0 {
		return 0
	}

	// Try Roaring64.
	bm := roaring64.New()
	if err := bm.UnmarshalBinary(v); err == nil {
		return bm.GetCardinality()
	}

	// Try as raw sorted uint32 list (Erigon v2 sharded format).
	if len(v)%4 == 0 {
		count := uint64(len(v)) / 4
		// Verify sorted.
		sorted := true
		for i := uint64(1); i < count; i++ {
			if binary.BigEndian.Uint32(v[i*4:]) <= binary.BigEndian.Uint32(v[(i-1)*4:]) {
				sorted = false
				break
			}
		}
		if sorted {
			return count
		}
	}

	// Fallback: estimate from size.
	return uint64(len(v)) / 4
}
