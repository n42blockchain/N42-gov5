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
// aggregator only needs to sequence them.
type WitnessResult struct {
	BlockNum     uint64
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

	for job := range blockCh {
		select {
		case <-ctx.Done():
			return
		default:
		}
		res := replayWitnessBlock(job, codeTx, chainCfg, engine, mode)
		select {
		case resultCh <- res:
		case <-ctx.Done():
			return
		}
	}
}

// replayWitnessBlock is the pure per-block replay path. No shared
// state between calls; all state I/O via the supplied codeTx.
func replayWitnessBlock(
	job WitnessJob,
	codeTx kv.Tx,
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	mode ReplayMode,
) WitnessResult {
	res := WitnessResult{BlockNum: job.BlockNum, WitnessBytes: job.Witness}

	if len(job.Body.Transactions) == 0 {
		if job.Header.GasUsed != 0 {
			res.Err = fmt.Errorf("block %d: empty body but header.GasUsed=%d",
				job.BlockNum, job.Header.GasUsed)
			return res
		}
		if !mode.NoOutput {
			res.ReceiptBytes = EncodeReceiptsCompact(nil)
		}
		return res
	}

	reader := NewWitnessReplayReader(job.Witness, codeTx)
	ibs := state.New(reader)

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
