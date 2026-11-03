package internal

import (
	"bytes"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// Exercise transaction-local interpreter state across successful calls,
// return data, REVERT, a precompile, creation, and plain transfers. Compare
// against a fresh interpreter at every step, not only the final balance.
func TestApplyTransactionReusedEVM(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	beneficiary := types.HexToAddress("0xc001")
	recipient := types.HexToAddress("0xc002")
	contract := types.HexToAddress("0xc003")
	revert := types.HexToAddress("0xc004")
	precompile := types.HexToAddress("0x04")
	cfg := testStateTransitionChainConfig()
	header := &block.Header{Number: uint256.NewInt(1), Time: 1, GasLimit: 30000000, BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0)}
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	makeState := func() *state.IntraBlockState {
		_, dbtx := memdb.NewTestTx(t)
		s := state.New(state.NewPlainState(dbtx, 1))
		s.AddBalance(sender, uint256.NewInt(1000000000000000000))
		s.CreateAccount(contract, true)
		// Increment storage slot 0, emit a log, and return the new value.
		s.SetCode(contract, types.Hex2Bytes("6000546001018060005560005260206000a060206000f3"))
		s.CreateAccount(revert, true)
		s.SetCode(revert, types.Hex2Bytes("60006000fd"))
		return s
	}
	fresh, reused := makeState(), makeState()
	evm := vm2.NewEVM(NewEVMBlockContext(header, hashFn, nil, cfg, &beneficiary), evmtypes.TxContext{}, reused, cfg, vm2.Config{})
	gp1, gp2 := new(common.GasPool).AddGas(header.GasLimit), new(common.GasPool).AddGas(header.GasLimit)
	var used1, used2 uint64
	for i, to := range []*types.Address{&contract, &recipient, &revert, &precompile, nil, &contract, &recipient} {
		data := []byte(nil)
		if to == &precompile {
			data = []byte{1, 2, 3}
		}
		if to == nil {
			data = types.Hex2Bytes("6001600c60003960016000f300")
		}
		raw := transaction.NewTransaction(uint64(i), sender, to, uint256.NewInt(1), 150000, uint256.NewInt(9), data)
		txn, err := transaction.SignTx(raw, transaction.NewLondonSigner(big.NewInt(1)), key)
		if err != nil {
			t.Fatal(err)
		}
		fresh.Prepare(txn.Hash(), types.Hash{}, i)
		reused.Prepare(txn.Hash(), types.Hash{}, i)
		a, retA, errA := ApplyTransaction(cfg, hashFn, nil, &beneficiary, gp1, fresh, state.NewNoopWriter(), header, txn, &used1, vm2.Config{})
		b, retB, errB := ApplyTransactionWithEVM(evm, cfg, nil, gp2, reused, state.NewNoopWriter(), header, txn, &used2, vm2.Config{})
		if errA != nil || errB != nil {
			t.Fatalf("tx %d errors: %v / %v", i, errA, errB)
		}
		if !reflect.DeepEqual(a, b) || !bytes.Equal(retA, retB) || used1 != used2 || gp1.Gas() != gp2.Gas() {
			t.Fatalf("tx %d receipt/return/gas differ", i)
		}
		if to == &revert && a.Status != block.ReceiptStatusFailed {
			t.Fatal("REVERT transaction did not fail")
		}
		for _, addr := range []types.Address{sender, beneficiary, recipient, contract, revert, crypto.CreateAddress(sender, uint64(i))} {
			if fresh.GetNonce(addr) != reused.GetNonce(addr) || !fresh.GetBalance(addr).Eq(reused.GetBalance(addr)) || !bytes.Equal(fresh.GetCode(addr), reused.GetCode(addr)) {
				t.Fatalf("tx %d state differs at %s", i, addr)
			}
		}
		var slot types.Hash
		var value1, value2 uint256.Int
		fresh.GetState(contract, &slot, &value1)
		reused.GetState(contract, &slot, &value2)
		if value1 != value2 {
			t.Fatalf("tx %d storage differs", i)
		}
	}
}

func BenchmarkApplyTransferEVM(b *testing.B) {
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	beneficiary := types.HexToAddress("0xc001")
	cfg := testStateTransitionChainConfig()
	header := &block.Header{Number: uint256.NewInt(1), Time: 1, GasLimit: 30000000, BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0)}
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	txs := make([]*transaction.Transaction, 1000)
	for i := range txs {
		to := types.BytesToAddress(big.NewInt(int64(0x10000 + i)).Bytes())
		txs[i], err = transaction.SignTx(transaction.NewTransaction(uint64(i), sender, &to, uint256.NewInt(1), 21000, uint256.NewInt(9), nil), transaction.NewLondonSigner(big.NewInt(1)), key)
		if err != nil {
			b.Fatal(err)
		}
	}
	for _, reuse := range []bool{false, true} {
		name := "fresh"
		if reuse {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			_, dbtx := memdb.NewTestTx(b)
			writer := state.NewNoopWriter()
			b.ReportAllocs()
			b.ResetTimer()
			for j := 0; j < b.N; j++ {
				b.StopTimer()
				s := state.New(state.NewPlainState(dbtx, 1))
				s.AddBalance(sender, uint256.NewInt(1000000000000000000))
				gp := new(common.GasPool).AddGas(header.GasLimit)
				var used uint64
				b.StartTimer()
				var evm *vm2.EVM
				if reuse {
					evm = vm2.NewEVM(NewEVMBlockContext(header, hashFn, nil, cfg, &beneficiary), evmtypes.TxContext{}, s, cfg, vm2.Config{})
				}
				for i, txn := range txs {
					s.Prepare(txn.Hash(), types.Hash{}, i)
					if reuse {
						_, _, err = ApplyTransactionWithEVM(evm, cfg, nil, gp, s, writer, header, txn, &used, vm2.Config{})
					} else {
						_, _, err = ApplyTransaction(cfg, hashFn, nil, &beneficiary, gp, s, writer, header, txn, &used, vm2.Config{})
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
