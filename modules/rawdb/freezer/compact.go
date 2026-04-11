// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/log"
)

// CompactAll rewrites all output freezer tables from srcDir to dstDir with
// batch compression. Tables are processed in parallel. The destination gets
// fresh cidx/cdat files with sequential numbering starting from 0000.
func CompactAll(srcDir, dstDir string) error {
	tables := []string{
		TableReceipts,
		TableSenders,
		TableAccountChanges,
		TableStorageChanges,
		TableLeavesJournal,
		TableBlockWitness,
	}

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	var wg sync.WaitGroup
	errs := make([]error, len(tables))

	for i, table := range tables {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			errs[idx] = CompactTable(srcDir, dstDir, name, "c")
		}(i, table)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("compact %s: %w", tables[i], err)
		}
	}
	return nil
}

// CompactTable reads a single table from srcDir, batch-compresses with
// SpeedBestCompression, and writes to dstDir with fresh file numbering.
// Always starts fresh — any existing destination files for this table are removed.
func CompactTable(srcDir, dstDir, name, ext string) error {
	src, err := NewFreezerTableCompressed(srcDir, name, ext)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer src.Close()

	totalItems := src.Items()
	if totalItems == 0 {
		log.Info("Compact: empty table, skipping", "table", name)
		return nil
	}

	batchSize := BatchSize

	// Always start fresh. Clean any existing dst files for this table.
	// Continue past gaps (a missing file doesn't mean later ones are absent).
	dstCidxPath := filepath.Join(dstDir, fmt.Sprintf("%s.%sidx", name, ext))
	os.Remove(dstCidxPath)
	consecutiveMissing := 0
	for i := 0; consecutiveMissing < 3; i++ {
		p := filepath.Join(dstDir, fmt.Sprintf("%s.%04d.%sdat", name, i, ext))
		if err := os.Remove(p); os.IsNotExist(err) {
			consecutiveMissing++
		} else {
			consecutiveMissing = 0
		}
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	cidxFile, err := os.Create(dstCidxPath)
	if err != nil {
		return err
	}
	defer cidxFile.Close()
	cidxBuf := bufio.NewWriterSize(cidxFile, 256*1024)
	defer cidxBuf.Flush()

	var (
		fileNum    uint16
		fileOffset uint32
		datFile    *os.File
		datBuf     *bufio.Writer
		totalRaw   int64
		totalComp  int64
	)
	defer func() {
		if datBuf != nil {
			datBuf.Flush()
		}
		if datFile != nil {
			datFile.Close()
		}
	}()

	openDataFile := func() error {
		if datBuf != nil {
			datBuf.Flush()
		}
		if datFile != nil {
			datFile.Close()
		}
		p := filepath.Join(dstDir, fmt.Sprintf("%s.%04d.%sdat", name, fileNum, ext))
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		fileOffset = 0
		datFile = f
		datBuf = bufio.NewWriterSize(f, 256*1024)
		return nil
	}

	if err := openDataFile(); err != nil {
		return err
	}

	for batchStart := uint64(0); batchStart < totalItems; batchStart += uint64(batchSize) {
		batchEnd := batchStart + uint64(batchSize)
		if batchEnd > totalItems {
			batchEnd = totalItems
		}

		// Read entries.
		rawSize := 0
		entries := make([][]byte, 0, batchEnd-batchStart)
		for item := batchStart; item < batchEnd; item++ {
			data, err := src.Retrieve(item)
			if err != nil {
				return fmt.Errorf("retrieve item %d: %w", item, err)
			}
			entries = append(entries, data)
			rawSize += len(data)
		}

		out := EncodeBatch(entries, enc)

		totalRaw += int64(rawSize + len(entries)*4)
		totalComp += int64(len(out))

		// Rotate data file if current would exceed maxFileSize.
		if fileOffset > 0 && int64(fileOffset)+int64(len(out)) > maxFileSize {
			fileNum++
			if err := openDataFile(); err != nil {
				return err
			}
		}

		// Write cidx: all entries in batch share same offset.
		for item := batchStart; item < batchEnd; item++ {
			idx := encodeIndex(indexEntry{fileNum: fileNum, offset: fileOffset})
			if _, err := cidxBuf.Write(idx); err != nil {
				return err
			}
		}

		// Write data.
		if _, err := datBuf.Write(out); err != nil {
			return err
		}
		fileOffset += uint32(len(out))

		if batchStart > 0 && batchStart%100000 == 0 {
			pct := float64(batchStart) / float64(totalItems) * 100
			log.Info("Compact progress", "table", name,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"items", fmt.Sprintf("%d/%d", batchStart, totalItems))
		}
	}

	// Flush before verify.
	if err := cidxBuf.Flush(); err != nil {
		return fmt.Errorf("flush cidx: %w", err)
	}
	if datBuf != nil {
		if err := datBuf.Flush(); err != nil {
			return fmt.Errorf("flush data: %w", err)
		}
	}

	ratio := float64(1)
	if totalComp > 0 {
		ratio = float64(totalRaw) / float64(totalComp)
	}
	log.Info("Compact done", "table", name, "items", totalItems,
		"batch", batchSize, "files", fileNum+1,
		"rawMB", totalRaw/1024/1024, "compMB", totalComp/1024/1024,
		"ratio", fmt.Sprintf("%.2fx", ratio))

	// Verify: sample 1000 random items, compare src vs dst.
	if err := verifyCompact(src, dstDir, name, ext, totalItems); err != nil {
		return fmt.Errorf("verify %s: %w", name, err)
	}
	return nil
}

// verifyCompact spot-checks that destination entries match source entries.
func verifyCompact(src *FreezerTable, dstDir, name, ext string, totalItems uint64) error {
	dst, err := NewFreezerTableCompressed(dstDir, name, ext)
	if err != nil {
		return fmt.Errorf("open dst for verify: %w", err)
	}
	defer dst.Close()

	if dst.Items() != totalItems {
		return fmt.Errorf("item count mismatch: src=%d dst=%d", totalItems, dst.Items())
	}

	// Sample 1000 items: first 10, last 10, 980 random.
	const sampleN = 1000
	samples := make(map[uint64]bool)
	for i := uint64(0); i < 10 && i < totalItems; i++ {
		samples[i] = true
	}
	// Guard against uint64 underflow when totalItems < 10.
	if totalItems > 10 {
		for i := totalItems - 10; i < totalItems; i++ {
			samples[i] = true
		}
	}
	for len(samples) < sampleN && len(samples) < int(totalItems) {
		samples[uint64(rand.Int63n(int64(totalItems)))] = true
	}

	checked := 0
	for item := range samples {
		srcData, err := src.Retrieve(item)
		if err != nil {
			return fmt.Errorf("src retrieve %d: %w", item, err)
		}
		dstData, err := dst.Retrieve(item)
		if err != nil {
			return fmt.Errorf("dst retrieve %d: %w", item, err)
		}
		if !bytes.Equal(srcData, dstData) {
			return fmt.Errorf("item %d: content mismatch (src=%d bytes, dst=%d bytes)", item, len(srcData), len(dstData))
		}
		checked++
	}

	log.Info("Compact verified", "table", name,
		"checked", checked, "total", totalItems)
	return nil
}
