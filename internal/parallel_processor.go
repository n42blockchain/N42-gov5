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

package internal

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// parallelTxResult stores per-transaction execution output from parallel execution.
type parallelTxResult struct {
	receipt *block.Receipt
	gasUsed uint64
	err     error
	logs    []*block.Log
}

// ProcessParallel executes all transactions in the block using Block-STM
// wave-based parallel execution. It falls back to sequential execution
// when the block has too few transactions or when parallel execution
// detects excessive conflicts.
//
// The approach:
//  1. Execute all txs in parallel, each with its own IntraBlockState and
//     ParallelStateReader (reads from MVS) / ParallelStateWriter (captures writes).
//  2. Validate read sets using the wave-based executor.
//  3. After validation, replay the validated state changes to the real
//     IntraBlockState (ibs) so that CommitBlock can flush them to the DB.
//
// Note: the stateWriter parameter may be a NoopWriter (from evmRecord). All
// state changes MUST be applied to ibs, which CommitBlock later flushes to
// the real database writer.
func (p *StateProcessor) ProcessParallel(b *block.Block, ibs *state.IntraBlockState, stateReader state.StateReader, stateWriter state.WriterWithChangeSets, blockHashFunc func(n uint64) types.Hash) (block.Receipts, map[types.Address]*uint256.Int, []*block.Log, uint64, error) {
	txs := b.Transactions()
	numTxs := len(txs)

	// Fall back to sequential for small blocks (overhead not worth it).
	if numTxs <= 4 {
		return p.Process(b, ibs, stateReader, stateWriter, blockHashFunc)
	}

	concreteHeader, ok := b.Header().(*block.Header)
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("ProcessParallel: invalid header type assertion for block %v", b.Number64())
	}

	chainConfig := p.config
	cfg := vm2.Config{}
	if err := ProcessPragueBlockStart(chainConfig, ibs, concreteHeader); err != nil {
		return nil, nil, nil, 0, err
	}
	// blockContext is a value type — each goroutine's NewEVM copies it, safe to share.
	blockContext := NewEVMBlockContext(concreteHeader, blockHashFunc, p.engine, nil)

	// Per-tx result storage. Each goroutine writes to its own index (no race).
	txResults := make([]parallelTxResult, numTxs)

	// Use var + assignment to allow the closure to reference executor via capture.
	// When the closure executes (during executor.Run), executor is already assigned.
	var executor *parallel.Executor
	executor = parallel.NewExecutor(numTxs, 0, func(txIndex int, rw *parallel.ReadWriteSet) error {
		tx := txs[txIndex]

		// Each tx gets its own IntraBlockState with MVS-backed reader.
		// NOTE: stateReader (base reader) must be safe for concurrent reads.
		// PlainStateReader wraps a kv.Tx (read-only MDBX transaction) which
		// supports concurrent reads from multiple goroutines.
		pReader := parallel.NewParallelStateReader(stateReader, executor.MVS(), rw, txIndex)
		txIBS := state.New(pReader)
		pWriter := parallel.NewParallelStateWriter(rw)

		txIBS.Prepare(tx.Hash(), b.Hash(), txIndex)

		// Each tx gets its own gas pool (block-level gas validation happens after).
		gp := new(common.GasPool)
		gp.AddGas(b.GasLimit())

		vmenv := vm2.NewEVM(blockContext, evmtypes.TxContext{}, txIBS, chainConfig, cfg)

		receipt, gasUsed, logs, err := parallelApplyTx(chainConfig, p.engine, gp, txIBS, pWriter, concreteHeader, tx, vmenv, cfg)

		txResults[txIndex] = parallelTxResult{
			receipt: receipt,
			gasUsed: gasUsed,
			err:     err,
			logs:    logs,
		}

		return err
	})

	// Run parallel execution with wave-based validation.
	results := executor.Run()

	// Check if any transaction had a hard error.
	for i, r := range results {
		if r.Err != nil {
			return nil, nil, nil, 0, fmt.Errorf("could not apply tx %d from block %d [%v]: %w",
				i, b.Number64(), txs[i].Hash().String(), r.Err)
		}
	}

	// Collect receipts and logs, fixing cumulative gas.
	usedGas := uint64(0)
	var receipts block.Receipts
	var allLogs []*block.Log

	for i := 0; i < numTxs; i++ {
		tr := txResults[i]
		usedGas += tr.gasUsed

		if tr.receipt != nil {
			tr.receipt.CumulativeGasUsed = usedGas
			receipts = append(receipts, tr.receipt)
		}

		allLogs = append(allLogs, tr.logs...)
	}

	// Apply MVS final state to the real IntraBlockState.
	// This is critical: the caller (evmRecord) expects ibs to contain all state
	// changes. writeBlockWithState later calls ibs.CommitBlock() to flush to DB.
	if err := applyMVSToIBS(executor.MVS(), numTxs, ibs); err != nil {
		return nil, nil, nil, 0, fmt.Errorf("ProcessParallel: failed to apply MVS state: %w", err)
	}

	// Validate total gas used.
	if usedGas != concreteHeader.GasUsed {
		return nil, nil, nil, 0, fmt.Errorf("gas used by execution: %d, in header: %d", usedGas, concreteHeader.GasUsed)
	}
	if _, err := ProcessPragueSystemCalls(p.config, ibs, concreteHeader, p.engine); err != nil {
		return nil, nil, nil, 0, err
	}

	// Finalize block (rewards, etc.) — operates on ibs which now has all changes.
	var nopay map[types.Address]*uint256.Int
	var err error
	_, nopay, err = p.engine.Finalize(p.bc, concreteHeader, ibs, txs, nil)
	if err != nil {
		return nil, nil, nil, 0, err
	}

	return receipts, nopay, allLogs, usedGas, nil
}

// parallelApplyTx executes a single transaction in the parallel context.
// Similar to applyTransaction but adapted for parallel execution with
// per-tx IntraBlockState and ParallelStateWriter.
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
) (*block.Receipt, uint64, []*block.Log, error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, 0, nil, err
	}
	rules := evm.ChainRules()

	msg, err := tx.AsMessage(transaction.MakeSignerWithTimestamp(config, headerNumber.ToBig(), header.Time), header.BaseFee)
	if err != nil {
		return nil, 0, nil, err
	}
	NormalizeExecutionMessage(&msg, cfg.StatelessExec, engine != nil)

	txContext := NewEVMTxContext(msg)
	evm.Reset(txContext, ibs)

	result, err := ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return nil, 0, nil, err
	}

	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, 0, nil, err
	}

	gasUsed := result.UsedGas
	logs := ibs.GetLogs(tx.Hash())

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = &block.Receipt{Type: tx.Type()}
		if result.Failed() {
			receipt.Status = block.ReceiptStatusFailed
		} else {
			receipt.Status = block.ReceiptStatusSuccessful
		}
		receipt.TxHash = tx.Hash()
		receipt.GasUsed = gasUsed
		if msg.To() == nil {
			receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
		}
		receipt.Logs = logs
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = uint(ibs.TxIndex())
	}

	return receipt, gasUsed, logs, nil
}

// applyMVSToIBS replays the final validated MVS state to the real IntraBlockState.
// Uses a two-pass approach:
//  1. Process FieldBalance entries to establish account existence/deletion.
//  2. Process FieldStorage and FieldCode for accounts that exist in the final state.
//
// This ordering ensures that storage/code changes are only applied to accounts
// that actually exist, preventing phantom account creation for deleted accounts.
func applyMVSToIBS(mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) error {
	// Collect all MVS entries, separated by type.
	type accountEntry struct {
		addr  types.Address
		value []byte // nil = deleted
	}
	type storageEntry struct {
		addr  types.Address
		slot  types.Hash
		value []byte
	}
	type codeEntry struct {
		addr types.Address
		code []byte
	}

	var accounts []accountEntry
	var storages []storageEntry
	var codes []codeEntry

	// Single pass to collect and categorize all MVS entries.
	if err := mvs.ApplyAll(numTxs, func(key parallel.LocationKey, value []byte) error {
		switch key.Field {
		case parallel.FieldBalance:
			// FieldBalance stores the full protobuf-encoded StateAccount
			// (balance + nonce + codeHash + incarnation), not just balance.
			accounts = append(accounts, accountEntry{addr: key.Address, value: value})
		case parallel.FieldStorage:
			storages = append(storages, storageEntry{addr: key.Address, slot: key.Slot, value: value})
		case parallel.FieldCode:
			if value != nil {
				codes = append(codes, codeEntry{addr: key.Address, code: value})
			}
		}
		// Other fields (CodeHash, CodeSize, Suicide, Exist, Incarnation) are
		// auxiliary — the IBS handles them internally via SetBalance/SetCode/etc.
		return nil
	}); err != nil {
		return err
	}

	// Track which accounts are deleted in the final state.
	deletedAccounts := make(map[types.Address]bool)

	// Pass 1: Apply account-level changes.
	for _, ae := range accounts {
		if ae.value == nil {
			// Account was deleted (selfdestructed).
			// Selfdestruct handles accounts that exist in base state;
			// for accounts created and deleted within the same block
			// (not in base state), Selfdestruct is a safe no-op.
			ibs.Selfdestruct(ae.addr)
			deletedAccounts[ae.addr] = true
		} else {
			acc, err := parallel.DecodeAccount(ae.value)
			if err != nil {
				return fmt.Errorf("decode account %v: %w", ae.addr, err)
			}
			ibs.SetBalance(ae.addr, &acc.Balance)
			ibs.SetNonce(ae.addr, acc.Nonce)
			if acc.Incarnation > 0 {
				ibs.SetIncarnation(ae.addr, acc.Incarnation)
			}
		}
	}

	// Pass 2: Apply storage changes (skip deleted accounts).
	for _, se := range storages {
		if deletedAccounts[se.addr] {
			continue // storage of deleted accounts is discarded
		}
		var val uint256.Int
		if len(se.value) > 0 {
			val.SetBytes(se.value)
		}
		slot := se.slot
		ibs.SetState(se.addr, &slot, val)
	}

	// Pass 3: Apply code changes (skip deleted accounts).
	for _, ce := range codes {
		if deletedAccounts[ce.addr] {
			continue
		}
		ibs.SetCode(ce.addr, ce.code)
	}

	return nil
}
