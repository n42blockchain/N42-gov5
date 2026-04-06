// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reth_senders.go extracts pre-computed senders from Reth MDBX
// TransactionSenders table and writes batch-64 compressed segments.
//
// Reth format: TransactionSenders key=txNum(8B BE), value=sender(20B)
// TransactionBlocks key=txNum(8B BE), value=blockNum(8B BE) (sparse)

package ethel

import (
	"context"
	"encoding/binary"
	"fmt"
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

func (e *RethSenderExporter) Export(ctx context.Context, startBlock, endBlock uint64) error {
	t0 := time.Now()

	txBlocks, err := cscompact.LoadTransactionBlocks(e.db, ctx)
	if err != nil {
		return fmt.Errorf("load TransactionBlocks: %w", err)
	}
	log.Info("TransactionBlocks loaded", "entries", len(txBlocks))

	store, err := cscompact.NewSegmentStoreWriter(e.outputDir, "senders")
	if err != nil {
		return err
	}
	defer store.Close()

	existingSegs := store.SegmentCount()
	if existingSegs > 0 {
		resumeBlock := existingSegs * uint64(freezer.BatchSize)
		if resumeBlock > startBlock {
			startBlock = resumeBlock
			log.Info("Resuming senders export", "from", startBlock)
		}
	}

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

	var batch [][]byte
	var currentSenders []byte
	var prevBlockNum uint64
	var totalBlocks, totalTx uint64
	first := true

	// Linear advance through txBlocks instead of binary search per entry.
	// txBlocks is sorted by txNum; cursor iterates txNum in order.
	txBlockIdx := 0

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

	finishBlock := func() error {
		if currentBlock := prevBlockNum; currentBlock >= startBlock && currentBlock < endBlock {
			batch = append(batch, append([]byte{}, currentSenders...))
			totalBlocks++
			if len(batch) >= freezer.BatchSize {
				if err := flushBatch(); err != nil {
					return err
				}
			}
		}
		currentSenders = currentSenders[:0]
		return nil
	}

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

		txNum := binary.BigEndian.Uint64(k)

		// Linear advance: move txBlockIdx forward until txBlocks[txBlockIdx+1].TxNum > txNum.
		for txBlockIdx+1 < len(txBlocks) && txBlocks[txBlockIdx+1].TxNum <= txNum {
			txBlockIdx++
		}
		var blockNum uint64
		if txBlockIdx < len(txBlocks) {
			blockNum = txBlocks[txBlockIdx].BlockNum
		}

		if blockNum >= endBlock {
			break
		}

		if first || blockNum != prevBlockNum {
			if !first {
				if err := finishBlock(); err != nil {
					return err
				}
			}
			// Fill empty blocks.
			if !first && blockNum > prevBlockNum+1 {
				for gap := prevBlockNum + 1; gap < blockNum && gap < endBlock; gap++ {
					if gap >= startBlock {
						batch = append(batch, nil)
						totalBlocks++
						if len(batch) >= freezer.BatchSize {
							if err := flushBatch(); err != nil {
								return err
							}
						}
					}
				}
			}
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

	if len(currentSenders) > 0 || !first {
		if err := finishBlock(); err != nil {
			return err
		}
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
