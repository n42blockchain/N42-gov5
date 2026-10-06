package internal_test

import (
	"bytes"
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	core "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/streamverify"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

func TestAccountPrefetchExecutionAndReadLog(t *testing.T) {
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
	cfg := core.AccountPrefetchChainConfigForTest()
	header := &block.Header{Number: uint256.NewInt(1), Time: 1, GasLimit: 30000000, BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(0)}
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Balance.SetUint64(1000000000000000000)
		enc := make([]byte, a.EncodingLengthForStorage())
		a.EncodeForStorage(enc)
		if err := tx.Put(modules.Account, sender[:], enc); err != nil {
			return err
		}
		for addr, code := range map[types.Address][]byte{
			contract: types.Hex2Bytes("6000546001018060005560005260206000a060206000f3"),
			revert:   types.Hex2Bytes("60006000fd"),
		} {
			a := account.NewAccount()
			a.Nonce = 1
			a.CodeHash = crypto.Keccak256Hash(code)
			enc := make([]byte, a.EncodingLengthForStorage())
			a.EncodeForStorage(enc)
			if err := tx.Put(modules.Account, addr[:], enc); err != nil {
				return err
			}
			if err := tx.Put(modules.Code, a.CodeHash[:], code); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer base.Rollback()
	plain := state.NewPlainStateReader(base)
	prefetched, err := core.AccountPrefetchReaderForTest(context.Background(), db, base, []types.Address{recipient, contract, revert, precompile}, 4)
	if err != nil {
		t.Fatal(err)
	}
	logsA := streamverify.NewReadLogRecorder(plain, nil)
	logsB := streamverify.NewReadLogRecorder(prefetched, nil)
	fresh, loaded := state.New(logsA), state.New(logsB)
	gpA, gpB := new(common.GasPool).AddGas(header.GasLimit), new(common.GasPool).AddGas(header.GasLimit)
	var usedA, usedB uint64
	for i, to := range []*types.Address{&contract, &recipient, &revert, &precompile, nil, &contract, &recipient} {
		var data []byte
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
		loaded.Prepare(txn.Hash(), types.Hash{}, i)
		a, retA, errA := core.ApplyTransaction(cfg, hashFn, nil, &beneficiary, gpA, fresh, state.NewNoopWriter(), header, txn, &usedA, vm2.Config{})
		b, retB, errB := core.ApplyTransaction(cfg, hashFn, nil, &beneficiary, gpB, loaded, state.NewNoopWriter(), header, txn, &usedB, vm2.Config{})
		if errA != nil || errB != nil {
			t.Fatalf("tx %d errors: %v/%v", i, errA, errB)
		}
		if !reflect.DeepEqual(a, b) || !bytes.Equal(retA, retB) || usedA != usedB || gpA.Gas() != gpB.Gas() {
			t.Fatalf("tx %d receipt/return/gas mismatch", i)
		}
		for _, addr := range []types.Address{sender, beneficiary, recipient, contract, revert, crypto.CreateAddress(sender, uint64(i))} {
			if fresh.GetNonce(addr) != loaded.GetNonce(addr) || !fresh.GetBalance(addr).Eq(loaded.GetBalance(addr)) || !bytes.Equal(fresh.GetCode(addr), loaded.GetCode(addr)) {
				t.Fatalf("tx %d state mismatch at %s", i, addr)
			}
		}
		var slot types.Hash
		var va, vb uint256.Int
		fresh.GetState(contract, &slot, &va)
		loaded.GetState(contract, &slot, &vb)
		if va != vb {
			t.Fatalf("tx %d storage mismatch", i)
		}
		if !reflect.DeepEqual(logsA.Log(), logsB.Log()) || !reflect.DeepEqual(logsA.UncachedBytecodes(), logsB.UncachedBytecodes()) {
			t.Fatalf("tx %d ordered reads/bytecodes changed", i)
		}
	}
	if core.AccountPrefetchHitsForTest(prefetched) != 4 {
		t.Fatalf("prefetch not exercised: %d hits", core.AccountPrefetchHitsForTest(prefetched))
	}
}
