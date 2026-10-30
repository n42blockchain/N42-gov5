package miner

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// On a Glamsterdam block the builder's header gas used is the EIP-8037 block
// figure that import recomputes, and a transaction that fails during the
// fill must not reset the block's execution and state counters.
func TestFillTransactionsAmsterdamHeaderGasUsed(t *testing.T) {
	f := mnTNewChainFixtureWith(t, func(cfg *params.ChainConfig, alloc conf.GenesisAlloc) {
		zero := big.NewInt(0)
		cfg.ShanghaiTime, cfg.CancunTime, cfg.PragueTime, cfg.OsakaTime, cfg.GlamsterdamTime = zero, zero, zero, zero, zero
		for _, d := range internal.PragueSystemContractDeployments() {
			alloc[d.Address] = d.Account
		}
		alloc[params.BeaconRootsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: params.BeaconRootsCode}
		alloc[vm2.BuilderDepositRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
		alloc[vm2.BuilderExitRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
	})

	// Sender 0 and 2 send to fresh accounts (execution and state gas);
	// sender 1 sends more than it holds and fails between them. Tips set
	// the order.
	pending := map[types.Address][]*transaction.Transaction{}
	for i, value := range []*uint256.Int{
		uint256.NewInt(1),
		new(uint256.Int).Mul(uint256.NewInt(1_000), uint256.NewInt(1e18)),
		uint256.NewInt(1),
	} {
		to := types.Address{0xa0, byte(i)}
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(f.Config.ChainID),
			GasTipCap: uint256.NewInt(uint64(3-i) * 1_000_000_000),
			GasFeeCap: uint256.NewInt(1_000_000_000_000),
			Gas:       1_000_000,
			To:        &to,
			Value:     value,
		}), transaction.LatestSignerForChainID(f.Config.ChainID), f.SenderKeys[i])
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		tx.SetFrom(f.Senders[i])
		pending[f.Senders[i]] = []*transaction.Transaction{tx}
	}

	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	ibs, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: pending}

	getHeader := func(types.Hash, uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}
	if len(env.txs) != 2 {
		t.Fatalf("included %d txs, want 2", len(env.txs))
	}

	// Re-execute the included transactions as import does.
	tx, err := f.DB.BeginRo(w.ctx)
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()
	replay := state.New(state.NewPlainStateReader(tx))
	header := block.CopyHeader(env.header)
	header.GasUsed = 0
	gp := new(common.GasPool).AddGas(header.GasLimit)
	var cumulative uint64
	for i, txn := range env.txs {
		replay.Prepare(txn.Hash(), types.Hash{}, i)
		coinbase := header.Coinbase
		if _, _, err := internal.ApplyTransaction(f.Config, func(uint64) types.Hash { return types.Hash{} }, f.Engine, &coinbase, gp, replay, state.NewNoopWriter(), header, txn, &cumulative, vm2.Config{}); err != nil {
			t.Fatalf("replay tx %d: %v", i, err)
		}
	}
	want := internal.HeaderGasUsed(f.Config, header, gp, cumulative)
	if want == cumulative {
		t.Fatalf("test needs state gas: block figure %d equals the receipts' cumulative", want)
	}
	if env.header.GasUsed != want {
		t.Fatalf("header.GasUsed = %d, import computes %d", env.header.GasUsed, want)
	}
}
