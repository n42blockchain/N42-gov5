// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Per-transaction scratch, receipt building and parallelApplyTx.

package internal

import (
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// preparedTx is a transaction's hash, built before the wave, and its
// transfer plan (first: a direct transfer reads the plan and the hash,
// adjacent, and not the message). Pointer-free: its message, when kept,
// is in the preparedSet's msgs.
type preparedTx struct {
	plan transferPlan
	hash types.Hash
	ok   bool
	// msgKept: the preparation kept the message (preparedSet.keptMsg).
	msgKept bool
}

// parallelTxScratch is a Block-STM worker's reusable per-transaction state
// for parallelApplyTx.
type parallelTxScratch struct {
	msg  transaction.Message
	fast fastTransferScratch
	// receipts is the unused tail of the worker's current receipt chunk:
	// one allocation per chunk instead of one per transaction. A chunk
	// stays reachable while any of its receipts is, and all of them belong
	// to the same block.
	receipts []block.Receipt
	chunk    int
	// msgReady says msg already holds this transaction's message (built
	// for a declined direct transfer); parallelApplyTx then skips building
	// it again.
	msgReady bool
}

// newReceipt returns a zero receipt carved from the scratch's chunk. The
// chunks grow from 16 to 256 receipts, so a small block does not pay for
// a large one's chunk.
func (sc *parallelTxScratch) newReceipt() *block.Receipt {
	if len(sc.receipts) == 0 {
		switch {
		case sc.chunk == 0:
			sc.chunk = 16
		case sc.chunk < 256:
			sc.chunk *= 2
		}
		sc.receipts = make([]block.Receipt, sc.chunk)
	}
	r := &sc.receipts[0]
	sc.receipts = sc.receipts[1:]
	return r
}

// fillReceipt builds a transaction's receipt from its execution result,
// carved from the scratch's chunk unless owned (a scratch made for one
// call).
func (sc *parallelTxScratch) fillReceipt(owned bool, tx *transaction.Transaction, txHash types.Hash, result *ExecutionResult, msg *transaction.Message, logs []*block.Log, header *block.Header, txIndex uint, evm vm2.VMInterface) *block.Receipt {
	var receipt *block.Receipt
	if owned {
		receipt = new(block.Receipt)
	} else {
		receipt = sc.newReceipt()
	}
	receipt.Type = tx.Type()
	if result.Failed() {
		receipt.Status = block.ReceiptStatusFailed
	} else {
		receipt.Status = block.ReceiptStatusSuccessful
	}
	receipt.TxHash = txHash
	receipt.GasUsed = result.UsedGas
	if msg.To() == nil {
		receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
	}
	receipt.Logs = logs
	if len(logs) != 0 {
		receipt.SetBloom(block.CreateBloom(block.Receipts{receipt}))
	} // else the zero bloom, which is what CreateBloom returns for no logs
	receipt.BlockNumber = header.Number
	receipt.TransactionIndex = txIndex
	return receipt
}

// transferReceipt is fillReceipt for a successful direct transfer (no
// logs, not a creation) of type txType.
func (sc *parallelTxScratch) transferReceipt(txType uint8, txHash types.Hash, result *ExecutionResult, header *block.Header, txIndex uint) *block.Receipt {
	receipt := sc.newReceipt()
	receipt.Type = txType
	receipt.Status = block.ReceiptStatusSuccessful
	receipt.TxHash = txHash
	receipt.GasUsed = result.UsedGas
	receipt.BlockNumber = header.Number
	receipt.TransactionIndex = txIndex
	return receipt
}

// parallelApplyTx executes a single transaction in the parallel context.
// Similar to applyTransaction but adapted for parallel execution with
// per-tx IntraBlockState and ParallelStateWriter. sc, when not nil, is the
// worker's reusable per-transaction scratch (its contents are overwritten).
func parallelApplyTx(
	config *params.ChainConfig,
	engine consensus.Engine,
	gp *common.GasPool,
	ibs *state.IntraBlockState,
	stateWriter state.StateWriter,
	header *block.Header,
	tx *transaction.Transaction,
	evm vm2.VMInterface,
	cfg vm2.Config,
	sink FeeSink,
	signer transaction.Signer,
	sc *parallelTxScratch,
) (*block.Receipt, uint64, []*block.Log, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, 0, nil, err
	}
	rules := evm.ChainRules()

	// The block's signer, built once per block by the caller (it re-derived
	// the fork rules for every transaction: 0.49 s of a follower's 25 s
	// profile). Nil falls back to building it here.
	if signer == nil {
		signer = transaction.MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time)
	}
	owned := sc == nil
	if owned {
		sc = new(parallelTxScratch)
		defer sc.fast.flush()
	}
	msg := &sc.msg
	if sc.msgReady {
		// The caller built it for the direct transfer attempt.
		sc.msgReady = false
	} else {
		if err := tx.AsMessageInto(msg, signer, header.BaseFee); err != nil {
			return nil, 0, nil, err
		}
		NormalizeExecutionMessage(msg, cfg.StatelessExec, engine != nil)
	}
	txHash := tx.Hash()

	// A plain EOA-to-EOA transfer skips the EVM (transfer_fast_path.go); any
	// other transaction, or one the fast path declines before writing
	// anything, runs the EVM path below in this same incarnation.
	var result *ExecutionResult
	fast := false
	if transferFastPath {
		if e, ok := evm.(*vm2.EVM); ok {
			result, fast = tryFastTransfer(e, ibs, msg, gp, sink, &sc.fast)
		}
	}
	if !fast {
		txContext := NewEVMTxContext(msg)
		evm.Reset(txContext, ibs)

		result, err = ApplyMessageWithFeeSink(evm, msg, gp, true, false, sink)
		if err != nil {
			return nil, 0, nil, err
		}
	}

	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, 0, nil, err
	}

	// Receipt bills the sender (post-refund); the block accumulates
	// EIP-7778 BlockGasUsed (pre-refund, floor-clamped) under Glamsterdam
	// so the parallel path validates against the same header.GasUsed the
	// serial miner/importer produce.
	blockGasUsed := result.BlockGasUsed
	logs := ibs.GetLogs(txHash)

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = sc.fillReceipt(owned, tx, txHash, result, msg, logs, header, uint(ibs.TxIndex()), evm)
	}

	return receipt, blockGasUsed, logs, nil
}
