// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_worker.go — per-block parallel replay logic.
//
// A worker pulls a (header, body, witness, senders) tuple from blockCh,
// re-executes the block against a witness-backed StateReader + a
// per-block WitnessCapturingWriter, verifies receipts root + gasUsed,
// encodes acctcs/storcs/receipts bytes, and pushes the result to
// resultCh. Workers are fully independent — each holds its own MDBX
// RoTx for the Code table and its own per-block writer state.

package ethel

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// WitnessJob is the input handed to a worker for one block. All fields
// are owned by the worker once received; no shared mutable state.
type WitnessJob struct {
	BlockNum    uint64
	Header      *block.Header
	Body        *GethBodyResult
	Witness     []byte
	Senders     []types.Address
	BlockHashFn func(uint64) types.Hash
}

// WitnessResult is what a worker produces per block. Output bytes are
// ready for outputBatcher.addEntry; the encoder ran inline so the
// aggregator only needs to sequence them. GasUsed and TxCount let the
// aggregator log throughput in mgas/s and tx/s without re-reading
// headers.
type WitnessResult struct {
	BlockNum     uint64
	GasUsed      uint64
	TxCount      uint32
	ReceiptBytes []byte
	AcctCSBytes  []byte
	StoCSBytes   []byte
	WitnessBytes []byte
	Err          error
}

// ReplayMode bundles the per-worker switches that control whether to
// verify gas / emit output cdat bytes. Bundling avoids parameter
// sprawl through runWitnessWorker → replayWitnessBlock.
type ReplayMode struct {
	SkipVerify bool
	NoOutput   bool
}

// runWitnessWorker pulls jobs from blockCh, replays each, pushes
// WitnessResult to resultCh. Returns when blockCh closes or ctx
// cancels.
func runWitnessWorker(
	ctx context.Context,
	id int,
	blockCh <-chan WitnessJob,
	resultCh chan<- WitnessResult,
	codeDB kv.RoDB,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
) {
	codeTx, err := codeDB.BeginRo(ctx)
	if err != nil {
		select {
		case resultCh <- WitnessResult{Err: fmt.Errorf("worker %d: BeginRo: %w", id, err)}:
		case <-ctx.Done():
		}
		return
	}
	defer codeTx.Rollback()

	// One IBS per worker, reused across blocks via Reset + SetStateReader.
	// Without reuse, every block allocated a fresh IBS plus a fresh
	// stateObject for every touched address — pprof showed the
	// stateObjectPool.New func at 18% of all allocs because the per-block
	// IBS was discarded with its objects, never returning them to the
	// pool. Reset returns objects to pool before the next block touches
	// them, so the pool actually recycles.
	reader := NewWitnessReplayReader(nil, codeTx)
	ibs := state.New(reader)

	// Select on ctx during receive so workers exit promptly when the
	// pipeline cancels (early error before the reader goroutine can
	// close blockCh — without this, codeTx held here deadlocks the
	// caller's defer codeDB.Close()).
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-blockCh:
			if !ok {
				return
			}
			res := replayWitnessBlock(job, codeTx, chainCfg, engine, mode, ibs, reader)
			select {
			case resultCh <- res:
			case <-ctx.Done():
				return
			}
		}
	}
}

// replayWitnessBlock is the per-block replay path. The supplied ibs +
// reader are owned by the caller (worker); this function rebinds them
// to the new block's witness stream and resets in-place to put the
// previous block's stateObjects back into the pool.
func replayWitnessBlock(
	job WitnessJob,
	codeTx kv.Tx,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
	ibs *state.IntraBlockState,
	reader *WitnessReplayReader,
) WitnessResult {
	res := WitnessResult{
		BlockNum:     job.BlockNum,
		GasUsed:      job.Header.GasUsed,
		TxCount:      uint32(len(job.Body.Transactions)),
		WitnessBytes: job.Witness,
	}

	if len(job.Body.Transactions) == 0 && job.Header.GasUsed != 0 {
		res.Err = fmt.Errorf("block %d: empty body but header.GasUsed=%d",
			job.BlockNum, job.Header.GasUsed)
		return res
	}

	// Block 0 is handled by the pipeline (genesis encoding requires a
	// fresh chaindata, not the user's potentially-populated MDBX); the
	// reader skips block 0 and the aggregator injects pre-encoded
	// bytes directly. Worker should never see block 0.
	if job.BlockNum == 0 {
		res.Err = fmt.Errorf("worker received block 0 — pipeline should have intercepted")
		return res
	}

	reader.Reset(job.Witness)
	ibs.Reset()

	var writer *WitnessCapturingWriter
	var stateWriter state.WriterWithChangeSets
	if mode.NoOutput {
		stateWriter = state.NewNoopWriter()
	} else {
		writer = NewWitnessCapturingWriter()
		stateWriter = writer
	}

	uncles := make([]block.IHeader, len(job.Body.Uncles))
	for i, u := range job.Body.Uncles {
		uncles[i] = u
	}

	result, err := ProcessBlock(
		chainCfg, engine, job.Header,
		job.Body.Transactions, uncles, job.Body.Withdrawals,
		ibs, job.BlockHashFn, job.Senders, stateWriter,
	)
	if err != nil {
		res.Err = fmt.Errorf("block %d: ProcessBlock: %w", job.BlockNum, err)
		return res
	}

	if !mode.SkipVerify && result.GasUsed != job.Header.GasUsed {
		res.Err = fmt.Errorf("block %d: gas mismatch: got %d want %d",
			job.BlockNum, result.GasUsed, job.Header.GasUsed)
		return res
	}

	if mode.NoOutput {
		return res
	}

	rules := chainCfg.Rules(job.Header.Number.Uint64())
	if err := ibs.CommitBlock(rules, writer); err != nil {
		res.Err = fmt.Errorf("block %d: CommitBlock: %w", job.BlockNum, err)
		return res
	}

	acctsCS, err := writer.ChangeSetWriter().GetAccountChanges()
	if err != nil {
		res.Err = fmt.Errorf("block %d: GetAccountChanges: %w", job.BlockNum, err)
		return res
	}
	stoCS, err := writer.ChangeSetWriter().GetStorageChanges()
	if err != nil {
		res.Err = fmt.Errorf("block %d: GetStorageChanges: %w", job.BlockNum, err)
		return res
	}
	res.AcctCSBytes = EncodeAccountChanges(acctsCS, writer.AccountNewValue)
	res.StoCSBytes = EncodeStorageChanges(stoCS, writer.StorageNewValue)
	res.ReceiptBytes = EncodeReceiptsCompact(result.Receipts)
	return res
}
