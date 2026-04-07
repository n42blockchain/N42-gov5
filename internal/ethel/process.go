// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	iinternal "github.com/n42blockchain/N42/internal"
)

// BlockResult holds per-block execution outputs.
type BlockResult struct {
	GasUsed  uint64
	Receipts block.Receipts
	Senders  []types.Address
}

// ProcessBlock executes all transactions in a block against the given state.
// This is the shared core between the batch Executor and the Engine API adapter.
//
// Parameters:
//   - chainCfg: chain configuration with fork rules
//   - engine: consensus engine (for Author, Finalize)
//   - header: block header being executed
//   - txs: transactions to execute
//   - uncles: uncle headers (for reward calculation, nil post-merge)
//   - ibs: in-memory state database
//   - blockHashFunc: BLOCKHASH opcode implementation
// ProcessBlock executes all transactions in a block.
// If precomputedSenders is provided (non-nil), senders are injected directly
// into transactions via SetFrom, skipping expensive ecrecover.
func ProcessBlock(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	header *block.Header,
	txs []*transaction.Transaction,
	uncles []block.IHeader,
	ibs *state.IntraBlockState,
	blockHashFunc func(uint64) types.Hash,
	precomputedSenders []types.Address,
) (*BlockResult, error) {
	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(header.GasLimit)

	cfg := vm2.Config{}

	// DAO fork.
	if chainCfg.DAOForkSupport && chainCfg.DAOForkBlock != nil &&
		chainCfg.DAOForkBlock.Cmp(header.Number.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	// Pre-block system calls (beacon root, Prague system contracts, etc.).
	if err := iinternal.ProcessExecutionBlockStart(nil, chainCfg, ibs, header, engine); err != nil {
		return nil, err
	}

	noop := state.NewNoopWriter()
	blockHash := header.Hash()

	var receipts block.Receipts
	senders := make([]types.Address, 0, len(txs))

	// Inject pre-computed senders to skip ecrecover in AsMessage().
	if precomputedSenders != nil {
		for i, txn := range txs {
			if i < len(precomputedSenders) {
				txn.SetFrom(precomputedSenders[i])
			}
		}
	}

	signer := transaction.MakeSigner(chainCfg, header.Number.ToBig())

	// Pre-Byzantium receipt PostState requires per-tx IntermediateRoot.
	// Only compute when a real RootComputer is set (verify blocks with TrieRootComputer).
	preByzantium := !chainCfg.IsByzantium(header.Number.Uint64())
	computePostState := preByzantium && ibs.HasRootComputer()

	for i, txn := range txs {
		ibs.Prepare(txn.Hash(), blockHash, i)
		receipt, _, err := iinternal.ApplyTransaction(
			chainCfg, blockHashFunc, engine, nil, gp,
			ibs, noop, header, txn, usedGas, cfg,
		)
		if err != nil {
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		if receipt != nil {
			if computePostState {
				root := ibs.IntermediateRoot()
				receipt.PostState = root[:]
			}
			receipts = append(receipts, receipt)
		}

		// Use pre-computed sender or recover from signature.
		if precomputedSenders != nil && i < len(precomputedSenders) {
			senders = append(senders, precomputedSenders[i])
		} else {
			sender, err := transaction.Sender(signer, txn)
			if err != nil {
				return nil, fmt.Errorf("tx %d sender: %w", i, err)
			}
			senders = append(senders, sender)
		}
	}

	// Post-block system calls.
	if _, err := iinternal.ProcessExecutionBlockEnd(nil, chainCfg, ibs, header, engine); err != nil {
		return nil, err
	}

	// Finalize (block rewards + uncle rewards).
	// Skip for genesis block (block 0) — Ethereum genesis has no execution
	// and no coinbase reward. Its state root is purely from the alloc.
	if header.Number.Uint64() > 0 {
		if _, _, err := engine.Finalize(nil, header, ibs, txs, uncles); err != nil {
			return nil, fmt.Errorf("finalize: %w", err)
		}
	}

	return &BlockResult{GasUsed: *usedGas, Receipts: receipts, Senders: senders}, nil
}
