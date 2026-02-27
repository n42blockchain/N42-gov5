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

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/ethdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	"github.com/holiman/uint256"
)

// StateProcessor implements Processor and handles state transitions.
type StateProcessor struct {
	config *params.ChainConfig
	bc     *BlockChain
	engine consensus.Engine
}

// NewStateProcessor initialises a new StateProcessor.
func NewStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine) *StateProcessor {
	return &StateProcessor{
		config: config,
		bc:     bc,
		engine: engine,
	}
}

// Process processes the state changes according to the Ethereum rules by running
// the transaction messages and applying rewards. Returns receipts, unpaid rewards,
// logs, and total gas used.
func (p *StateProcessor) Process(b *block.Block, ibs *state.IntraBlockState, stateReader state.StateReader, stateWriter state.WriterWithChangeSets, blockHashFunc func(n uint64) types.Hash) (block.Receipts, map[types.Address]*uint256.Int, []*block.Log, uint64, error) {
	concreteHeader, ok := b.Header().(*block.Header)
	if !ok {
		return nil, nil, nil, 0, fmt.Errorf("Process: invalid header type assertion for block %v", b.Number64())
	}

	usedGas := new(uint64)
	gp := new(common.GasPool)
	gp.AddGas(b.GasLimit())

	var (
		rejectedTxs []*RejectedTx
		includedTxs transaction.Transactions
		receipts    block.Receipts
	)

	chainConfig := p.config
	cfg := vm2.Config{}

	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(b.Number64().ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}
	noop := state.NewNoopWriter()

	for i, tx := range b.Transactions() {
		ibs.Prepare(tx.Hash(), b.Hash(), i)
		receipt, _, err := ApplyTransaction(chainConfig, blockHashFunc, p.engine, nil, gp, ibs, noop, concreteHeader, tx, usedGas, cfg)
		if err != nil {
			if !cfg.StatelessExec {
				return nil, nil, nil, 0, fmt.Errorf("could not apply tx %d from block %d [%v]: %w", i, b.Number64(), tx.Hash().String(), err)
			}
			rejectedTxs = append(rejectedTxs, &RejectedTx{i, err.Error()})
		} else {
			includedTxs = append(includedTxs, tx)
			if !cfg.NoReceipts {
				receipts = append(receipts, receipt)
			}
		}
	}

	if !cfg.StatelessExec && *usedGas != concreteHeader.GasUsed {
		return nil, nil, nil, 0, fmt.Errorf("gas used by execution: %d, in header: %d", *usedGas, concreteHeader.GasUsed)
	}

	var nopay map[types.Address]*uint256.Int
	if !cfg.ReadOnly {
		txs := b.Transactions()
		var err error
		_, nopay, err = p.engine.Finalize(p.bc, concreteHeader, ibs, txs, nil)
		if err != nil {
			return nil, nil, nil, 0, err
		}
	}

	// Suppress unused variable warnings
	_ = rejectedTxs
	_ = includedTxs

	return receipts, nopay, ibs.Logs(), *usedGas, nil
}

// applyTransaction applies a single transaction and returns its receipt.
func applyTransaction(config *params.ChainConfig, engine consensus.Engine, gp *common.GasPool, ibs *state.IntraBlockState, stateWriter state.StateWriter, header *block.Header, tx *transaction.Transaction, usedGas *uint64, evm vm2.VMInterface, cfg vm2.Config) (*block.Receipt, []byte, error) {
	rules := evm.ChainRules()

	msg, err := tx.AsMessage(transaction.MakeSigner(config, header.Number.ToBig()), header.BaseFee)
	if err != nil {
		return nil, nil, err
	}
	msg.SetCheckNonce(!cfg.StatelessExec)

	if msg.FeeCap().IsZero() && engine != nil {
		msg.SetIsFree(false)
	}

	txContext := NewEVMTxContext(msg)
	if cfg.TraceJumpDest {
		txContext.TxHash = tx.Hash()
	}

	evm.Reset(txContext, ibs)

	result, err := ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return nil, nil, err
	}
	if err = ibs.FinalizeTx(rules, stateWriter); err != nil {
		return nil, nil, err
	}
	*usedGas += result.UsedGas

	var receipt *block.Receipt
	if !cfg.NoReceipts {
		receipt = &block.Receipt{Type: tx.Type(), CumulativeGasUsed: *usedGas}
		if result.Failed() {
			receipt.Status = block.ReceiptStatusFailed
		} else {
			receipt.Status = block.ReceiptStatusSuccessful
		}

		receipt.TxHash = tx.Hash()
		receipt.GasUsed = result.UsedGas
		if msg.To() == nil {
			receipt.ContractAddress = crypto.CreateAddress(evm.TxContext().Origin, tx.Nonce())
		}
		receipt.Logs = ibs.GetLogs(tx.Hash())
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = uint(ibs.TxIndex())
	}

	return receipt, result.ReturnData, err
}

// ApplyTransaction creates the EVM environment and applies a transaction.
func ApplyTransaction(config *params.ChainConfig, blockHashFunc func(n uint64) types.Hash, engine consensus.Engine, author *types.Address, gp *common.GasPool, ibs *state.IntraBlockState, stateWriter state.StateWriter, header *block.Header, tx *transaction.Transaction, usedGas *uint64, cfg vm2.Config) (*block.Receipt, []byte, error) {
	blockContext := NewEVMBlockContext(header, blockHashFunc, engine, author)
	vmenv := vm2.NewEVM(blockContext, evmtypes.TxContext{}, ibs, config, cfg)

	return applyTransaction(config, engine, gp, ibs, stateWriter, header, tx, usedGas, vmenv, cfg)
}

// NewStateReaderWriter creates a new state reader and writer pair.
func NewStateReaderWriter(batch ethdb.Database, tx kv.RwTx, blockNumber uint64, writeChangeSets bool) (state.StateReader, state.WriterWithChangeSets, error) {
	stateReader := state.NewPlainStateReader(tx)

	var stateWriter state.WriterWithChangeSets
	if writeChangeSets {
		stateWriter = state.NewPlainStateWriter(batch, tx, blockNumber)
	} else {
		stateWriter = state.NewPlainStateWriterNoHistory(batch)
	}
	return stateReader, stateWriter, nil
}
