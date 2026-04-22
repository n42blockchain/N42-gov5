// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// parallel_evm.go — real EVM integration for the Block-STM parallel
// executor. Wraps vm.EVM + IntraBlockState behind the state.ParallelEVM
// interface by reusing the existing internal.ApplyTransaction pipeline,
// swapping in MVStateReader + MVStateWriter as the state-access points.
//
// Design:
//   - One ParallelEVM instance per block (holds header + block context).
//   - Each Execute() call builds a fresh IntraBlockState with an
//     MVStateReader backed by the per-tx view. Writes flow through a
//     fresh MVStateWriter into the same view. No sharing between
//     concurrent tx executions.
//   - Each tx gets its own GasPool seeded with the full block gas
//     limit (parallel txs cannot share a gas counter; commit phase
//     sums per-tx GasUsed and enforces the block limit).
//   - Abort-retry: if the view observed an MVEstimate read, we return
//     state.AbortRetry() without flushing. Scheduler re-queues.

package ethel

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalpkg "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// RealParallelEVM adapts n42's real EVM (vm.EVM + IntraBlockState +
// internal.ApplyTransaction) to the state.ParallelEVM interface so
// Block-STM workers can run mainnet-real transactions in parallel.
type RealParallelEVM struct {
	chainCfg      *params.ChainConfig
	engine        consensus.Engine
	cfg           vm2.Config
	header        *block.Header
	blockHashFunc func(uint64) types.Hash
	author        *types.Address
}

// NewRealParallelEVM constructs an adapter for one block. Reusable
// across the block's txs; not safe to call Execute concurrently on
// the SAME adapter (each goroutine needs its own).
//
// Implementation note: the adapter itself is stateless after
// construction — all per-tx state is derived from the (tx, view)
// arguments of Execute. So sharing is technically safe, but for
// clarity we document it as per-worker.
func NewRealParallelEVM(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	cfg vm2.Config,
	header *block.Header,
	blockHashFunc func(uint64) types.Hash,
	author *types.Address,
) *RealParallelEVM {
	return &RealParallelEVM{
		chainCfg:      chainCfg,
		engine:        engine,
		cfg:           cfg,
		header:        header,
		blockHashFunc: blockHashFunc,
		author:        author,
	}
}

// Execute satisfies state.ParallelEVM. Runs one transaction against
// the supplied per-tx view and returns the commit-phase summary.
func (e *RealParallelEVM) Execute(
	tx *transaction.Transaction,
	sender types.Address,
	view *state.EVMStateView,
	_ *state.BlockContext, // unused — we use the raw *block.Header we were constructed with
) (state.TxOutput, error) {
	// Propagate the pre-recovered sender into the tx so ApplyTransaction
	// doesn't re-ecrecover. Matches the sequential ProcessBlock path.
	tx.SetFrom(sender)

	reader := state.NewMVStateReader(view)
	writer := state.NewMVStateWriter(view)

	// Fresh IntraBlockState per tx. The existing IntraBlockState
	// isn't thread-safe; one-per-tx avoids ANY shared-state races.
	// Cost: ~1 allocation per tx for the IBS struct. EVM + blockCtx
	// will also reallocate. Over ~200 tx/block this is ~200 allocs —
	// negligible compared to the per-tx EVM work.
	ibs := state.New(reader)

	// Set tx context on IBS (thash, bhash, txIndex) matching the
	// sequential ProcessBlock.Prepare() call. blockHash = header hash.
	// We don't have a canonical txIdx here — the scheduler knows it
	// but didn't pass it. Leave blank; AddLog records it as 0 which
	// is fine for parallel execution (the commit phase re-assigns
	// log indices in tx-order anyway).
	ibs.Prepare(tx.Hash(), e.header.Hash(), 0)

	// Per-tx gas pool: seed with FULL block gas limit. Parallel txs
	// cannot share a gas counter (races). Commit phase sums per-tx
	// GasUsed and verifies totalUsed ≤ header.GasLimit.
	gp := new(common.GasPool)
	gp.AddGas(e.header.GasLimit)

	var usedGas uint64
	receipt, _, err := internalpkg.ApplyTransaction(
		e.chainCfg,
		e.blockHashFunc,
		e.engine,
		e.author,
		gp,
		ibs,
		writer,
		e.header,
		tx,
		&usedGas,
		e.cfg,
	)

	// If any read during execution observed an MVEstimate, the
	// result is speculative-garbage — tell scheduler to retry.
	// Check AbortPending BEFORE surfacing err so a reader-hit-estimate
	// doesn't leak as a terminal tx error.
	if view.Inner().AbortPending() {
		return state.TxOutput{}, state.AbortRetry()
	}

	if err != nil {
		// Terminal tx failure (not an abort-retry). Keep the partial
		// GasUsed but mark error; commit still builds a failed receipt.
		return state.TxOutput{
			GasUsed: usedGas,
			Status:  0,
			Err:     err,
		}, err
	}

	// Convert receipt logs → state.Log. state.Log's TxIndex/Index are
	// filled by the commit phase from the block-global log counter.
	var logs []state.Log
	if receipt != nil {
		for _, l := range receipt.Logs {
			topics := make([]types.Hash, len(l.Topics))
			copy(topics, l.Topics)
			data := make([]byte, len(l.Data))
			copy(data, l.Data)
			logs = append(logs, state.Log{
				Address: l.Address,
				Topics:  topics,
				Data:    data,
			})
		}
	}

	// Coinbase tip = effective gas price × gas used, minus base fee.
	// For London+ txs: tip = min(tx.Tip, tx.GasFeeCap - baseFee).
	// For pre-London: tip = tx.GasPrice - baseFee (often == tx.GasPrice
	//                 when baseFee is zero).
	tip := computeCoinbaseTip(tx, e.header, usedGas)

	status := uint8(1)
	if receipt != nil {
		status = uint8(receipt.Status)
	}

	return state.TxOutput{
		GasUsed:     usedGas,
		Status:      status,
		Logs:        logs,
		CoinbaseTip: tip,
	}, nil
}

// computeCoinbaseTip calculates the portion of the tx's gas fee that
// flows to the block producer. Pre-London: full gasPrice × gasUsed.
// London+: (effectiveGasPrice - baseFee) × gasUsed where effectiveGasPrice
// = min(tx.GasFeeCap, baseFee + tx.Tip).
func computeCoinbaseTip(tx *transaction.Transaction, header *block.Header, gasUsed uint64) *uint256.Int {
	if gasUsed == 0 {
		return new(uint256.Int)
	}
	gasUsedU := uint256.NewInt(gasUsed)

	// London+ (header has BaseFee): tip = (gasPriceEff - baseFee) × gasUsed.
	if header.BaseFee != nil && !header.BaseFee.IsZero() {
		baseFee := header.BaseFee
		tipCap := tx.GasTipCap()
		feeCap := tx.GasFeeCap()
		if tipCap == nil {
			tipCap = new(uint256.Int)
		}
		// effectiveGasPrice = min(feeCap, baseFee + tipCap)
		priority := new(uint256.Int).Add(baseFee, tipCap)
		if feeCap != nil && feeCap.Cmp(priority) < 0 {
			priority.Set(feeCap)
		}
		// subtract baseFee
		if priority.Cmp(baseFee) < 0 {
			// Shouldn't happen (tx validation catches it), but defend.
			return new(uint256.Int)
		}
		perGas := new(uint256.Int).Sub(priority, baseFee)
		return new(uint256.Int).Mul(perGas, gasUsedU)
	}

	// Pre-London: full gasPrice is the producer's tip.
	gasPrice := tx.GasPrice()
	if gasPrice == nil {
		return new(uint256.Int)
	}
	return new(uint256.Int).Mul(gasPrice, gasUsedU)
}

// Compile-time interface assertion.
var _ state.ParallelEVM = (*RealParallelEVM)(nil)

// sanity: force "fmt" import even if unused in shipping code.
var _ = fmt.Sprintf
