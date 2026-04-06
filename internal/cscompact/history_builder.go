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
	"time"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/recsplit"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
)

type HistoryBuilder struct {
	db        kv.RoDB
	outputDir string
	tableName string // "AccountsHistory" or "StoragesHistory"
	prefix    string // "accthist" or "storhist"
	keyLen    int    // 20 (account) or 52 (storage: addr+slot)
}

func NewAccountHistoryBuilder(db kv.RoDB, outputDir string) *HistoryBuilder {
	return &HistoryBuilder{
		db: db, outputDir: outputDir,
		tableName: "AccountsHistory", prefix: "accthist", keyLen: 20,
	}
}

func NewStorageHistoryBuilder(db kv.RoDB, outputDir string) *HistoryBuilder {
	return &HistoryBuilder{
		db: db, outputDir: outputDir,
		tableName: "StoragesHistory", prefix: "storhist", keyLen: 52,
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
	entries, totalShards, err := b.collectKeys(ctx, startBlock, endBlock)
	if err != nil {
		return err
	}
	log.Info("Collected from history bitmaps",
		"table", b.tableName,
		"uniqueKeys", len(entries),
		"totalShards", totalShards)
	return b.buildFromKeyMap(ctx, entries, idxPath, datPath, startBlock, endBlock)
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

	keyMap := make(map[string][]uint64)
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

		keyMap[prefix] = append(keyMap[prefix], filtered...)
		totalShards++

		if totalShards%500000 == 0 {
			log.Info("  history collect progress",
				"shards", totalShards,
				"uniqueKeys", len(keyMap))
		}
	}

	entries := sortAndDedup(keyMap)
	return entries, totalShards, nil
}

func (b *HistoryBuilder) writeDat(path string, entries []histKeyData) error {
	data, err := b.buildDatBytes(entries)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
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
// (AccountChangeSet/StorageChangeSet) sequentially by block. 280x faster than
// BuildRange which scans the entire history bitmap table.
// Uses freezer-style file management via SegmentStoreWriter.
func (b *HistoryBuilder) BuildFromChangesets(ctx context.Context, startBlock, endBlock uint64) error {
	store, err := NewSegmentStoreWriter(b.outputDir, b.prefix)
	if err != nil {
		return err
	}
	defer store.Close()

	// Resume from existing segments.
	existingSegs := store.SegmentCount()
	resumeBlock := existingSegs * HistSegmentSize
	if resumeBlock > startBlock {
		startBlock = resumeBlock
		log.Info("Resuming history build", "from", startBlock, "existingSegments", existingSegs)
	}

	csTable := b.detectCSTable(ctx)

	for segStart := (startBlock / HistSegmentSize) * HistSegmentSize; segStart < endBlock; segStart += HistSegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		segEnd := segStart + HistSegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}

		entries, err := b.collectFromChangesets(ctx, csTable, segStart, segEnd)
		if err != nil {
			return fmt.Errorf("collect seg %d: %w", segStart, err)
		}

		// Build RecSplit to temp file with unique name to avoid Windows lock conflicts.
		tmpIdx := filepath.Join(b.outputDir, fmt.Sprintf("tmp_%s_%d.ri", b.prefix, segStart))
		datBuf, err := b.buildSegment(ctx, entries, tmpIdx, segStart, segEnd)
		if err != nil {
			os.Remove(tmpIdx)
			return err
		}

		segNum, err := store.WriteSegment(datBuf, tmpIdx)
		if err != nil {
			return err
		}
		_ = segNum
	}
	return nil
}

// buildSegment builds RecSplit + dat bytes for one segment.
// Returns the dat bytes (zstd compressed) and writes RecSplit to idxPath.
func (b *HistoryBuilder) buildSegment(ctx context.Context, entries []histKeyData, idxPath string, startBlock, endBlock uint64) ([]byte, error) {
	t0 := time.Now()

	datBytes, err := buildHistSegment(entries, idxPath, startBlock, endBlock)
	if err != nil {
		return nil, err
	}

	if len(entries) > 0 {
		elapsed := time.Since(t0)
		log.Info("History segment built",
			"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
			"keys", len(entries),
			"dat", fmt.Sprintf("%.1f KB", float64(len(datBytes))/1024),
			"avg", fmt.Sprintf("%.1f B/key", float64(len(datBytes))/float64(len(entries))),
			"elapsed", elapsed.Truncate(time.Second))
	}

	return datBytes, nil
}

// buildDatBytes creates the zstd-compressed dat content.
func (b *HistoryBuilder) buildDatBytes(entries []histKeyData) ([]byte, error) {
	return buildHistDatBytes(entries)
}

// collectFromChangesets reads changeset table sequentially by block.
// detectCSTable probes the MDBX for changeset table names.
// Returns the correct table name (Erigon or Reth) and whether it uses LE byte order.
func (b *HistoryBuilder) detectCSTable(ctx context.Context) string {
	// Try Erigon names first (no 's'), then Reth names (with 's').
	candidates := []string{"AccountChangeSet", "AccountChangeSets"}
	if b.keyLen == 52 {
		candidates = []string{"StorageChangeSet", "StorageChangeSets"}
	}
	tx, err := b.db.BeginRo(ctx)
	if err != nil {
		return candidates[0]
	}
	defer tx.Rollback()
	for _, tbl := range candidates {
		cursor, err := tx.Cursor(tbl)
		if err != nil {
			continue
		}
		k, _, _ := cursor.First()
		cursor.Close()
		if k != nil && len(k) >= 8 {
			log.Info("Detected changeset table", "table", tbl, "keyLen", len(k))
			return tbl
		}
	}
	return candidates[0]
}

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
				// StorageChangeSet:
				//   Erigon: key=blockNum(8)+addr(20)+incarnation(8)=36B, value=slot(32)+oldValue
				//   Reth:   key=blockNum(8)+addr(20)=28B, value=slot(32)+oldValue
				if len(k) >= 28 && len(v) >= 32 {
					var compositeKey [52]byte
					copy(compositeKey[:20], k[8:28]) // addr (same offset for both)
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

	entries := sortAndDedup(keyMap)

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
