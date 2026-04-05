// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// ReceiptStage reads receipts from a Geth ancient freezer, decodes the Geth
// RLP format, re-encodes them with EncodeReceiptsCompact (matching the exec
// output format), and writes batch-compressed (batch 64) into the output
// freezer's "receipts" table.
//
// Architecture: reader → workCh → N workers → resultCh → writer (reorder + batch).
type ReceiptStage struct {
	inputFreezer  *freezer.Freezer
	outputFreezer *freezer.Freezer
	workers       int
}

type receiptWork struct {
	blockNum uint64
	gethData []byte // raw Geth ancient receipt (snappy RLP)
}

type receiptResult struct {
	blockNum    uint64
	compactData []byte // EncodeReceiptsCompact output
}

func NewReceiptStage(input, output *freezer.Freezer, workers int) *ReceiptStage {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &ReceiptStage{
		inputFreezer:  input,
		outputFreezer: output,
		workers:       workers,
	}
}

func (s *ReceiptStage) Run(ctx context.Context) error {
	tbl, err := s.outputFreezer.EnsureTableCompressed("receipts", "c")
	if err != nil {
		return fmt.Errorf("open receipts table: %w", err)
	}

	startBlock := tbl.Items()
	endBlock := s.inputFreezer.Frozen()
	if startBlock >= endBlock {
		log.Info("Receipt copy: already up to date", "at", startBlock)
		return nil
	}

	log.Info("Receipt copy starting",
		"from", startBlock, "to", endBlock-1,
		"blocks", endBlock-startBlock, "workers", s.workers)

	workCh := make(chan receiptWork, s.workers*4)
	resultCh := make(chan receiptResult, s.workers*4)
	var processed atomic.Uint64

	// Writer goroutine: reorder and batch-write.
	var writerErr error
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		writerErr = s.writerLoop(tbl, resultCh, startBlock, endBlock, &processed)
	}()

	// Worker pool: decode Geth RLP → re-encode compact.
	var workerWg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			for work := range workCh {
				receipts, err := DecodeGethReceipts(work.gethData)
				if err != nil {
					// Send empty compact data on decode error — writer will
					// still maintain correct block ordering.
					resultCh <- receiptResult{blockNum: work.blockNum}
					continue
				}
				resultCh <- receiptResult{
					blockNum:    work.blockNum,
					compactData: EncodeReceiptsCompact(receipts),
				}
			}
		}()
	}

	// Reader: sequential freezer reads → workCh.
	t0 := time.Now()
	var readerErr error
	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			break
		}
		data, err := s.inputFreezer.Ancient(freezer.TableReceipts, blockNum)
		if err != nil {
			readerErr = fmt.Errorf("read receipt %d: %w", blockNum, err)
			break
		}
		workCh <- receiptWork{blockNum: blockNum, gethData: data}
	}

	close(workCh)
	workerWg.Wait()
	close(resultCh)
	writerWg.Wait()

	if readerErr != nil {
		return readerErr
	}
	if writerErr != nil {
		return writerErr
	}

	elapsed := time.Since(t0)
	total := processed.Load()
	if total > 0 {
		blkPerSec := float64(total) / elapsed.Seconds()
		log.Info("Receipt copy complete",
			"blocks", total, "elapsed", elapsed.Truncate(time.Second),
			"blk/s", fmt.Sprintf("%.0f", blkPerSec))
	}
	return nil
}

// writerLoop receives results out of order, reorders, and writes in batches.
func (s *ReceiptStage) writerLoop(
	tbl *freezer.FreezerTable,
	results <-chan receiptResult,
	startBlock, endBlock uint64,
	processed *atomic.Uint64,
) error {
	batchSize := freezer.BatchSize

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer enc.Close()

	pending := make(map[uint64][]byte)
	nextBlock := startBlock
	t0 := time.Now()

	batchEntries := make([][]byte, 0, batchSize)
	batchStartBlock := nextBlock

	flushBatch := func() error {
		if len(batchEntries) == 0 {
			return nil
		}
		encoded := freezer.EncodeBatch(batchEntries, enc)
		if err := freezer.WriteBatch(tbl, batchEntries, encoded); err != nil {
			return fmt.Errorf("write receipts batch at %d: %w", batchStartBlock, err)
		}
		batchEntries = batchEntries[:0]
		return nil
	}

	for r := range results {
		pending[r.blockNum] = r.compactData

		for {
			data, ok := pending[nextBlock]
			if !ok {
				break
			}
			delete(pending, nextBlock)

			if len(batchEntries) == 0 {
				batchStartBlock = nextBlock
			}
			batchEntries = append(batchEntries, data)
			nextBlock++
			processed.Add(1)

			if len(batchEntries) >= batchSize {
				if err := flushBatch(); err != nil {
					return err
				}
			}

			done := processed.Load()
			if done%100000 == 0 {
				elapsed := time.Since(t0)
				blkPerSec := float64(done) / elapsed.Seconds()
				pct := float64(nextBlock-startBlock) / float64(endBlock-startBlock) * 100
				log.Info("Receipt copy progress",
					"block", nextBlock-1,
					"pct", fmt.Sprintf("%.1f%%", pct),
					"blk/s", fmt.Sprintf("%.0f", blkPerSec))
			}
		}

		if len(pending) > s.workers*100 {
			log.Warn("Receipt copy: reorder buffer large", "size", len(pending))
		}
	}

	// Flush remaining partial batch.
	if len(batchEntries) > 0 {
		if err := flushBatch(); err != nil {
			return err
		}
	}

	// Flush remaining pending (shouldn't happen in normal flow).
	if len(pending) > 0 {
		keys := make([]uint64, 0, len(pending))
		for k := range pending {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, k := range keys {
			if k != nextBlock {
				return fmt.Errorf("gap in receipt results: expected %d, got %d", nextBlock, k)
			}
			batchStartBlock = nextBlock
			batchEntries = append(batchEntries, pending[k])
			nextBlock++
			processed.Add(1)
			if len(batchEntries) >= batchSize {
				if err := flushBatch(); err != nil {
					return err
				}
			}
		}
		if err := flushBatch(); err != nil {
			return err
		}
	}

	return nil
}
