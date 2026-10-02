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

package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// execHarnessChainConfig returns a chain config with every fork active at
// genesis, suitable for exercising the full opcode surface via EVM.Call.
func execHarnessChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		MergeNetsplitBlock:    big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
	}
}

// newExecHarnessEVM builds an EVM over an in-memory state DB with the given
// chain config, returning the EVM and the underlying IntraBlockState so
// callers can seed balances/code/storage before executing bytecode.
func newExecHarnessEVM(t *testing.T, cfg *params.ChainConfig) (*EVM, *state.IntraBlockState) {
	t.Helper()

	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
			return db.GetBalance(addr).Cmp(amount) >= 0
		},
		Transfer: func(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
			db.SubBalance(sender, amount)
			db.AddBalance(recipient, amount)
		},
		GetHash:     func(n uint64) types.Hash { return types.BytesToHash([]byte{byte(n)}) },
		Coinbase:    types.HexToAddress("0xc011bac0000000000000000000000000000000"),
		GasLimit:    30_000_000,
		BlockNumber: 1,
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     uint256.NewInt(1),
	}
	evm := NewEVM(blockCtx, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, ibs, cfg, Config{})
	return evm, ibs
}

// execHarnessCall deploys/places runtime code at a fixed address with
// sufficient balance for the caller, then invokes it via EVM.Call.
func execHarnessCall(t *testing.T, evm *EVM, ibs *state.IntraBlockState, code, input []byte, gas uint64, value *uint256.Int) (ret []byte, leftOverGas uint64, err error) {
	t.Helper()

	caller := types.HexToAddress("0xcacacacacacacacacacacacacacacacacacacac")
	object := types.HexToAddress("0x0bec70beef0bec70beef0bec70beef0bec70bee")

	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000_000_000))
	ibs.CreateAccount(object, true)
	ibs.SetCode(object, code)

	if value == nil {
		value = uint256.NewInt(0)
	}

	return evm.Call(AccountRef(caller), object, input, gas, value, false)
}
