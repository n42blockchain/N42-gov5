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
// ApplyTx: applies a single transaction inside the ZISK zkVM guest
// program, producing the state delta and gas used used to build the
// public inputs of the execution proof. Matches the host-side EVM
// semantics exactly so the generated proof is verifiable by the
// N42 settlement layer.

package guest

import (
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// TxResult holds the result of a single transaction execution.
type TxResult struct {
	GasUsed         uint64
	CumulativeGas   uint64
	Failed          bool // true if EVM returned an error (e.g., revert)
	ContractAddress types.Address
	Logs            []*block.Log
}

// ApplyTx executes a single transaction against the given IntraBlockState.
// This function is fully self-contained — no dependency on package `internal`
// to avoid import cycles.
//
// Parameters:
//   - gp: shared GasPool across all transactions in the block.
//   - getHash: block hash lookup function for BLOCKHASH opcode.
//     May be nil, in which case BLOCKHASH returns zero.
//   - cumulativeGas: cumulative gas used before this transaction.
func ApplyTx(fork ForkConfig, chainID uint64, header *block.Header, ibs *state.IntraBlockState, tx *transaction.Transaction, txIndex int, gp *common.GasPool, getHash func(uint64) types.Hash, cumulativeGas uint64) (*TxResult, error) {
	if header.Number == nil {
		return nil, fmt.Errorf("block header number is nil")
	}
	blockNumber := header.Number.Uint64()

	ibs.Prepare(tx.Hash(), types.Hash{}, txIndex)

	rules := forkToChainRules(fork)
	blockNum := header.Number.ToBig()

	// Build a ChainConfig with fork activation fields so that MakeSigner
	// selects the correct signer type (London/EIP2930/EIP155/Homestead).
	chainCfg := forkToChainConfig(fork, chainID, blockNum, header.Time)

	signer := transaction.MakeSignerWithTimestamp(chainCfg, blockNum, header.Time)
	msg, err := tx.AsMessage(signer, header.BaseFee)
	if err != nil {
		return nil, fmt.Errorf("failed to create message: %w", err)
	}
	msg.SetCheckNonce(false)

	var baseFee uint256.Int
	if header.BaseFee != nil {
		baseFee.SetFromBig(header.BaseFee.ToBig())
	}
	difficulty := new(big.Int)
	if header.Difficulty != nil {
		difficulty = header.Difficulty.ToBig()
	}

	if getHash == nil {
		getHash = func(n uint64) types.Hash { return types.Hash{} }
	}

	blockCtx := evmtypes.BlockContext{
		CanTransfer: canTransfer,
		Transfer:    transfer,
		GetHash:     getHash,
		Coinbase:    header.Coinbase,
		BlockNumber: blockNumber,
		Time:        header.Time,
		Difficulty:  difficulty,
		BaseFee:     &baseFee,
		GasLimit:    header.GasLimit,
	}

	txCtx := evmtypes.TxContext{
		Origin:   msg.From(),
		GasPrice: msg.GasPrice(),
	}

	vmCfg := vm.Config{
		StatelessExec: true,
		NoReceipts:    false,
	}
	evm := vm.NewEVM(blockCtx, txCtx, ibs, chainCfg, vmCfg)

	// Execute the full state transition (buyGas + EVM + refund + coinbase payment).
	result, err := vm.ApplyMessageForGuest(evm, msg, gp)
	if err != nil {
		return nil, fmt.Errorf("apply message failed: %w", err)
	}

	if err := ibs.FinalizeTx(&rules, state.NewNoopWriter()); err != nil {
		return nil, fmt.Errorf("finalize tx failed: %w", err)
	}

	// Collect logs for receipt construction.
	logs := ibs.GetLogs(tx.Hash())

	// Determine contract address for CREATE transactions.
	var contractAddr types.Address
	if msg.To() == nil {
		contractAddr = crypto.CreateAddress(msg.From(), tx.Nonce())
	}

	return &TxResult{
		GasUsed: result.UsedGas,
		// EIP-7778 (Glamsterdam): the receipt trie accumulates the
		// block-level figure, not the sender's post-refund bill.
		CumulativeGas:   cumulativeGas + result.BlockGasUsed,
		Failed:          result.Err != nil,
		ContractAddress: contractAddr,
		Logs:            logs,
	}, nil
}

func canTransfer(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
	return !db.GetBalance(addr).Lt(amount)
}

func transfer(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, _ bool) {
	db.SubBalance(sender, amount)
	db.AddBalance(recipient, amount)
}

// forkToChainConfig builds a params.ChainConfig from a ForkConfig so that
// MakeSigner can select the correct signer type (London/EIP2930/EIP155/etc.).
// Fork activation is set to the current block number/time so that Is*() returns
// true for all forks enabled in the ForkConfig.
func forkToChainConfig(f ForkConfig, chainID uint64, blockNum *big.Int, blockTime uint64) *params.ChainConfig {
	cfg := &params.ChainConfig{
		ChainID: big.NewInt(int64(chainID)),
	}
	if f.IsHomestead {
		cfg.HomesteadBlock = blockNum
	}
	if f.IsEIP150 {
		cfg.TangerineWhistleBlock = blockNum
	}
	if f.IsEIP155 || f.IsEIP158 {
		cfg.SpuriousDragonBlock = blockNum
	}
	if f.IsByzantium {
		cfg.ByzantiumBlock = blockNum
	}
	if f.IsConstantinople {
		cfg.ConstantinopleBlock = blockNum
	}
	if f.IsPetersburg {
		cfg.PetersburgBlock = blockNum
	}
	if f.IsIstanbul {
		cfg.IstanbulBlock = blockNum
	}
	if f.IsBerlin {
		cfg.BerlinBlock = blockNum
	}
	if f.IsLondon {
		cfg.LondonBlock = blockNum
	}
	if f.IsShanghai {
		cfg.ShanghaiBlock = blockNum
	}
	if f.IsCancun {
		cfg.CancunBlock = blockNum
	}
	if f.IsBeijing {
		cfg.BeijingBlock = blockNum
	}
	if f.IsPrague {
		cfg.PragueTime = new(big.Int).SetUint64(blockTime)
	}
	if f.IsPectra {
		cfg.PectraTime = new(big.Int).SetUint64(blockTime)
	}
	if f.IsOsaka {
		cfg.OsakaTime = new(big.Int).SetUint64(blockTime)
	}
	if f.IsFusaka {
		cfg.FusakaTime = new(big.Int).SetUint64(blockTime)
	}
	if f.IsNano {
		cfg.NanoBlock = blockNum
	}
	if f.IsMoran {
		cfg.MoranBlock = blockNum
	}
	if f.IsEip1559FeeCollector {
		cfg.Eip1559FeeCollectorTransition = blockNum
	}
	if f.IsPQPrecompiles {
		cfg.PQPrecompilesTime = new(big.Int).SetUint64(blockTime)
	}
	return cfg
}

// forkToChainRules converts a ForkConfig to params.Rules.
// This mapping must be kept in sync with params.Rules fields.
func forkToChainRules(f ForkConfig) params.Rules {
	return params.Rules{
		ChainID:               new(big.Int),
		IsHomestead:           f.IsHomestead,
		IsTangerineWhistle:    f.IsEIP150,
		IsSpuriousDragon:      f.IsEIP155 || f.IsEIP158,
		IsByzantium:           f.IsByzantium,
		IsConstantinople:      f.IsConstantinople,
		IsPetersburg:          f.IsPetersburg,
		IsIstanbul:            f.IsIstanbul,
		IsBerlin:              f.IsBerlin,
		IsLondon:              f.IsLondon,
		IsShanghai:            f.IsShanghai,
		IsCancun:              f.IsCancun,
		IsPrague:              f.IsPrague,
		IsPectra:              f.IsPectra,
		IsOsaka:               f.IsOsaka,
		IsFusaka:              f.IsFusaka,
		IsNano:                f.IsNano,
		IsMoran:               f.IsMoran,
		IsEip1559FeeCollector: f.IsEip1559FeeCollector,
		IsParlia:              f.IsParlia,
		IsAura:                f.IsAura,
		IsBeijing:             f.IsBeijing,
	}
}
