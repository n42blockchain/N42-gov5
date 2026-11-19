package miner

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/params"
)

// On a native Osaka chain the honest miner never packs a transaction over
// the EIP-7825 cap (decision D2: includability does not check it, so the
// builder must not produce one) nor a blob transaction (decision D4).
func TestFillTransactionsNativeOsakaSkipsOverCapAndBlobTxs(t *testing.T) {
	f := mnTNewChainFixtureWith(t, func(cfg *params.ChainConfig, alloc conf.GenesisAlloc) {
		zero := big.NewInt(0)
		cfg.ShanghaiTime, cfg.CancunTime, cfg.PragueTime, cfg.OsakaTime = zero, zero, zero, zero
		cfg.GlamsterdamTime = nil
		cfg.Consensus = params.HotStuffConsensus
		for _, d := range internal.PragueSystemContractDeployments() {
			alloc[d.Address] = d.Account
		}
		alloc[params.BeaconRootsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: params.BeaconRootsCode}
		alloc[vm2.BuilderDepositRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
		alloc[vm2.BuilderExitRequestsAddress] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: []byte{0x00}}
	})
	if !f.Config.IsN42Native() {
		t.Fatal("fixture is not a native chain")
	}

	chainID := uint256.MustFromBig(f.Config.ChainID)
	signer := transaction.LatestSignerForChainID(f.Config.ChainID)
	to := types.Address{0xa0}
	datas := []transaction.TxData{
		&transaction.DynamicFeeTx{ChainID: chainID, GasTipCap: uint256.NewInt(3e9), GasFeeCap: uint256.NewInt(1e12), Gas: 21_000, To: &to, Value: uint256.NewInt(1)},
		&transaction.DynamicFeeTx{ChainID: chainID, GasTipCap: uint256.NewInt(2e9), GasFeeCap: uint256.NewInt(1e12), Gas: fixedgas.MaxTxnGasLimit + 1, To: &to, Value: uint256.NewInt(1)},
		&transaction.BlobTx{ChainID: chainID, GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(1e12), Gas: 21_000, To: to, Value: uint256.NewInt(1),
			BlobFeeCap: uint256.NewInt(1e12), BlobHashes: []types.Hash{{transaction.VersionedHashVersionKZG}}},
	}
	pending := map[types.Address][]*transaction.Transaction{}
	for i, data := range datas {
		tx, err := transaction.SignTx(transaction.NewTx(data), signer, f.SenderKeys[i])
		if err != nil {
			t.Fatalf("SignTx %d: %v", i, err)
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
	if len(env.txs) != 1 || *env.txs[0].From() != f.Senders[0] {
		t.Fatalf("included %d txs, want only sender 0's transfer", len(env.txs))
	}
}
