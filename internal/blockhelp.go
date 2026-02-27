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
// Package core implements the Ethereum consensus protocol.

package internal

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type SyncMode string

const (
	TriesInMemory = 128
)

type RejectedTx struct {
	Index int    `json:"index"    gencodec:"required"`
	Err   string `json:"error"    gencodec:"required"`
}

type RejectedTxs []*RejectedTx

type EphemeralExecResult struct {
	StateRoot        types.Hash            `json:"stateRoot"`
	TxRoot           types.Hash            `json:"txRoot"`
	ReceiptRoot      types.Hash            `json:"receiptsRoot"`
	LogsHash         types.Hash            `json:"logsHash"`
	Bloom            types.Bloom           `json:"logsBloom"        gencodec:"required"`
	Receipts         block.Receipts        `json:"receipts"`
	Rejected         RejectedTxs           `json:"rejected,omitempty"`
	Difficulty       *math.HexOrDecimal256 `json:"currentDifficulty" gencodec:"required"`
	GasUsed          math.HexOrDecimal64   `json:"gasUsed"`
	StateSyncReceipt *block.Receipt        `json:"-"`
}

// SysCallContract executes a system call to a contract using the system address as sender.
func SysCallContract(contract types.Address, data []byte, chainConfig params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) (result []byte, err error) {
	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(header.Number64().ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	msg := transaction.NewMessage(
		state.SystemAddress,
		&contract,
		0, u256.Num0,
		math.MaxUint64, u256.Num0,
		nil, nil,
		data, nil, false,
		true, // isFree
	)
	vmConfig := vm.Config{NoReceipts: true}

	isBor := chainConfig.Bor != nil
	var txContext evmtypes.TxContext
	var author *types.Address
	if isBor {
		author = &header.Coinbase
		txContext = evmtypes.TxContext{}
	} else {
		author = &state.SystemAddress
		txContext = NewEVMTxContext(msg)
	}

	blockContext := NewEVMBlockContext(header, GetHashFn(header, nil), engine, author)
	evm := vm.NewEVM(blockContext, txContext, ibs, &chainConfig, vmConfig)

	ret, _, err := evm.Call(
		vm.AccountRef(msg.From()),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		msg.Value(),
		false,
	)
	if isBor && err != nil {
		return nil, nil
	}
	return ret, err
}

// FinalizeBlockExecution finalizes block execution by running engine finalization
// and committing state changes.
func FinalizeBlockExecution(engine consensus.Engine, header *block.Header,
	txs transaction.Transactions, stateWriter state.WriterWithChangeSets, cc *params.ChainConfig, ibs *state.IntraBlockState,
	receipts block.Receipts, headerReader consensus.ChainHeaderReader, isMining bool) (newBlock block.IBlock, newTxs transaction.Transactions, newReceipt block.Receipts, err error) {

	if isMining {
		newBlock, _, _, err = engine.FinalizeAndAssemble(headerReader, header, ibs, txs, nil, receipts)
	} else {
		_, _, err = engine.Finalize(headerReader, header, ibs, txs, nil)
	}
	if err != nil {
		return nil, nil, nil, err
	}

	if err := ibs.CommitBlock(cc.Rules(header.Number.Uint64()), stateWriter); err != nil {
		return nil, nil, nil, fmt.Errorf("committing block %d failed: %w", header.Number.Uint64(), err)
	}
	if err := stateWriter.WriteChangeSets(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing changesets for block %d failed: %w", header.Number.Uint64(), err)
	}
	if err := stateWriter.WriteHistory(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing history for block %d failed: %w", header.Number.Uint64(), err)
	}

	return newBlock, newTxs, newReceipt, nil
}
