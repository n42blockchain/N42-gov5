// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// process.go — shared per-block execution core.
//
// ProcessBlock takes a header, a slice of transactions, uncles, and an
// IntraBlockState, then drives the EVM through every transaction using
// the chain config fork rules and the consensus engine's Finalize hook.
// Pre-computed senders can be injected to skip ecrecover. The returned
// BlockResult carries gas used, receipts and senders. The function is
// shared by the batch Executor and the Engine API adapter so both paths
// produce byte-identical receipts and state transitions.

package ethel

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/log"
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

func applyEthelWithdrawals(chainCfg *params.ChainConfig, header *block.Header, ibs *state.IntraBlockState, withdrawals []*Withdrawal) {
	if chainCfg == nil || header == nil || header.Number == nil || ibs == nil || len(withdrawals) == 0 {
		return
	}
	if !chainCfg.IsShanghaiAt(header.Number.Uint64(), header.Time) {
		return
	}
	gwei := uint256.NewInt(params.GWei)
	for _, withdrawal := range withdrawals {
		if withdrawal == nil || withdrawal.Amount == 0 {
			continue
		}
		amount := new(uint256.Int).Mul(uint256.NewInt(withdrawal.Amount), gwei)
		if amount.IsZero() {
			continue
		}
		ibs.AddBalance(withdrawal.Address, amount)
	}
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
//   - withdrawals: EIP-4895 withdrawals, nil before Shanghai
//   - ibs: in-memory state database
//   - blockHashFunc: BLOCKHASH opcode implementation
//
// ProcessBlock executes all transactions in a block.
// If precomputedSenders is provided (non-nil), senders are injected directly
// into transactions via SetFrom, skipping expensive ecrecover.
func ProcessBlock(
	chainCfg *params.ChainConfig,
	engine consensus.Engine,
	header *block.Header,
	txs []*transaction.Transaction,
	uncles []block.IHeader,
	withdrawals []*Withdrawal,
	ibs *state.IntraBlockState,
	blockHashFunc func(uint64) types.Hash,
	precomputedSenders []types.Address,
	stateWriter ...state.StateWriter,
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
	if err := iinternal.ProcessExecutionBlockStart(header.ParentBeaconRoot, chainCfg, ibs, header, engine); err != nil {
		return nil, err
	}

	blockHash := header.Hash()

	var receipts block.Receipts
	senders := make([]types.Address, 0, len(txs))

	if precomputedSenders != nil {
		for i, txn := range txs {
			if i < len(precomputedSenders) {
				txn.SetFrom(precomputedSenders[i])
			}
		}
	}

	signer := transaction.MakeSigner(chainCfg, header.Number.ToBig())

	noop := state.NewNoopWriter()

	for i, txn := range txs {
		ibs.Prepare(txn.Hash(), blockHash, i)
		receipt, _, err := iinternal.ApplyTransaction(
			chainCfg, blockHashFunc, engine, nil, gp,
			ibs, noop, header, txn, usedGas, cfg,
		)
		if err != nil {
			if errors.Is(err, common.ErrGasLimitReached) {
				log.Error("Gas pool exhausted",
					"block", header.Number.Uint64(),
					"txIndex", i,
					"txHash", txn.Hash().Hex(),
					"txGasLimit", txn.Gas(),
					"poolRemaining", gp.Gas(),
					"usedGasSoFar", *usedGas,
					"blockGasLimit", header.GasLimit,
					"totalTxs", len(txs),
				)
				// Return partial result so caller can diff against geth receipts.
				return &BlockResult{GasUsed: *usedGas, Receipts: receipts, Senders: senders},
					fmt.Errorf("tx %d: %w", i, err)
			}
			return nil, fmt.Errorf("tx %d: %w", i, err)
		}
		if receipt != nil {
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

	applyEthelWithdrawals(chainCfg, header, ibs, withdrawals)

	// Post-block system calls depend on the receipts emitted by executed transactions.
	if _, err := iinternal.ProcessExecutionBlockEnd(receipts, chainCfg, ibs, header, engine); err != nil {
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
