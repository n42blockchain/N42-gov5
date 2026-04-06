// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// erigon_v3.go — compress changesets using Erigon V3's exact algorithm
// (suffix-array dictionary + Huffman per-word). Uses lib/seg/ directly.
//
// Each changeset entry is stored as one "word" in the seg file:
//   AccountCS word = addr(20B) + oldValue(variable)
//   StorageCS word = addr(20B) + incarnation(8B) + slot(32B) + oldValue(variable)
//
// This enables per-word random access decompression via mmap.

package cscompact

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/seg"
	"github.com/n42blockchain/N42/log"
)

// ErigonV3Compactor compresses changesets using Erigon V3's seg library.
type ErigonV3Compactor struct {
	db        kv.RoDB
	outputDir string
}

func NewErigonV3Compactor(db kv.RoDB, outputDir string) *ErigonV3Compactor {
	return &ErigonV3Compactor{db: db, outputDir: outputDir}
}

// CompressAccountCS compresses AccountChangeSet entries for [startBlock, endBlock).
// Output: one .seg file with per-word compression.
// Each word = addr(20B) + oldValue(variable). Block boundaries stored in a
// separate sidecar file (.blocks).
func (c *ErigonV3Compactor) CompressAccountCS(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(c.outputDir, 0755); err != nil {
		return err
	}

	baseName := fmt.Sprintf("account_cs_v3_%d_%d", startBlock, endBlock)
	segFile := filepath.Join(c.outputDir, baseName+".seg")
	blocksFile := filepath.Join(c.outputDir, baseName+".blocks")

	if _, err := os.Stat(segFile); err == nil {
		log.Info("V3 segment exists, skipping", "file", baseName)
		return nil
	}

	t0 := time.Now()
	logger := log2.New()

	// Create compressor.
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4 // seg compressor is memory-heavy
	}
	comp, err := seg.NewCompressor(ctx, "accountCS", segFile, os.TempDir(),
		1024, // minPatternScore
		workers, log2.LvlInfo, logger)
	if err != nil {
		return fmt.Errorf("new compressor: %w", err)
	}

	// Read entries from Erigon MDBX and add as words.
	tx, err := c.db.BeginRo(ctx)
	if err != nil {
		comp.Close()
		return err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(ErigonAccountChangeSet)
	if err != nil {
		comp.Close()
		return err
	}
	defer cursor.Close()

	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		comp.Close()
		return err
	}

	var totalEntries, totalRawBytes uint64
	var lastBlock uint64 = startBlock
	blockEntryCounts := make([]uint32, endBlock-startBlock)

	for k != nil {
		if len(k) < 8 {
			k, v, err = cursor.Next()
			if err != nil {
				comp.Close()
				return err
			}
			continue
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= endBlock {
			break
		}

		if blockNum >= startBlock {
			// Word = full value (addr + oldValue) — same as Erigon's approach.
			if err := comp.AddWord(v); err != nil {
				comp.Close()
				return err
			}

			totalEntries++
			totalRawBytes += uint64(len(v))
			blockEntryCounts[blockNum-startBlock]++
			lastBlock = blockNum
		}

		k, v, err = cursor.Next()
		if err != nil {
			comp.Close()
			return err
		}

		if totalEntries%1_000_000 == 0 && totalEntries > 0 {
			log.Info("V3 compress scan",
				"block", lastBlock,
				"entries", totalEntries,
				"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6))
		}
	}

	log.Info("V3 compressing...",
		"entries", totalEntries,
		"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6))

	// Build dictionary + Huffman + compress.
	if err := comp.Compress(); err != nil {
		comp.Close()
		return fmt.Errorf("compress: %w", err)
	}
	comp.Close()

	// Write block boundaries sidecar.
	blocksBuf := make([]byte, len(blockEntryCounts)*4)
	for i, cnt := range blockEntryCounts {
		binary.LittleEndian.PutUint32(blocksBuf[i*4:], cnt)
	}
	if err := os.WriteFile(blocksFile, blocksBuf, 0644); err != nil {
		return err
	}

	// Report.
	fi, _ := os.Stat(segFile)
	elapsed := time.Since(t0)
	ratio := float64(fi.Size()) / float64(totalRawBytes) * 100
	log.Info("V3 AccountCS complete",
		"entries", totalEntries,
		"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6),
		"compressed", fmt.Sprintf("%.1f MB", float64(fi.Size())/1e6),
		"ratio", fmt.Sprintf("%.1f%%", ratio),
		"elapsed", elapsed.Truncate(time.Second))

	return nil
}

// CompressStorageCS compresses StorageChangeSet entries for [startBlock, endBlock).
// Each word = storageKey(32B) + oldValue(variable).
// Address prefix is stored as a separate uncompressed word before each group.
func (c *ErigonV3Compactor) CompressStorageCS(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(c.outputDir, 0755); err != nil {
		return err
	}

	baseName := fmt.Sprintf("storage_cs_v3_%d_%d", startBlock, endBlock)
	segFile := filepath.Join(c.outputDir, baseName+".seg")

	if _, err := os.Stat(segFile); err == nil {
		log.Info("V3 storage segment exists, skipping", "file", baseName)
		return nil
	}

	t0 := time.Now()
	logger := log2.New()

	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	comp, err := seg.NewCompressor(ctx, "storageCS", segFile, os.TempDir(),
		1024, workers, log2.LvlInfo, logger)
	if err != nil {
		return fmt.Errorf("new compressor: %w", err)
	}

	tx, err := c.db.BeginRo(ctx)
	if err != nil {
		comp.Close()
		return err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(ErigonStorageChangeSet)
	if err != nil {
		comp.Close()
		return err
	}
	defer cursor.Close()

	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		comp.Close()
		return err
	}

	var totalEntries, totalRawBytes uint64

	for k != nil {
		if len(k) < 8 {
			k, v, err = cursor.Next()
			if err != nil {
				comp.Close()
				return err
			}
			continue
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= endBlock {
			break
		}

		if blockNum >= startBlock {
			// Erigon StorageCS: key = blockNum(8) + addr(20) + incarnation(8)
			// value = storageKey(32) + oldValue
			// Word = addr(20) + incarnation(8) + value (key prefix from MDBX key)
			word := make([]byte, 0, len(k)-8+len(v))
			word = append(word, k[8:]...)  // addr + incarnation
			word = append(word, v...)       // storageKey + oldValue

			if err := comp.AddWord(word); err != nil {
				comp.Close()
				return err
			}

			totalEntries++
			totalRawBytes += uint64(len(k) + len(v))
		}

		k, v, err = cursor.Next()
		if err != nil {
			comp.Close()
			return err
		}

		if totalEntries%5_000_000 == 0 && totalEntries > 0 {
			log.Info("V3 storage scan",
				"entries", totalEntries,
				"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6))
		}
	}

	log.Info("V3 storage compressing...",
		"entries", totalEntries,
		"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6))

	if err := comp.Compress(); err != nil {
		comp.Close()
		return fmt.Errorf("compress: %w", err)
	}
	comp.Close()

	fi, _ := os.Stat(segFile)
	elapsed := time.Since(t0)
	ratio := float64(fi.Size()) / float64(totalRawBytes) * 100
	log.Info("V3 StorageCS complete",
		"entries", totalEntries,
		"raw", fmt.Sprintf("%.1f MB", float64(totalRawBytes)/1e6),
		"compressed", fmt.Sprintf("%.1f MB", float64(fi.Size())/1e6),
		"ratio", fmt.Sprintf("%.1f%%", ratio),
		"elapsed", elapsed.Truncate(time.Second))

	return nil
}

// VerifyAccountCS reads back the compressed seg file and verifies every
// word matches the original Erigon MDBX data.
func VerifyAccountCS(ctx context.Context, db kv.RoDB, segFile string, startBlock, endBlock uint64) error {
	d, err := seg.NewDecompressor(segFile)
	if err != nil {
		return fmt.Errorf("open decompressor: %w", err)
	}
	defer d.Close()

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(ErigonAccountChangeSet)
	if err != nil {
		return err
	}
	defer cursor.Close()

	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		return err
	}

	g := d.MakeGetter()
	var verified, mismatches uint64

	for g.HasNext() {
		word, _ := g.Next(nil)

		// Find matching entry in Erigon.
		for k != nil && len(k) >= 8 {
			blockNum := binary.BigEndian.Uint64(k[:8])
			if blockNum >= startBlock {
				break
			}
			k, v, err = cursor.Next()
			if err != nil {
				return err
			}
		}

		if k == nil {
			return fmt.Errorf("seg has more entries than MDBX at entry %d", verified)
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= endBlock {
			return fmt.Errorf("seg entry %d: block %d beyond endBlock %d", verified, blockNum, endBlock)
		}

		// Compare word with original value.
		if len(word) != len(v) {
			mismatches++
			if mismatches <= 5 {
				log.Error("V3 verify mismatch", "entry", verified, "block", blockNum,
					"segLen", len(word), "mdbxLen", len(v))
			}
		} else {
			match := true
			for i := range word {
				if word[i] != v[i] {
					match = false
					break
				}
			}
			if !match {
				mismatches++
				if mismatches <= 5 {
					log.Error("V3 verify content mismatch", "entry", verified, "block", blockNum)
				}
			}
		}

		verified++
		k, v, err = cursor.Next()
		if err != nil {
			return err
		}

		if verified%1_000_000 == 0 {
			log.Info("V3 verify progress", "verified", verified, "mismatches", mismatches)
		}
	}

	if mismatches > 0 {
		return fmt.Errorf("%d mismatches in %d entries", mismatches, verified)
	}
	log.Info("V3 verify complete", "entries", verified, "mismatches", 0)
	return nil
}
