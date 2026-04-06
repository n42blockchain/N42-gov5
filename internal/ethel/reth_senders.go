// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reth_senders.go extracts pre-computed senders from Reth MDBX
// TransactionSenders table and writes them as batch-64 compressed
// chain/senders.cidx + senders.NNNN.cdat files.
//
// Reth format: TransactionSenders key=txNum(8B LE), value=sender(20B)
// TransactionBlocks key=txNum(8B LE), value=blockNum(8B LE) (sparse)

package ethel

import (
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// RethSenderExporter reads senders from Reth MDBX and writes batch-compressed segments.
type RethSenderExporter struct {
	db        kv.RoDB
	outputDir string
}

func NewRethSenderExporter(db kv.RoDB, outputDir string) *RethSenderExporter {
	return &RethSenderExporter{db: db, outputDir: outputDir}
}

// txBlockEntry for the sorted TransactionBlocks lookup.
type txBlockEntry struct {
	txNum    uint64
	blockNum uint64
}

func (e *RethSenderExporter) Export(ctx context.Context, startBlock, endBlock uint64) error {
	t0 := time.Now()

	// 1. Load TransactionBlocks (sparse txNum→blockNum mapping).
	txBlocks, err := e.loadTxBlocks(ctx)
	if err != nil {
		return fmt.Errorf("load TransactionBlocks: %w", err)
	}
	log.Info("TransactionBlocks loaded", "entries", len(txBlocks))

	// 2. Open segment store for output.
	store, err := cscompact.NewSegmentStoreWriter(e.outputDir, "senders")
	if err != nil {
		return err
	}
	defer store.Close()

	// Resume.
	existingSegs := store.SegmentCount()
	if existingSegs > 0 {
		resumeBlock := existingSegs * uint64(freezer.BatchSize)
		if resumeBlock > startBlock {
			startBlock = resumeBlock
			log.Info("Resuming senders export", "from", startBlock)
		}
	}

	// 3. Scan TransactionSenders, group by block, write batches.
	tx, err := e.db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor("TransactionSenders")
	if err != nil {
		return fmt.Errorf("open TransactionSenders: %w", err)
	}
	defer cursor.Close()

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()

	// Accumulate senders per block, flush in batch-64.
	type blockSenders struct {
		blockNum uint64
		senders  []byte // concatenated 20B addresses
	}

	var batch [][]byte
	var currentBlock uint64
	var currentSenders []byte
	var totalBlocks, totalTx uint64

	flushBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		encoded := freezer.EncodeBatch(batch, enc)
		if _, err := store.WriteSegment(encoded, ""); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	finishBlock := func() {
		if currentBlock >= startBlock && currentBlock < endBlock {
			batch = append(batch, append([]byte{}, currentSenders...))
			totalBlocks++
			if len(batch) >= freezer.BatchSize {
				if err := flushBatch(); err != nil {
					log.Warn("flush error", "err", err)
				}
			}
		}
		currentSenders = currentSenders[:0]
	}

	// Iterate all senders sequentially (txNum order).
	prevBlockNum := uint64(0)
	first := true
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(k) < 8 || len(v) < 20 {
			continue
		}

		txNum := binary.LittleEndian.Uint64(k)
		blockNum := txNumToBlock(txBlocks, txNum)

		if blockNum >= endBlock {
			break
		}

		if first || blockNum != prevBlockNum {
			if !first {
				finishBlock()
			}
			// Fill empty blocks between prevBlockNum and blockNum.
			if !first && blockNum > prevBlockNum+1 {
				for gap := prevBlockNum + 1; gap < blockNum && gap < endBlock; gap++ {
					if gap >= startBlock {
						batch = append(batch, nil) // empty block
						totalBlocks++
						if len(batch) >= freezer.BatchSize {
							flushBatch()
						}
					}
				}
			}
			currentBlock = blockNum
			prevBlockNum = blockNum
			first = false
		}

		currentSenders = append(currentSenders, v[:20]...)
		totalTx++

		if totalTx%10_000_000 == 0 {
			log.Info("  senders progress",
				"txs", totalTx,
				"blocks", totalBlocks,
				"block", blockNum,
				"elapsed", time.Since(t0).Truncate(time.Second))
		}
	}

	// Finish last block + flush remaining.
	if len(currentSenders) > 0 || !first {
		finishBlock()
	}
	if err := flushBatch(); err != nil {
		return err
	}

	elapsed := time.Since(t0)
	log.Info("Senders export complete",
		"blocks", totalBlocks,
		"txs", totalTx,
		"segments", store.SegmentCount(),
		"elapsed", elapsed.Truncate(time.Second))
	return nil
}

func (e *RethSenderExporter) loadTxBlocks(ctx context.Context) ([]txBlockEntry, error) {
	tx, err := e.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor, err := tx.Cursor("TransactionBlocks")
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var entries []txBlockEntry
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 8 || len(v) < 8 {
			continue
		}
		entries = append(entries, txBlockEntry{
			txNum:    binary.LittleEndian.Uint64(k),
			blockNum: binary.LittleEndian.Uint64(v),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].txNum < entries[j].txNum
	})
	return entries, nil
}

func txNumToBlock(txBlocks []txBlockEntry, txNum uint64) uint64 {
	idx := sort.Search(len(txBlocks), func(i int) bool {
		return txBlocks[i].txNum > txNum
	}) - 1
	if idx < 0 {
		return 0
	}
	return txBlocks[idx].blockNum
}
