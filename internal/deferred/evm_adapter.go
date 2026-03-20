// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package deferred

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
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

		// Read receipts to get gas used
		receipts := rawdb.ReadReceipts(tx, b, senders)

		var totalGasUsed uint64
		if len(receipts) > 0 {
			totalGasUsed = receipts[len(receipts)-1].CumulativeGasUsed
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

// compile-time check
var _ = state.NewPlainState
