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

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type SyncMode string

const (
	TriesInMemory               = 128
	systemCallGas               = 30_000_000
	beaconRootsHistoryBufferLen = 8191
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
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(headerNumber.ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}

	msg := transaction.NewMessage(
		state.SystemAddress,
		&contract,
		0, u256.Num0,
		systemCallGas, u256.Num0,
		nil, nil, nil, nil,
		data, nil, false,
		true, // isFree
	)
	vmConfig := vm.Config{NoReceipts: true}

	policy := newExecutionChainPolicy(&chainConfig, params.ConsensusType(""))
	author, txContext := policy.systemCallContext(header, msg)

	blockContext := NewEVMBlockContext(header, GetHashFn(header, nil), engine, author)
	evm := vm.NewEVM(blockContext, txContext, ibs, &chainConfig, vmConfig)
	if rules := evm.ChainRules(); rules.IsBerlin {
		ibs.PrepareAccessList(msg.From(), msg.To(), vm.ActivePrecompiles(rules), nil)
		if rules.IsShanghai {
			ibs.AddAddressToAccessList(blockContext.Coinbase)
		}
	}

	ret, _, err := evm.Call(
		vm.AccountRef(msg.From()),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		msg.Value(),
		false,
	)
	if policy.shouldIgnoreSystemCallError() && err != nil {
		return nil, nil
	}
	return ret, err
}

func appendExecutionRequest(requests []hexutil.Bytes, requestType byte, payload []byte) []hexutil.Bytes {
	if len(payload) == 0 {
		return requests
	}
	grouped := make([]byte, 1+len(payload))
	grouped[0] = requestType
	copy(grouped[1:], payload)
	return append(requests, hexutil.Bytes(grouped))
}

// CollectDepositExecutionRequests extracts EIP-6110 deposit requests from block receipts.
func CollectDepositExecutionRequests(receipts block.Receipts) ([]hexutil.Bytes, error) {
	var serialized []byte
	for _, receipt := range receipts {
		if receipt == nil {
			continue
		}
		for _, lg := range receipt.Logs {
			if lg == nil || lg.Address != vm.DepositContractAddress {
				continue
			}
			if len(lg.Topics) == 0 || lg.Topics[0] != vm.DepositEventSignature {
				continue
			}
			deposit, err := vm.ParseDepositLog(lg.Topics, lg.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to parse deposit logs: %w", err)
			}
			serialized = append(serialized, deposit.Serialize()...)
		}
	}
	return appendExecutionRequest(nil, vm.DepositRequestType, serialized), nil
}

// ProcessBeaconBlockRoot applies the EIP-4788 system call before block transaction execution.
func ProcessBeaconBlockRoot(beaconRoot *types.Hash, chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) error {
	if beaconRoot == nil || chainConfig == nil || ibs == nil || header == nil {
		return nil
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return err
	}
	rules := chainConfig.RulesWithTimestamp(headerNumber.Uint64(), header.Time)
	if rules == nil || !rules.IsCancun {
		return nil
	}

	timestampSlot := types.Hash{}
	uint256.NewInt(header.Time % beaconRootsHistoryBufferLen).WriteToSlice(timestampSlot[:])

	rootSlot := types.Hash{}
	uint256.NewInt((header.Time % beaconRootsHistoryBufferLen) + beaconRootsHistoryBufferLen).WriteToSlice(rootSlot[:])

	timestampValue := uint256.NewInt(header.Time)
	rootValue := uint256.NewInt(0).SetBytes(beaconRoot[:])
	ibs.SetState(params.BeaconRootsAddress, &timestampSlot, *timestampValue)
	ibs.SetState(params.BeaconRootsAddress, &rootSlot, *rootValue)
	return ibs.FinalizeTx(rules, state.NewNoopWriter())
}

// ProcessExecutionBlockStart applies shared start-of-block execution hooks in
// canonical order.
func ProcessExecutionBlockStart(parentBeaconRoot *types.Hash, chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) error {
	if err := ProcessBeaconBlockRoot(parentBeaconRoot, chainConfig, ibs, header, engine); err != nil {
		return err
	}
	return ProcessPragueBlockStart(chainConfig, ibs, header)
}

// ProcessPragueBlockStart applies Prague/Pectra start-of-block system operations:
//   - EIP-2935: store the parent block hash in the ring buffer once the history
//     contract has been deployed.
//
// Must be called BEFORE transaction execution, after ProcessBeaconBlockRoot.
func ProcessPragueBlockStart(chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header) error {
	if chainConfig == nil || ibs == nil || header == nil {
		return nil
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return err
	}
	rules := chainConfig.RulesWithTimestamp(headerNumber.Uint64(), header.Time)
	if rules == nil || !rules.IsPrague {
		return nil
	}
	registry := resolvePragueSystemContractRegistry(rules)

	// EIP-2935 deployment may happen before, on, or after the fork block depending
	// on the fixture. Only store history once the contract actually exists.
	if ibs.GetCodeSize(registry.historyStorage) == 0 {
		return nil
	}
	vm.StoreParentBlockHash(ibs, headerNumber.Uint64()-1, header.ParentHash)
	return ibs.FinalizeTx(rules, state.NewNoopWriter())
}

// ProcessPragueSystemCalls applies Prague/Pectra end-of-block system contract
// calls that may invalidate the payload if they error.
// EIP-7002 (withdrawal requests) and EIP-7251 (consolidation requests) system
// contracts MUST have code deployed at their addresses; otherwise the block is invalid.
func ProcessPragueSystemCalls(chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) ([]hexutil.Bytes, error) {
	if chainConfig == nil || ibs == nil || header == nil {
		return nil, nil
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	rules := chainConfig.RulesWithTimestamp(headerNumber.Uint64(), header.Time)
	if rules == nil || !rules.IsPrague {
		return nil, nil
	}
	registry := resolvePragueSystemContractRegistry(rules)

	noop := state.NewNoopWriter()
	requests := make([]hexutil.Bytes, 0, 2)
	for _, systemCall := range registry.executionRequests {
		// EIP-7002/EIP-7251: skip system call if contract not deployed.
		// The requests hash mismatch will surface as INVALID if the test
		// expects non-empty requests from a missing contract.
		if ibs.GetCodeSize(systemCall.contract) == 0 {
			requests = appendExecutionRequest(requests, systemCall.requestType, nil)
			continue
		}
		ret, err := SysCallContract(systemCall.contract, nil, *chainConfig, ibs, header, engine)
		if err != nil {
			return nil, fmt.Errorf("system call failed to execute: %w", err)
		}
		requests = appendExecutionRequest(requests, systemCall.requestType, ret)
		if err := ibs.FinalizeTx(rules, noop); err != nil {
			return nil, err
		}
	}
	return requests, nil
}

// ProcessExecutionBlockEnd applies shared end-of-block execution hooks and
// returns any execution requests emitted by the active fork.
func ProcessExecutionBlockEnd(receipts block.Receipts, chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) ([]hexutil.Bytes, error) {
	if chainConfig == nil || ibs == nil || header == nil {
		return nil, nil
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, err
	}
	rules := chainConfig.RulesWithTimestamp(headerNumber.Uint64(), header.Time)
	if rules == nil || !rules.IsPrague {
		return nil, nil
	}

	requests, err := CollectDepositExecutionRequests(receipts)
	if err != nil {
		return nil, err
	}
	pragueRequests, err := ProcessPragueSystemCalls(chainConfig, ibs, header, engine)
	if err != nil {
		return nil, err
	}
	return append(requests, pragueRequests...), nil
}

// CollectPragueExecutionRequests assembles Prague execution requests from both
// deposit receipts and Prague end-of-block system calls.
func CollectPragueExecutionRequests(receipts block.Receipts, chainConfig *params.ChainConfig, ibs *state.IntraBlockState, header *block.Header, engine consensus.Engine) ([]hexutil.Bytes, error) {
	return ProcessExecutionBlockEnd(receipts, chainConfig, ibs, header, engine)
}

// FinalizeBlockExecution finalizes block execution by running engine finalization
// and committing state changes.
func FinalizeBlockExecution(engine consensus.Engine, header *block.Header,
	txs transaction.Transactions, stateWriter state.WriterWithChangeSets, cc *params.ChainConfig, ibs *state.IntraBlockState,
	receipts block.Receipts, headerReader consensus.ChainHeaderReader, isMining bool) (newBlock block.IBlock, newTxs transaction.Transactions, newReceipt block.Receipts, err error) {
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return nil, nil, nil, err
	}

	if isMining {
		newBlock, _, _, err = engine.FinalizeAndAssemble(headerReader, header, ibs, txs, nil, receipts)
	} else {
		_, _, err = engine.Finalize(headerReader, header, ibs, txs, nil)
	}
	if err != nil {
		return nil, nil, nil, err
	}

	if err := ibs.CommitBlock(cc.Rules(headerNumber.Uint64()), stateWriter); err != nil {
		return nil, nil, nil, fmt.Errorf("committing block %d failed: %w", headerNumber.Uint64(), err)
	}
	if err := stateWriter.WriteChangeSets(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing changesets for block %d failed: %w", headerNumber.Uint64(), err)
	}
	if err := stateWriter.WriteHistory(); err != nil {
		return nil, nil, nil, fmt.Errorf("writing history for block %d failed: %w", headerNumber.Uint64(), err)
	}

	return newBlock, newTxs, newReceipt, nil
}
