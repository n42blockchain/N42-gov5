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
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// selfdestructToCode builds: SELFDESTRUCT(beneficiary) where beneficiary is
// the 20-byte address passed in.
func selfdestructToCode(beneficiary types.Address) []byte {
	word := make([]byte, 32)
	copy(word[12:], beneficiary.Bytes())
	code := []byte{byte(PUSH32)}
	code = append(code, word...)
	code = append(code, byte(SELFDESTRUCT))
	return code
}

// TestVMTSelfdestruct6780SameTxCreatedIsFullyDeleted verifies EIP-6780: a
// contract CREATEd and then SELFDESTRUCTed within the same transaction is
// fully removed (code, storage), not just balance-swept.
func TestVMTSelfdestruct6780SameTxCreatedIsFullyDeleted(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())

	caller := types.HexToAddress("0x00000000000000000000000000000000c0ffee")
	beneficiary := types.HexToAddress("0x000000000000000000000000000000000beef1")
	ibs.CreateAccount(caller, true)
	ibs.AddBalance(caller, uint256.NewInt(1_000_000))
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)

	initCode := append([]byte{}, selfdestructToCode(beneficiary)...)
	// Wrap init code: deploy the selfdestruct bytecode as runtime code,
	// then immediately invoke SELFDESTRUCT from within the constructor by
	// just returning it would not execute it; instead, run it directly as
	// init code using CREATE so execution itself performs the destruct.
	// init code = runtime code executed at creation time, so make the
	// init code BE the selfdestruct sequence.
	_, createdAddr, _, err := evm.create(AccountRef(caller), &codeAndHash{code: initCode}, 500_000, uint256.NewInt(100), types.Address{}, CREATE, false, false)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if ibs.GetCodeSize(createdAddr) != 0 {
		t.Fatalf("expected created+selfdestructed contract to have no code, got size %d", ibs.GetCodeSize(createdAddr))
	}
	if got := ibs.GetBalance(createdAddr).Uint64(); got != 0 {
		t.Fatalf("expected created contract balance 0 after selfdestruct, got %d", got)
	}
	if got := ibs.GetBalance(beneficiary).Uint64(); got != 100 {
		t.Fatalf("expected beneficiary to receive swept balance 100, got %d", got)
	}
}

// TestVMTSelfdestruct6780PreexistingKeepsCode verifies EIP-6780: a contract
// that existed before the current transaction (loaded from committed state)
// keeps its code and storage after SELFDESTRUCT; only the balance moves.
func TestVMTSelfdestruct6780PreexistingKeepsCode(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	cfg := execHarnessChainConfig()
	rules := cfg.RulesWithTimestamp(1, 1)

	preexisting := types.HexToAddress("0x00000000000000000000000000000000facade")
	beneficiary := types.HexToAddress("0x000000000000000000000000000000000beef2")
	slot0 := types.Hash{31: 7}

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer1 := state.NewPlainStateWriter(tx, tx, 1)
		sdb1 := state.New(state.NewPlainState(tx, 1))
		sdb1.CreateAccount(preexisting, true)
		sdb1.SetCode(preexisting, selfdestructToCode(beneficiary))
		sdb1.SetState(preexisting, &slot0, *uint256.NewInt(42))
		sdb1.AddBalance(preexisting, uint256.NewInt(500))
		if err := sdb1.FinalizeTx(rules, writer1); err != nil {
			return err
		}
		if err := sdb1.CommitBlock(rules, writer1); err != nil {
			return err
		}

		// New block/tx: preexisting is now loaded fresh from the DB, so its
		// "created" flag is false — this is the EIP-6780 pre-existing case.
		sdb2 := state.New(state.NewPlainState(tx, 2))
		caller := types.HexToAddress("0x00000000000000000000000000000000ca11e2")
		sdb2.CreateAccount(caller, true)
		sdb2.PrepareAccessList(caller, &preexisting, nil, nil)

		blockCtx := evmtypes.BlockContext{
			CanTransfer: func(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
				return db.GetBalance(addr).Cmp(amount) >= 0
			},
			Transfer: func(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
				db.SubBalance(sender, amount)
				db.AddBalance(recipient, amount)
			},
			GetHash:     func(uint64) types.Hash { return types.Hash{} },
			Coinbase:    types.Address{},
			GasLimit:    1_000_000,
			BlockNumber: 2,
			Time:        1,
			Difficulty:  big.NewInt(0),
			BaseFee:     uint256.NewInt(1),
		}
		evm := NewEVM(blockCtx, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, sdb2, cfg, Config{})

		if _, _, err := evm.Call(AccountRef(caller), preexisting, nil, 500_000, uint256.NewInt(0), false); err != nil {
			t.Fatalf("call failed: %v", err)
		}

		if sdb2.GetCodeSize(preexisting) == 0 {
			t.Fatalf("pre-existing contract must keep its code after SELFDESTRUCT under EIP-6780")
		}
		var val uint256.Int
		sdb2.GetState(preexisting, &slot0, &val)
		if val.Cmp(uint256.NewInt(42)) != 0 {
			t.Fatalf("pre-existing contract must keep its storage after SELFDESTRUCT under EIP-6780, got %s", val.String())
		}
		if got := sdb2.GetBalance(preexisting).Uint64(); got != 0 {
			t.Fatalf("balance must be swept from the self-destructed account, got %d", got)
		}
		if got := sdb2.GetBalance(beneficiary).Uint64(); got != 500 {
			t.Fatalf("beneficiary must receive the swept balance, got %d", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestVMTSelfdestructZeroBalanceNoBeneficiaryTransfer exercises the
// SELFDESTRUCT path when the destructing account has no balance at all.
func TestVMTSelfdestructZeroBalanceNoBeneficiaryTransfer(t *testing.T) {
	evm, ibs := newExecHarnessEVM(t, execHarnessChainConfig())
	beneficiary := types.HexToAddress("0x000000000000000000000000000000000beef3")

	code := selfdestructToCode(beneficiary)
	_, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if got := ibs.GetBalance(beneficiary).Uint64(); got != 0 {
		t.Fatalf("expected zero transfer to beneficiary, got %d", got)
	}
}

// TestVMTSelfdestructToSelfPreexisting exercises the Selfdestruct6780
// self-to-self branch, which only triggers for an account NOT created in
// the current transaction (a same-tx-created account instead takes the
// full-delete branch, tested above). The caller (opSelfdestruct) first
// double-credits the balance to the account itself, and Selfdestruct6780's
// self-to-self case must halve it back to the original value (a no-op).
func TestVMTSelfdestructToSelfPreexisting(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	cfg := execHarnessChainConfig()
	rules := cfg.RulesWithTimestamp(1, 1)
	object := types.HexToAddress("0x00000000000000000000000000000000facad2")

	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer1 := state.NewPlainStateWriter(tx, tx, 1)
		sdb1 := state.New(state.NewPlainState(tx, 1))
		sdb1.CreateAccount(object, true)
		sdb1.SetCode(object, selfdestructToCode(object))
		sdb1.AddBalance(object, uint256.NewInt(10))
		if err := sdb1.FinalizeTx(rules, writer1); err != nil {
			return err
		}
		if err := sdb1.CommitBlock(rules, writer1); err != nil {
			return err
		}

		sdb2 := state.New(state.NewPlainState(tx, 2))
		caller := types.HexToAddress("0x00000000000000000000000000000000ca11e3")
		sdb2.CreateAccount(caller, true)
		sdb2.PrepareAccessList(caller, &object, nil, nil)

		blockCtx := evmtypes.BlockContext{
			CanTransfer: func(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
				return db.GetBalance(addr).Cmp(amount) >= 0
			},
			Transfer: func(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
				db.SubBalance(sender, amount)
				db.AddBalance(recipient, amount)
			},
			GetHash:     func(uint64) types.Hash { return types.Hash{} },
			Coinbase:    types.Address{},
			GasLimit:    1_000_000,
			BlockNumber: 2,
			Time:        1,
			Difficulty:  big.NewInt(0),
			BaseFee:     uint256.NewInt(1),
		}
		evm := NewEVM(blockCtx, evmtypes.TxContext{GasPrice: uint256.NewInt(1)}, sdb2, cfg, Config{})

		if _, _, err := evm.Call(AccountRef(caller), object, nil, 100_000, uint256.NewInt(0), false); err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if got := sdb2.GetBalance(object).Uint64(); got != 10 {
			t.Fatalf("self-destruct-to-self should leave the balance unchanged at 10, got %d", got)
		}
		if sdb2.GetCodeSize(object) == 0 {
			t.Fatalf("self-destruct-to-self on a pre-existing account must keep its code")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
