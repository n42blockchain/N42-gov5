// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reth_builder.go builds txindex RecSplit segments from Reth MDBX tables.
// Uses TransactionHashNumbers (txHash→txNum) + TransactionBlocks (txNum→blockNum).

package txlookup

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/recsplit"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
)

// RethBuilder builds txindex segments from Reth MDBX.
type RethBuilder struct {
	db        kv.RoDB
	outputDir string
}

func NewRethBuilder(db kv.RoDB, outputDir string) *RethBuilder {
	return &RethBuilder{db: db, outputDir: outputDir}
}

// BuildRange builds txindex segments from Reth TransactionHashNumbers table.
func (b *RethBuilder) BuildRange(ctx context.Context, startBlock, endBlock uint64) error {
	store, err := cscompact.NewSegmentStoreWriter(b.outputDir, "txindex")
	if err != nil {
		return err
	}
	defer store.Close()

	existingSegs := store.SegmentCount()
	resumeBlock := existingSegs * SegmentSize
	if resumeBlock > startBlock {
		startBlock = resumeBlock
		log.Info("Resuming txindex build from Reth", "from", startBlock, "segments", existingSegs)
	}

	// Load TransactionBlocks index (txNum → blockNum, sparse).
	txnBlocks, err := b.loadTxnBlocks(ctx)
	if err != nil {
		return fmt.Errorf("load TransactionBlocks: %w", err)
	}
	log.Info("TransactionBlocks loaded", "entries", len(txnBlocks))

	for segStart := (startBlock / SegmentSize) * SegmentSize; segStart < endBlock; segStart += SegmentSize {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		segEnd := segStart + SegmentSize
		if segEnd > endBlock {
			segEnd = endBlock
		}

		tmpIdx := filepath.Join(b.outputDir, fmt.Sprintf("tmp_txindex_%d.ri", segStart))
		datBytes, err := b.buildOneFromReth(ctx, segStart, segEnd, tmpIdx, txnBlocks)
		if err != nil {
			os.Remove(tmpIdx)
			return fmt.Errorf("build segment %d-%d: %w", segStart, segEnd, err)
		}

		if _, err := store.WriteSegment(datBytes, tmpIdx); err != nil {
			return err
		}
	}
	return nil
}

// txnBlockEntry is a sorted entry for binary search: txNum → blockNum.
type txnBlockEntry struct {
	txNum    uint64
	blockNum uint64
}

// loadTxnBlocks reads the TransactionBlocks table into a sorted slice.
func (b *RethBuilder) loadTxnBlocks(ctx context.Context) ([]txnBlockEntry, error) {
	tx, err := b.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor("TransactionBlocks")
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var entries []txnBlockEntry
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 8 || len(v) < 8 {
			continue
		}
		txNum := binary.LittleEndian.Uint64(k)
		blockNum := binary.LittleEndian.Uint64(v)
		entries = append(entries, txnBlockEntry{txNum: txNum, blockNum: blockNum})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].txNum < entries[j].txNum
	})
	return entries, nil
}

// txNumToBlockNum does binary search to find blockNum for a given txNum.
func txNumToBlockNum(txnBlocks []txnBlockEntry, txNum uint64) uint64 {
	idx := sort.Search(len(txnBlocks), func(i int) bool {
		return txnBlocks[i].txNum > txNum
	}) - 1
	if idx < 0 {
		return 0
	}
	return txnBlocks[idx].blockNum
}

func (b *RethBuilder) buildOneFromReth(ctx context.Context, startBlock, endBlock uint64, idxPath string, txnBlocks []txnBlockEntry) ([]byte, error) {
	t0 := time.Now()
	blockCount := endBlock - startBlock

	tx, err := b.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor("TransactionHashNumbers")
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	// Collect all txHash→blockNum pairs in the block range.
	type txEntry struct {
		hash     [32]byte
		blockNum uint64
	}

	var txEntries []txEntry
	txPerBlock := make(map[uint64]uint32)

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(k) != 32 || len(v) < 8 {
			continue
		}
		txNum := binary.LittleEndian.Uint64(v)
		blockNum := txNumToBlockNum(txnBlocks, txNum)
		if blockNum < startBlock || blockNum >= endBlock {
			continue
		}
		var hash [32]byte
		copy(hash[:], k)
		txEntries = append(txEntries, txEntry{hash: hash, blockNum: blockNum})
		txPerBlock[blockNum]++

		if len(txEntries)%10_000_000 == 0 {
			log.Info("  txindex scan progress",
				"txs", len(txEntries),
				"elapsed", time.Since(t0).Truncate(time.Second))
		}
	}

	totalTx := len(txEntries)
	if totalTx == 0 {
		log.Info("Empty segment (no transactions)", "blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1))
		logger := log2.New()
		rs, _ := recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount: 0, IndexFile: idxPath, TmpDir: os.TempDir(),
		}, logger)
		rs.Build(ctx)
		return buildEmptyDatV2(blockCount), nil
	}

	log.Info("Building txindex segment from Reth",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx)

	// Sort by hash for RecSplit (deterministic ordering).
	sort.Slice(txEntries, func(i, j int) bool {
		return string(txEntries[i].hash[:]) < string(txEntries[j].hash[:])
	})

	// Build RecSplit.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           totalTx,
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         startBlock,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		return nil, err
	}
	for i, e := range txEntries {
		if err := rs.AddKey(e.hash[:], uint64(i)); err != nil {
			return nil, err
		}
	}
	if err := rs.Build(ctx); err != nil {
		return nil, err
	}

	// Build Elias-Fano block boundaries.
	txPerBlockArr := make([]uint32, blockCount)
	for bn, cnt := range txPerBlock {
		txPerBlockArr[bn-startBlock] = cnt
	}
	datBytes := buildDatV2Bytes(blockCount, uint64(totalTx), txPerBlockArr)

	elapsed := time.Since(t0)
	log.Info("Segment built from Reth",
		"blocks", fmt.Sprintf("%d-%d", startBlock, endBlock-1),
		"txCount", totalTx,
		"dat", fmt.Sprintf("%.1f KB", float64(len(datBytes))/1e3),
		"elapsed", elapsed.Truncate(time.Second))
	return datBytes, nil
}
