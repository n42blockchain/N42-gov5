// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// sender_stage.go — parallel ecrecover pipeline for block senders.
//
// SenderStage streams block bodies from the input freezer through a pool
// of goroutines that run ecrecover on every transaction using a signer
// built from the chain config fork rules, then writes the 20-byte
// sender addresses (batch-64 zstd) into the output freezer's "senders"
// table via a reorder buffer. Running senders as a standalone pass lets
// the executor re-use pre-computed senders and skip ecrecover during
// the hot execution loop.

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

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

type SenderStage struct {
	inputFreezer  *freezer.Freezer
	outputFreezer *freezer.Freezer
	chainCfg      *params.ChainConfig
	workers       int
}

type senderWork struct {
	blockNum uint64
	txs      []*transaction.Transaction
	signer   transaction.Signer
}

type senderResult struct {
	blockNum uint64
	senders  []byte // 20B × txCount
}

func NewSenderStage(input, output *freezer.Freezer, chainCfg *params.ChainConfig, workers int) *SenderStage {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	// Ensure input freezer has headers/bodies tables (coreTableSpecs is empty).
	input.EnsureTable("headers", "c")
	input.EnsureTable("bodies", "c")
	return &SenderStage{
		inputFreezer:  input,
		outputFreezer: output,
		chainCfg:      chainCfg,
		workers:       workers,
	}
}

func (s *SenderStage) Run(ctx context.Context) error {
	tbl, err := s.outputFreezer.EnsureTableCompressed("senders", "c")
	if err != nil {
		return fmt.Errorf("open senders table: %w", err)
	}

	startBlock := tbl.Items()
	endBlock := s.inputFreezer.Frozen()
	if startBlock >= endBlock {
		log.Info("Sender recovery: already up to date", "at", startBlock)
		return nil
	}

	log.Info("Sender recovery starting",
		"from", startBlock, "to", endBlock-1,
		"blocks", endBlock-startBlock, "workers", s.workers)

	workCh := make(chan senderWork, s.workers*4)
	resultCh := make(chan senderResult, s.workers*4)
	var processed atomic.Uint64

	// Writer goroutine: reorder and batch-write.
	var writerErr error
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		writerErr = s.writerLoop(tbl, resultCh, startBlock, endBlock, &processed)
	}()

	// Worker pool.
	var workerWg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			for work := range workCh {
				senders := make([]byte, 0, len(work.txs)*20)
				for _, tx := range work.txs {
					sender, err := transaction.Sender(work.signer, tx)
					if err != nil {
						sender = types.Address{}
					}
					senders = append(senders, sender[:]...)
				}
				resultCh <- senderResult{blockNum: work.blockNum, senders: senders}
			}
		}()
	}

	// Reader goroutine.
	t0 := time.Now()
	var readerErr error

	for blockNum := startBlock; blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			break
		}

		headerData, err := s.inputFreezer.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			readerErr = fmt.Errorf("read header %d: %w", blockNum, err)
			break
		}
		header, err := DecodeGethHeader(headerData)
		if err != nil {
			readerErr = fmt.Errorf("decode header %d: %w", blockNum, err)
			break
		}

		bodyData, err := s.inputFreezer.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			readerErr = fmt.Errorf("read body %d: %w", blockNum, err)
			break
		}
		body, err := DecodeGethBody(bodyData)
		if err != nil {
			readerErr = fmt.Errorf("decode body %d: %w", blockNum, err)
			break
		}

		signer := transaction.MakeSigner(s.chainCfg, header.Number.ToBig())

		workCh <- senderWork{
			blockNum: blockNum,
			txs:      body.Transactions,
			signer:   signer,
		}
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
	blkPerSec := float64(total) / elapsed.Seconds()
	log.Info("Sender recovery complete",
		"blocks", total, "elapsed", elapsed.Truncate(time.Second),
		"blk/s", fmt.Sprintf("%.0f", blkPerSec))
	return nil
}

// writerLoop receives results out of order, reorders, and writes in batches.
// Uses the same batch format as compact: [len0:4LE][data0][len1:4LE][data1]...
// with cidx entries sharing the same offset within a batch.
func (s *SenderStage) writerLoop(
	tbl *freezer.FreezerTable,
	results <-chan senderResult,
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

	// Accumulate a full batch, then flush.
	batchEntries := make([][]byte, 0, batchSize)
	batchStartBlock := nextBlock

	flushBatch := func() error {
		if len(batchEntries) == 0 {
			return nil
		}
		encoded := freezer.EncodeBatch(batchEntries, enc)
		if err := freezer.WriteBatch(tbl, batchEntries, encoded); err != nil {
			return fmt.Errorf("write senders batch at %d: %w", batchStartBlock, err)
		}

		batchEntries = batchEntries[:0]
		return nil
	}

	for r := range results {
		pending[r.blockNum] = r.senders

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

			// Flush when batch full.
			if len(batchEntries) >= batchSize {
				if err := flushBatch(); err != nil {
					return err
				}
			}

			// Progress log every 100K blocks.
			done := processed.Load()
			if done%100000 == 0 {
				elapsed := time.Since(t0)
				blkPerSec := float64(done) / elapsed.Seconds()
				pct := float64(nextBlock-startBlock) / float64(endBlock-startBlock) * 100
				log.Info("Sender recovery progress",
					"block", nextBlock-1,
					"pct", fmt.Sprintf("%.1f%%", pct),
					"blk/s", fmt.Sprintf("%.0f", blkPerSec))
			}
		}

		if len(pending) > s.workers*100 {
			log.Warn("Sender recovery: reorder buffer large", "size", len(pending))
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
				return fmt.Errorf("gap in sender results: expected %d, got %d", nextBlock, k)
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
