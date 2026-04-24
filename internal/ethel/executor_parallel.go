// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_parallel.go — Block-STM parallel execution path for ethel.
//
// executeBlockParallel is a drop-in replacement for executeBlock that
// uses the parallel EVM pipeline (MVHashMap + Block-STM scheduler +
// RealParallelEVM) for the tx loop. Pre-block and post-block hooks
// (DAO fork, EIP-4788 beacon root, engine.Finalize rewards, etc.) still
// run sequentially — they modify a small, fixed set of addresses and
// parallelizing them buys nothing while adding complexity.
//
// Shape of the block:
//
//   1. Pre-block: sequential IntraBlockState on the shared buffer.
//      DAO fork + ProcessExecutionBlockStart (beacon root, Prague system
//      calls). CommitBlock flushes pre-block writes into stateBuf so the
//      parallel tx loop sees them via fall-through reads.
//
//   2. Tx loop (parallel): RealParallelEVM wraps ApplyTransaction
//      behind a fresh IBS per tx, with MVStateReader / MVStateWriter as
//      the I/O adapters. Block-STM scheduler coordinates validation
//      and re-execution. FinalizeBlock aggregates outputs and
//      PlainStateBufferApplyTarget flushes them to stateBuf.
//
//   3. Post-block: sequential IBS. ProcessExecutionBlockEnd (Prague
//      system calls; no-op pre-Prague) + engine.Finalize (block
//      rewards + uncles). CommitBlock flushes to stateBuf.
//
// Output freezer writes (receipts, senders, changesets) are SKIPPED in
// the parallel path for Phase 5 MVP — building n42 receipts from the
// BlockCommit's scalar TxReceipts requires per-tx ContractAddress +
// Bloom computation that isn't on the critical path to state-root
// equivalence. The ethexec startup warns when --parallel-evm is set
// and the output freezer is configured.

package ethel

import (
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	iinternal "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// executeBlockParallel is the Block-STM variant of executeBlock.
// Preconditions: e.cfg.ParallelEVM == true and e.cfg.ParallelWorkers > 0.
func (e *Executor) executeBlockParallel(ctx context.Context, tx kv.Tx, blockNum uint64) error {
	t0 := time.Now()

	header, err := e.readHeader(blockNum)
	if err != nil {
		return err
	}
	body, err := e.readBody(blockNum)
	if err != nil {
		return err
	}
	e.cacheHeader(blockNum, header)

	// Shared state I/O over the in-memory buffer. Reader sees:
	// active buf → in-flight snapshot → LRU → MDBX.
	bufReader := state.NewBufferedPlainStateReader(e.stateBuf, tx)
	// NoHistory writer: the parallel path does NOT emit changesets for
	// Phase 5 MVP. Parallel-aware ChangeSetWriter is a later task.
	bufWriter := state.NewBufferedPlainStateWriterNoHistory(e.stateBuf)

	t1 := time.Now()

	// --- Phase 1: pre-block hooks (DAO fork, EIP-4788, Prague pre-calls) ---
	ibsPre := state.New(bufReader)
	if e.chainCfg.DAOForkSupport && e.chainCfg.DAOForkBlock != nil &&
		e.chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibsPre)
	}
	if err := iinternal.ProcessExecutionBlockStart(header.ParentBeaconRoot, e.chainCfg, ibsPre, header, e.engine); err != nil {
		return fmt.Errorf("ProcessExecutionBlockStart: %w", err)
	}
	rules := e.chainCfg.Rules(header.Number.Uint64())
	if err := ibsPre.CommitBlock(rules, bufWriter); err != nil {
		return fmt.Errorf("pre-block CommitBlock: %w", err)
	}

	// --- Phase 2: parallel tx loop ---
	// Pre-recovered senders from senderStore/senderFreezer, or nil if
	// none were configured. We keep zero addresses as a "no pre-recovery"
	// sentinel — RealParallelEVM.Execute only calls tx.SetFrom when the
	// sender is non-zero; otherwise ApplyTransaction's AsMessage performs
	// ecrecover. This matches the sequential ProcessBlock path, which
	// only calls SetFrom when precomputedSenders != nil.
	senders := e.loadSenders(blockNum, len(body.Transactions))
	if senders == nil {
		senders = make([]types.Address, len(body.Transactions))
	}

	totalGas := uint64(0)
	if len(body.Transactions) > 0 {
		mvBase := state.NewMVBaseFromStateReader(bufReader)
		parallelEVM := NewRealParallelEVM(
			e.chainCfg, e.engine, vm2.Config{},
			header, e.makeBlockHashFunc(header), nil,
		)
		numWorkers := e.cfg.ParallelWorkers
		if numWorkers <= 0 {
			numWorkers = 8
		}

		outputs := make([]state.TxOutput, len(body.Transactions))
		// state.BlockContext is unused by RealParallelEVM (it consults the
		// header directly) but the signature requires one — pass zero.
		runnerBlock := &state.BlockContext{}
		runner := state.ParallelTxRunner(parallelEVM, body.Transactions, senders, runnerBlock, outputs)

		_, mv, err := state.ExecuteBlockParallel(
			len(body.Transactions), numWorkers, mvBase, runner,
		)
		if err != nil {
			return fmt.Errorf("ExecuteBlockParallel: %w", err)
		}

		for _, out := range outputs {
			totalGas += out.GasUsed
		}
		// Gas sanity checks BEFORE we flush writes. If we're wrong about
		// gas, the block state is already inconsistent — bail before
		// corrupting the buffer.
		if totalGas > header.GasLimit {
			return fmt.Errorf("block %d: sum per-tx gas %d > header.GasLimit %d",
				blockNum, totalGas, header.GasLimit)
		}

		bc, err := state.FinalizeBlock(mv, outputs, header.Coinbase)
		if err != nil {
			return fmt.Errorf("FinalizeBlock: %w", err)
		}
		target := state.NewPlainStateBufferApplyTarget(bufWriter)
		if err := bc.Apply(target); err != nil {
			return fmt.Errorf("BlockCommit.Apply: %w", err)
		}
	}

	// Gas mismatch check (after parallel loop, consistent with sequential
	// path). Parallel and sequential should converge on the same total
	// gas if the txs executed identically.
	if totalGas != header.GasUsed {
		if e.cfg.SkipErrors {
			log.Warn("Gas mismatch (skipped, parallel)",
				"block", blockNum, "got", totalGas, "want", header.GasUsed,
				"diff", int64(header.GasUsed)-int64(totalGas))
		} else {
			return fmt.Errorf("gas mismatch (parallel): got %d, want %d", totalGas, header.GasUsed)
		}
	}

	// --- Phase 3: post-block hooks (Prague system calls + engine.Finalize) ---
	ibsPost := state.New(bufReader)
	// For pre-Prague blocks, ProcessExecutionBlockEnd is a no-op regardless
	// of the receipts argument. Post-Prague needs receipts built from the
	// parallel outputs — Phase 5 follow-up.
	if rules.IsPrague {
		return fmt.Errorf("block %d: --parallel-evm does not yet support post-Prague receipts for ProcessExecutionBlockEnd",
			blockNum)
	}
	if _, err := iinternal.ProcessExecutionBlockEnd(nil, e.chainCfg, ibsPost, header, e.engine); err != nil {
		return fmt.Errorf("ProcessExecutionBlockEnd: %w", err)
	}
	if header.Number.Uint64() > 0 {
		var uncles []block.IHeader
		for _, u := range body.Uncles {
			uncles = append(uncles, u)
		}
		if _, _, err := e.engine.Finalize(nil, header, ibsPost, body.Transactions, uncles); err != nil {
			return fmt.Errorf("finalize: %w", err)
		}
	}
	if err := ibsPost.CommitBlock(rules, bufWriter); err != nil {
		return fmt.Errorf("post-block CommitBlock: %w", err)
	}

	t5 := time.Now()
	e.collectTiming(blockNum, timingSample{
		read:    t1.Sub(t0),
		evm:     t5.Sub(t1),
		total:   t5.Sub(t0),
		txCount: len(body.Transactions),
		gasUsed: totalGas,
	})
	return nil
}
