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

	txnBlocks, err := cscompact.LoadTransactionBlocks(b.db, ctx)
	if err != nil {
		return fmt.Errorf("load TransactionBlocks: %w", err)
	}
	log.Info("TransactionBlocks loaded", "entries", len(txnBlocks))

	// Compute txNum range for each segment to avoid full-table scan.
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

// txNumRange returns the [minTxNum, maxTxNum) range for a block range.
// txnBlocks is sorted by TxNum, and BlockNum is monotonically increasing
// (each entry = first tx of a new block), so both fields are co-sorted.
func txNumRange(txnBlocks []cscompact.TxBlockEntry, startBlock, endBlock uint64) (uint64, uint64) {
	// Binary search: both TxNum and BlockNum are co-sorted (monotonic).
	startIdx := sort.Search(len(txnBlocks), func(i int) bool {
		return txnBlocks[i].BlockNum >= startBlock
	})
	endIdx := sort.Search(len(txnBlocks), func(i int) bool {
		return txnBlocks[i].BlockNum >= endBlock
	})

	var minTxNum uint64
	if startIdx < len(txnBlocks) {
		minTxNum = txnBlocks[startIdx].TxNum
	}
	var maxTxNum uint64
	if endIdx < len(txnBlocks) {
		maxTxNum = txnBlocks[endIdx].TxNum
	} else if len(txnBlocks) > 0 {
		maxTxNum = txnBlocks[len(txnBlocks)-1].TxNum + 100_000_000
	}
	return minTxNum, maxTxNum
}

func (b *RethBuilder) buildOneFromReth(ctx context.Context, startBlock, endBlock uint64, idxPath string, txnBlocks []cscompact.TxBlockEntry) ([]byte, error) {
	t0 := time.Now()
	blockCount := endBlock - startBlock

	// Compute txNum range to limit scan scope.
	minTxNum, maxTxNum := txNumRange(txnBlocks, startBlock, endBlock)

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

	type txEntry struct {
		hash     [32]byte
		blockNum uint64
	}

	var txEntries []txEntry
	txPerBlock := make(map[uint64]uint32)

	// Scan all entries (hash-ordered, no range seek possible).
	// But skip entries outside [minTxNum, maxTxNum) using txNum from value.
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
		txNum := binary.BigEndian.Uint64(v)
		// Fast filter by txNum range before expensive binary search.
		if txNum < minTxNum || txNum >= maxTxNum {
			continue
		}
		blockNum := cscompact.TxNumToBlockNum(txnBlocks, txNum)
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

	sort.Slice(txEntries, func(i, j int) bool {
		return string(txEntries[i].hash[:]) < string(txEntries[j].hash[:])
	})

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
