// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// history_builder.go builds RecSplit + adaptive-value history segments
// from Erigon MDBX history tables (READ-ONLY).

package cscompact

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/recsplit"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
)

type HistoryBuilder struct {
	db        kv.RoDB
	outputDir string
	tableName string // "AccountHistory" or "StorageHistory"
	prefix    string // "account_hist" or "storage_hist"
	keyLen    int    // 20 (account) or 52 (storage: addr+slot)
}

func NewAccountHistoryBuilder(db kv.RoDB, outputDir string) *HistoryBuilder {
	return &HistoryBuilder{
		db: db, outputDir: outputDir,
		tableName: "AccountHistory", prefix: "account_hist", keyLen: 20,
	}
}

func NewStorageHistoryBuilder(db kv.RoDB, outputDir string) *HistoryBuilder {
	return &HistoryBuilder{
		db: db, outputDir: outputDir,
		tableName: "StorageHistory", prefix: "storage_hist", keyLen: 52,
	}
}

func (b *HistoryBuilder) BuildRange(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(b.outputDir, 0755); err != nil {
		return err
	}

	for segStart := (startBlock / HistSegmentSize) * HistSegmentSize; segStart < endBlock; segStart += HistSegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		segEnd := segStart + HistSegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}

		baseName := HistSegmentFileName(b.prefix, segStart, segEnd)
		idxPath := filepath.Join(b.outputDir, baseName+".idx")
		datPath := filepath.Join(b.outputDir, baseName+".dat")

		if _, err := os.Stat(idxPath); err == nil {
			log.Info("History segment exists, skipping", "name", baseName)
			continue
		}

		if err := b.buildOne(ctx, segStart, segEnd, idxPath, datPath); err != nil {
			return fmt.Errorf("build %s: %w", baseName, err)
		}
	}
	return nil
}

// histKey + block list collected from Erigon history shards.
type histKeyData struct {
	key    []byte
	blocks []uint64 // sorted, deduplicated
}

func (b *HistoryBuilder) buildOne(ctx context.Context, startBlock, endBlock uint64, idxPath, datPath string) error {
	t0 := time.Now()

	// Step 1: Collect all unique keys and their block lists for this segment range.
	entries, totalShards, err := b.collectKeys(ctx, startBlock, endBlock)
	if err != nil {
		return err
	}

	log.Info("Building history segment",
		"table", b.tableName,
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"uniqueKeys", len(entries),
		"totalShards", totalShards)

	if len(entries) == 0 {
		return b.writeEmpty(idxPath, datPath)
	}

	// Step 2: Build RecSplit index.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           len(entries),
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		return err
	}

	for i, e := range entries {
		if err := rs.AddKey(e.key, uint64(i)); err != nil {
			return fmt.Errorf("addKey %d: %w", i, err)
		}
	}

	if err := rs.Build(ctx); err != nil {
		return fmt.Errorf("recsplit build: %w", err)
	}

	// Step 3: Write adaptive value dat file.
	if err := b.writeDat(datPath, entries); err != nil {
		os.Remove(idxPath)
		return err
	}

	elapsed := time.Since(t0)
	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)
	avgBytes := float64(idxFi.Size()+datFi.Size()) / float64(len(entries))
	log.Info("History segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", len(entries),
		"idx", fmt.Sprintf("%.1f KB", float64(idxFi.Size())/1024),
		"dat", fmt.Sprintf("%.1f KB", float64(datFi.Size())/1024),
		"avg", fmt.Sprintf("%.1f B/key", avgBytes),
		"elapsed", elapsed.Truncate(time.Second))

	return nil
}

func (b *HistoryBuilder) collectKeys(ctx context.Context, startBlock, endBlock uint64) ([]histKeyData, int, error) {
	tx, err := b.db.BeginRo(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(b.tableName)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close()

	// Map: key prefix → accumulated block numbers.
	type accum struct {
		key    []byte
		blocks []uint64
	}
	keyMap := make(map[string]*accum)
	totalShards := 0

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, 0, err
		}
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if len(k) < b.keyLen+8 {
			continue
		}

		prefix := string(k[:b.keyLen])

		// Decode Roaring bitmap to get block numbers.
		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}

		// Filter blocks in [startBlock, endBlock).
		var filtered []uint64
		it := bm.Iterator()
		for it.HasNext() {
			bn := it.Next()
			if bn >= startBlock && bn < endBlock {
				filtered = append(filtered, bn)
			}
		}

		if len(filtered) == 0 {
			totalShards++
			continue
		}

		a, ok := keyMap[prefix]
		if !ok {
			keyCopy := make([]byte, b.keyLen)
			copy(keyCopy, k[:b.keyLen])
			a = &accum{key: keyCopy}
			keyMap[prefix] = a
		}
		a.blocks = append(a.blocks, filtered...)
		totalShards++

		if totalShards%500000 == 0 {
			log.Info("  history collect progress",
				"shards", totalShards,
				"uniqueKeys", len(keyMap))
		}
	}

	// Sort and deduplicate block lists, sort keys.
	entries := make([]histKeyData, 0, len(keyMap))
	for _, a := range keyMap {
		sort.Slice(a.blocks, func(i, j int) bool { return a.blocks[i] < a.blocks[j] })
		// Deduplicate.
		deduped := a.blocks[:0]
		for i, b := range a.blocks {
			if i == 0 || b != a.blocks[i-1] {
				deduped = append(deduped, b)
			}
		}
		entries = append(entries, histKeyData{key: a.key, blocks: deduped})
	}

	// Sort entries by key for deterministic RecSplit ordering.
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})

	return entries, totalShards, nil
}

func (b *HistoryBuilder) writeDat(path string, entries []histKeyData) error {
	// Layout:
	// [4B magic][4B keyCount][4B flags]
	// [4B × keyCount: offset table]
	// [adaptive values...]

	keyCount := len(entries)
	headerSize := 12
	offsetTableSize := keyCount * 4
	dataStart := headerSize + offsetTableSize

	// First pass: calculate data sizes.
	var dataBuf []byte
	offsets := make([]uint32, keyCount)

	for i, e := range entries {
		offsets[i] = uint32(dataStart + len(dataBuf))

		count := len(e.blocks)
		if count <= 254 {
			dataBuf = append(dataBuf, byte(count))
		} else {
			dataBuf = append(dataBuf, 0xFF)
			var cb [4]byte
			binary.LittleEndian.PutUint32(cb[:], uint32(count))
			dataBuf = append(dataBuf, cb[:]...)
		}

		// Delta-encode block numbers.
		prev := uint64(0)
		for _, bn := range e.blocks {
			delta := bn - prev
			dataBuf = appendVarint(dataBuf, delta)
			prev = bn
		}
	}

	// Write file.
	totalSize := dataStart + len(dataBuf)
	buf := make([]byte, totalSize)

	// Header.
	copy(buf[:4], histDatMagic)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(keyCount))
	binary.LittleEndian.PutUint32(buf[8:12], 0) // flags

	// Offset table.
	for i, off := range offsets {
		binary.LittleEndian.PutUint32(buf[headerSize+i*4:], off)
	}

	// Data.
	copy(buf[dataStart:], dataBuf)

	// zstd compress entire dat.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	compressed := enc.EncodeAll(buf, nil)
	enc.Close()

	// Only use compressed if smaller.
	if len(compressed) < len(buf) {
		return os.WriteFile(path, compressed, 0644)
	}
	return os.WriteFile(path, buf, 0644)
}

func (b *HistoryBuilder) writeEmpty(idxPath, datPath string) error {
	// Empty dat.
	buf := make([]byte, 12)
	copy(buf[:4], histDatMagic)
	// keyCount=0, flags=0
	if err := os.WriteFile(datPath, buf, 0644); err != nil {
		return err
	}

	// Empty RecSplit.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:  0,
		IndexFile: idxPath,
		TmpDir:    os.TempDir(),
	}, logger)
	if err != nil {
		return err
	}
	return rs.Build(context.Background())
}

// BuildFromChangesets builds history segments by reading Erigon changeset tables
// (AccountChangeSet/StorageChangeSet) sequentially by block. 10-14x faster than
// BuildRange which scans the entire history bitmap table.
func (b *HistoryBuilder) BuildFromChangesets(ctx context.Context, startBlock, endBlock uint64) error {
	if err := os.MkdirAll(b.outputDir, 0755); err != nil {
		return err
	}

	csTable := "AccountChangeSet"
	if b.keyLen == 52 {
		csTable = "StorageChangeSet"
	}

	for segStart := (startBlock / HistSegmentSize) * HistSegmentSize; segStart < endBlock; segStart += HistSegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		segEnd := segStart + HistSegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}

		baseName := HistSegmentFileName(b.prefix, segStart, segEnd)
		idxPath := filepath.Join(b.outputDir, baseName+".idx")
		datPath := filepath.Join(b.outputDir, baseName+".dat")

		if _, err := os.Stat(idxPath); err == nil {
			log.Info("History segment exists, skipping", "name", baseName)
			continue
		}

		entries, err := b.collectFromChangesets(ctx, csTable, segStart, segEnd)
		if err != nil {
			return fmt.Errorf("collect %s: %w", baseName, err)
		}

		if err := b.buildFromKeyMap(ctx, entries, idxPath, datPath, segStart, segEnd); err != nil {
			return fmt.Errorf("build %s: %w", baseName, err)
		}
	}
	return nil
}

// collectFromChangesets reads changeset table sequentially by block.
func (b *HistoryBuilder) collectFromChangesets(ctx context.Context, csTable string, startBlock, endBlock uint64) ([]histKeyData, error) {
	tx, err := b.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor(csTable)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	keyMap := make(map[string][]uint64)

	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], startBlock)
	k, v, err := cursor.Seek(seekKey[:])
	if err != nil {
		return nil, err
	}

	var totalEntries uint64
	for k != nil {
		if len(k) < 8 {
			k, v, err = cursor.Next()
			if err != nil {
				return nil, err
			}
			continue
		}

		blockNum := binary.BigEndian.Uint64(k[:8])
		if blockNum >= endBlock {
			break
		}

		if blockNum >= startBlock {
			if b.keyLen == 20 {
				// AccountChangeSet: key=blockNum(8), value=addr(20)+oldValue
				if len(v) >= 20 {
					mapKey := string(v[:20])
					keyMap[mapKey] = append(keyMap[mapKey], blockNum)
				}
			} else {
				// StorageChangeSet: key=blockNum(8)+addr(20)+incarnation(8), value=slot(32)+oldValue
				if len(k) >= 36 && len(v) >= 32 {
					var compositeKey [52]byte
					copy(compositeKey[:20], k[8:28]) // addr
					copy(compositeKey[20:], v[:32])   // slot
					mapKey := string(compositeKey[:])
					keyMap[mapKey] = append(keyMap[mapKey], blockNum)
				}
			}
			totalEntries++
		}

		k, v, err = cursor.Next()
		if err != nil {
			return nil, err
		}

		if totalEntries%5_000_000 == 0 && totalEntries > 0 {
			log.Info("  changeset scan progress",
				"entries", totalEntries,
				"uniqueKeys", len(keyMap),
				"block", blockNum)
		}
	}

	// Sort and deduplicate.
	entries := make([]histKeyData, 0, len(keyMap))
	for mapKey, blocks := range keyMap {
		sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })
		deduped := blocks[:0]
		for i, b := range blocks {
			if i == 0 || b != blocks[i-1] {
				deduped = append(deduped, b)
			}
		}
		entries = append(entries, histKeyData{key: []byte(mapKey), blocks: deduped})
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].key, entries[j].key) < 0
	})

	log.Info("Changeset scan complete",
		"entries", totalEntries,
		"uniqueKeys", len(entries),
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1))

	return entries, nil
}

// BuildFromKeyMap builds a single segment from pre-collected key data.
// Reusable by both BuildFromChangesets and future exec integration.
func (b *HistoryBuilder) buildFromKeyMap(ctx context.Context, entries []histKeyData, idxPath, datPath string, startBlock, endBlock uint64) error {
	t0 := time.Now()

	if len(entries) == 0 {
		return b.writeEmpty(idxPath, datPath)
	}

	log.Info("Building history segment",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", len(entries))

	// Build RecSplit.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           len(entries),
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if err := rs.AddKey(e.key, uint64(i)); err != nil {
			return err
		}
	}
	if err := rs.Build(ctx); err != nil {
		return err
	}

	// Write dat.
	if err := b.writeDat(datPath, entries); err != nil {
		os.Remove(idxPath)
		return err
	}

	elapsed := time.Since(t0)
	idxFi, _ := os.Stat(idxPath)
	datFi, _ := os.Stat(datPath)
	avgBytes := float64(idxFi.Size()+datFi.Size()) / float64(len(entries))
	log.Info("History segment built",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"keys", len(entries),
		"idx", fmt.Sprintf("%.1f KB", float64(idxFi.Size())/1024),
		"dat", fmt.Sprintf("%.1f KB", float64(datFi.Size())/1024),
		"avg", fmt.Sprintf("%.1f B/key", avgBytes),
		"elapsed", elapsed.Truncate(time.Second))

	return nil
}
