// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// evm_bridge.go: skeleton for plugging a real EVM into the Block-STM
// parallel executor. Defines the interface between the scheduler/MV
// pipeline (which speaks in MVStateView + ApplyTarget) and an EVM
// instance that wants to execute one transaction.
//
// Phase 3 status:
//   - Interface: STABLE (this file).
//   - Implementation: NOT YET — Phase 3 Part 7 wires n42's vm.EVM and
//     IntraBlockState behind ParallelEVM. Until then, ParallelEVM is
//     a Mock that returns deterministic synthetic outputs, used to
//     unit-test the wiring shape.
//
// Why a skeleton first:
//   - Forces the abstraction boundary to be explicit before EVM
//     integration starts.
//   - Lets us exercise the full ExecuteBlockParallel + EVMBridge +
//     FinalizeBlock + Apply path with a mock that's bit-for-bit
//     deterministic — catches integration bugs without dragging in
//     EVM complexity.
//   - The real EVM integration becomes "implement these 3 methods"
//     instead of "rewrite IntraBlockState".

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// BlockContext carries the immutable per-block facts that every tx
// execution needs. Equivalent to vm.BlockContext minus state-reader
// references (state is provided per-tx via EVMStateView).
type BlockContext struct {
	Coinbase    types.Address
	BlockNumber uint64
	Time        uint64
	GasLimit    uint64
	BaseFee     *uint256.Int   // EIP-1559; nil pre-London
	Difficulty  *uint256.Int   // pre-Merge; post-Merge: prev randao
	BlobBaseFee *uint256.Int   // EIP-4844; nil pre-Cancun
	BlobHashes  []types.Hash   // EIP-4844 blob versioned hashes
	// BlockHash supplies BLOCKHASH opcode lookup. Implementations
	// must be safe for concurrent calls (parallel txs may invoke).
	BlockHash func(uint64) types.Hash
}

// ParallelEVM executes a single transaction against an EVMStateView
// and produces a TxOutput summarizing the side effects that the
// commit phase needs (gas, logs, coinbase tip, status).
//
// Contract:
//   - Reads/writes to state MUST go through `view`. The EVM must NOT
//     touch any other state object — concurrent txs would race.
//   - The implementation is responsible for catching panics and
//     converting to errors; the scheduler must not panic on bad txs.
//   - If `view.AbortPending()` is true after any read, the EVM MUST
//     return early (best-effort) so the scheduler can re-execute.
//     Returning a TxOutput with err=ErrAbortRetry signals this.
//   - On gas exhaustion / revert, return the partial output with the
//     gas used and an Err describing the failure. Status=0 vs 1 is
//     set by the implementation per Ethereum receipt semantics.
type ParallelEVM interface {
	// Execute runs `tx` (with sender pre-recovered) against `view`.
	// `block` is the per-block context.
	Execute(
		tx *transaction.Transaction,
		sender types.Address,
		view *EVMStateView,
		block *BlockContext,
	) (TxOutput, error)
}

// ErrAbortRetry signals that the executor saw an MVEstimate read and
// the scheduler should re-execute this tx. Returned from ParallelEVM
// implementations, NOT from the scheduler. The parallel executor
// recognizes this error and re-queues without bumping incarnation.
type errAbortRetry struct{}

func (errAbortRetry) Error() string { return "parallel EVM: abort and retry" }

// IsAbortRetry reports whether err is the abort-retry sentinel.
func IsAbortRetry(err error) bool {
	_, ok := err.(errAbortRetry)
	return ok
}

// AbortRetry returns the sentinel error.
func AbortRetry() error { return errAbortRetry{} }

// ParallelTxRunner adapts ParallelEVM into the TxExecutor closure
// the scheduler expects. Wires the per-tx (txIdx → tx + sender)
// lookup and bridges the view through.
//
// txs[i] and senders[i] must correspond to scheduler tx i.
func ParallelTxRunner(
	evm ParallelEVM,
	txs []*transaction.Transaction,
	senders []types.Address,
	block *BlockContext,
	outputs []TxOutput, // pre-allocated slice the runner fills (one entry per txIdx)
) func(txIdx int) TxExecutor {
	return func(txIdx int) TxExecutor {
		return func(view *MVStateView) error {
			ev := NewEVMStateView(view)
			out, err := evm.Execute(txs[txIdx], senders[txIdx], ev, block)
			if IsAbortRetry(err) {
				// Don't record output; let scheduler retry this incarnation.
				return nil
			}
			if err != nil {
				// Real error: surface via TxOutput.Err so commit phase
				// still records a failed receipt.
				out.TxIdx = txIdx
				out.Err = err
				outputs[txIdx] = out
				return err
			}
			out.TxIdx = txIdx
			outputs[txIdx] = out
			return nil
		}
	}
}

// --- MockEVM: deterministic stub for unit tests ---

// MockEVM implements ParallelEVM with simple synthetic logic:
//   - reads tx.To's account
//   - increments tx.To's storage slot 0 (simulating an ERC20 balance bump)
//   - returns 21000 gas + a tip of 1 wei
//   - status=1, no logs
//
// Used in evm_bridge_test.go and parallel-evm-demo for shape testing.
type MockEVM struct{}

func (MockEVM) Execute(
	tx *transaction.Transaction,
	sender types.Address,
	view *EVMStateView,
	block *BlockContext,
) (TxOutput, error) {
	if to := tx.To(); to != nil {
		// Read account (consumes a readSet entry).
		if _, err := view.ReadAccount(*to); err != nil {
			return TxOutput{}, err
		}
		if view.Inner().AbortPending() {
			return TxOutput{}, AbortRetry()
		}
		// Read+write slot 0.
		slot := types.Hash{}
		cur, err := view.ReadStorage(*to, slot)
		if err != nil {
			return TxOutput{}, err
		}
		if view.Inner().AbortPending() {
			return TxOutput{}, AbortRetry()
		}
		view.WriteStorage(*to, slot, cur.AddUint64(cur, 1))
	}
	return TxOutput{
		GasUsed:     21000,
		CoinbaseTip: uint256.NewInt(1),
		Status:      1,
	}, nil
}
