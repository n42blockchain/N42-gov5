// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// sender_stage.go — parallel ecrecover pipeline for block senders.
//
// SenderStage streams block bodies from a headersBodiesSource (either a
// geth ancient freezer or N42 columnar headerc/bodyc) through a pool of
// goroutines that run ecrecover on every transaction using a signer
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
	inputSrc      HeadersBodiesSource
	outputFreezer *freezer.Freezer
	chainCfg      *params.ChainConfig
	workers       int

	// Optional explicit range. Zero means "auto":
	//   forceStart=0 → resume from tbl.Items()
	//   forceEnd=0   → run until inputFreezer.Frozen()
	// forceStart must equal tbl.Items() (no gaps allowed) — strictly a
	// no-op assertion in the common case, useful for scripted reruns.
	forceStart uint64
	forceEnd   uint64
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

// NewSenderStage builds a SenderStage reading from any HeadersBodiesSource
// (geth ancient or N42 columnar — caller picks via OpenHeadersBodiesSource).
// The caller is responsible for the source's Close lifecycle.
func NewSenderStage(inputSrc HeadersBodiesSource, output *freezer.Freezer, chainCfg *params.ChainConfig, workers int) *SenderStage {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	return &SenderStage{
		inputSrc:      inputSrc,
		outputFreezer: output,
		chainCfg:      chainCfg,
		workers:       workers,
	}
}

// SetRange overrides auto-detected [start, end). Use 0 to keep auto.
//   - start: if non-zero, must equal current senders.Items() (no truncate, no gap).
//   - end:   if non-zero, caps the run at this block (exclusive).
func (s *SenderStage) SetRange(start, end uint64) {
	s.forceStart = start
	s.forceEnd = end
}

func (s *SenderStage) Run(ctx context.Context) error {
	tbl, err := s.outputFreezer.EnsureTableCompressed("senders", "c")
	if err != nil {
		return fmt.Errorf("open senders table: %w", err)
	}

	resumeAt := tbl.Items()
	startBlock := resumeAt
	if s.forceStart != 0 {
		if s.forceStart != resumeAt {
			return fmt.Errorf("sender recovery: --start=%d would create a gap or truncate (current senders.Items()=%d); delete files to redo, or omit --start to auto-resume",
				s.forceStart, resumeAt)
		}
		startBlock = s.forceStart
	}

	endBlock := s.inputSrc.MaxBlock()
	if s.forceEnd != 0 {
		if s.forceEnd > endBlock {
			return fmt.Errorf("sender recovery: --end=%d exceeds input maxBlock=%d", s.forceEnd, endBlock)
		}
		endBlock = s.forceEnd
	}

	if startBlock >= endBlock {
		log.Info("Sender recovery: already up to date", "at", startBlock, "endBlock", endBlock)
		return nil
	}

	mode := "starting"
	if resumeAt > 0 {
		mode = "resuming"
	}
	log.Info("Sender recovery "+mode,
		"from", startBlock, "to", endBlock-1,
		"blocks", endBlock-startBlock, "workers", s.workers,
		"already_done", resumeAt)

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

		header, err := s.inputSrc.Header(blockNum)
		if err != nil {
			// Tolerate read errors right at the end of input range.
			// Two sources of phantom past-end blocks:
			//  - Geth cidx sentinel (freezer-layer fix strips this on
			//    open, but legacy paths/per-table readers can still see
			//    it via direct Ancient calls).
			//  - N42 columnar MaxBlock = segments*8192 over-reports past
			//    the last real block when the final segment is partial.
			if blockNum+1 == endBlock {
				log.Info("Sender recovery: end-of-input read error — stopping cleanly",
					"at", blockNum, "items", tbl.Items(), "err", err)
				break
			}
			readerErr = fmt.Errorf("read header %d: %w", blockNum, err)
			break
		}
		body, err := s.inputSrc.Body(blockNum)
		if err != nil {
			if blockNum+1 == endBlock {
				log.Info("Sender recovery: end-of-input body read error — stopping cleanly",
					"at", blockNum, "items", tbl.Items(), "err", err)
				break
			}
			readerErr = fmt.Errorf("read body %d: %w", blockNum, err)
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

			// Progress log + fsync every 100K blocks.
			// fsync caps resume rollback to ≤100K blocks on hard kill /
			// power loss; without it, buffered cidx/cdat writes can be
			// lost up to the entire run.
			done := processed.Load()
			if done%100000 == 0 {
				if err := flushBatch(); err != nil {
					return err
				}
				if err := tbl.Sync(); err != nil {
					log.Warn("Sender recovery: fsync failed", "err", err)
				}
				elapsed := time.Since(t0)
				blkPerSec := float64(done) / elapsed.Seconds()
				pct := float64(nextBlock-startBlock) / float64(endBlock-startBlock) * 100
				log.Info("Sender recovery progress",
					"block", nextBlock-1,
					"pct", fmt.Sprintf("%.1f%%", pct),
					"blk/s", fmt.Sprintf("%.0f", blkPerSec),
					"items", tbl.Items())
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

	// Final fsync so a clean exit makes the new tail durable; without
	// this the OS may discard buffered writes on power loss.
	if err := tbl.Sync(); err != nil {
		log.Warn("Sender recovery: final fsync failed", "err", err)
	}
	return nil
}
