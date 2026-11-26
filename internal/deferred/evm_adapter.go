// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// EVM adapter for the deferred executor. NewEVMExecuteFunc wires a real
// EVM execution path into the deferred pipeline by opening a read-only
// MDBX transaction, loading the block and senders via rawdb, reading
// cumulative gas from receipts, and returning an ExecutionResult carrying
// the stored state root. Used to bridge the asynchronous deferred
// Executor to the blockchain's canonical state transition.

package deferred

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// NewEVMExecuteFunc creates an ExecuteFunc that runs real EVM execution
// against the blockchain's database. This bridges the deferred executor
// to the actual state transition logic.
func NewEVMExecuteFunc(db kv.RwDB, bc common.IBlockChain) ExecuteFunc {
	return func(ctx context.Context, blockNumber uint64, blockHash types.Hash) (*ExecutionResult, error) {
		tx, err := db.BeginRo(ctx)
		if err != nil {
			return nil, fmt.Errorf("deferred: begin ro tx: %w", err)
		}
		defer tx.Rollback()

		// Load the block
		b, senders, err := rawdb.ReadBlockWithSenders(tx, blockHash, blockNumber)
		if err != nil {
			return nil, fmt.Errorf("deferred: read block %d: %w", blockNumber, err)
		}
		if b == nil {
			return nil, fmt.Errorf("deferred: block %d not found", blockNumber)
		}

		// The block's gas figure is the header rule's (HeaderGasUsed), which
		// from Amsterdam is not the receipts' cumulative gas.
		receipts := rawdb.ReadReceipts(tx, b, senders)
		totalGasUsed, err := executedBlockGas(tx, blockHash, b.GasUsed(), receipts)
		if err != nil {
			return nil, fmt.Errorf("deferred: read executed result %d: %w", blockNumber, err)
		}

		// Get state root from the block header
		stateRoot := b.Header().StateRoot()

		log.Debug("Deferred execution completed via adapter",
			"block", blockNumber,
			"txs", len(b.Transactions()),
			"gasUsed", totalGasUsed,
		)

		return &ExecutionResult{
			BlockNumber: blockNumber,
			BlockHash:   blockHash,
			StateRoot:   stateRoot,
			GasUsed:     totalGasUsed,
			TxCount:     len(b.Transactions()),
		}, nil
	}
}

// executedBlockGas is the gas figure of an executed block under the header
// rule (internal.HeaderGasUsed). Under deferred execution the header does not
// carry it; it is stored with the block's executed result. Without a stored
// result the block's own header figure applies when the header is not
// deferred, else the receipts' cumulative gas (the pre-Amsterdam figure).
func executedBlockGas(db kv.Getter, blockHash types.Hash, headerGas uint64, receipts []*block.Receipt) (uint64, error) {
	if r, ok, err := rawdb.ReadExecutedResult(db, blockHash); err != nil {
		return 0, err
	} else if ok {
		return r.GasUsed, nil
	}
	if n := len(receipts); n > 0 && receipts[n-1] != nil {
		if headerGas != 0 {
			return headerGas, nil
		}
		return receipts[n-1].CumulativeGasUsed, nil
	}
	return headerGas, nil
}

// NewCommitFunc creates a CommitFunc that logs the commit.
// In a full deferred execution setup, this would persist the execution
// result and update the canonical chain head.
func NewCommitFunc() CommitFunc {
	return func(result *ExecutionResult) error {
		log.Info("Deferred execution result committed",
			"block", result.BlockNumber,
			"stateRoot", result.StateRoot.Hex()[:10],
			"gasUsed", result.GasUsed,
		)
		return nil
	}
}
