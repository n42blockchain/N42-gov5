package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestParallelContractObservesPreviousFees(t *testing.T) {
	cfg := benchTransferConfig()
	db := memdb.NewTestDB(t)
	coinbase := types.Address{0xc0}
	contract := types.Address{0xdd}
	recipient := types.Address{0xee}
	txs := make([]*transaction.Transaction, 2)
	senders := make([]types.Address, 2)
	for i := range txs {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		senders[i] = crypto.PubkeyToAddress(key.PublicKey)
		to := recipient
		gas := uint64(21000)
		if i == 1 {
			to = contract
			gas = 100000
		}
		signed, err := transaction.SignNewTx(key, transaction.NewLondonSigner(cfg.ChainID), &transaction.DynamicFeeTx{ChainID: uint256.MustFromBig(cfg.ChainID), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Gas: gas, To: &to, Value: uint256.NewInt(0)})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := transaction.EncodeEthereumTransaction(signed)
		if err != nil {
			t.Fatal(err)
		}
		txs[i], err = transaction.DecodeEthereumTransaction(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	fundBenchAccounts(t, db, senders, uint256.NewInt(1000000000))
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		ibs.CreateAccount(contract, true)
		// COINBASE BALANCE PUSH1(0) SSTORE STOP.
		ibs.SetCode(contract, []byte{0x41, 0x31, 0x60, 0, 0x55, 0})
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, state.NewPlainStateWriter(tx, tx, 1))
	})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Rollback()
	ibs := state.New(state.NewPlainStateReader(ro))
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(cfg, bc, nil)
	header := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1000000, Coinbase: coinbase}
	run, err := sp.BuildParallel(header, txs, ibs, func(uint64) types.Hash { return types.Hash{} })
	if err != nil {
		t.Fatal(err)
	}
	if run.Failed != 0 {
		t.Fatalf("failed %d", run.Failed)
	}
	var actual uint256.Int
	slot := types.Hash{}
	ibs.GetState(contract, &slot, &actual)
	if actual.Uint64() != 21000 {
		t.Fatalf("contract observed previous priority fees %s, want 21000", actual.String())
	}
}
