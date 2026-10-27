package internal

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// leanCounterCode increments storage slot 0 and stops: every call to it
// conflicts with the one before.
var leanCounterCode = []byte{0x60, 0x00, 0x54, 0x60, 0x01, 0x01, 0x60, 0x00, 0x55, 0x00}

// leanBlock is a block for the N42_EXEC_LEAN differential: senders with
// nonce chains (deep version lists), plain transfers to three hot
// recipients (deep delta lists) and to two accounts that also send (full
// writes and deltas on one account), contract calls bumping one slot
// (aborts and re-executions on the EVM path), and a transfer its sender
// cannot pay.
func leanBlock(t *testing.T, nSenders, perSender int) (txs []*transaction.Transaction, funded []types.Address, poor types.Address, contract types.Address) {
	t.Helper()
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	chainID, _ := uint256.FromBig(senderRecoveryChainID)
	// Two more funded accounts are credited by every seventh transfer and
	// send one transfer each, mid-block.
	keys := make([]*ecdsa.PrivateKey, nSenders+2)
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		keys[i] = k
		funded = append(funded, crypto.PubkeyToAddress(k.PublicKey))
	}
	hot := []types.Address{{0xB0, 1}, {0xB0, 2}, {0xB0, 3}}
	contract = types.Address{0xCC, 0x01}
	sign := func(k *ecdsa.PrivateKey, nonce uint64, to types.Address, gas, value uint64) *transaction.Transaction {
		signed, err := transaction.SignNewTx(k, signer, &transaction.DynamicFeeTx{
			ChainID: chainID, Nonce: nonce, GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(1_000_000_000),
			Gas: gas, To: &to, Value: uint256.NewInt(value),
		})
		require.NoError(t, err)
		enc, err := transaction.EncodeEthereumTransaction(signed)
		require.NoError(t, err)
		dec, err := transaction.DecodeEthereumTransaction(enc)
		require.NoError(t, err)
		return dec
	}
	// Round-robin over the senders: a sender's chain spans the block.
	for n := 0; n < perSender; n++ {
		for s := 0; s < nSenders; s++ {
			i := n*nSenders + s
			switch {
			case i%97 == 3:
				txs = append(txs, sign(keys[s], uint64(n), contract, 100_000, 0))
			case i%7 == 1:
				txs = append(txs, sign(keys[s], uint64(n), funded[nSenders+i%2], params.TxGas, 3))
			default:
				txs = append(txs, sign(keys[s], uint64(n), hot[i%len(hot)], params.TxGas, uint64(1+i%5)))
			}
		}
	}
	mid := len(txs) / 2
	dual := []*transaction.Transaction{sign(keys[nSenders], 0, hot[1], params.TxGas, 1), sign(keys[nSenders+1], 0, hot[2], params.TxGas, 1)}
	txs = append(txs[:mid], append(dual, txs[mid:]...)...)
	pk, err := crypto.GenerateKey()
	require.NoError(t, err)
	poor = crypto.PubkeyToAddress(pk.PublicKey)
	txs = append(txs, sign(pk, 0, hot[0], params.TxGas, 1))
	return txs, funded, poor, contract
}

// leanRun builds the block with N42_EXEC_LEAN set to lean and returns the
// run and the post-state's root, accounts and contract slot.
func leanRun(t *testing.T, lean bool, sp *StateProcessor, db kv.RwDB, header *block.Header, txs []*transaction.Transaction, addrs []types.Address, contract types.Address) (*parallelRun, types.Hash, []string) {
	t.Helper()
	defer parallel.SetExecLeanForTest(lean)()
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()
	ibs := state.New(state.NewPlainStateReader(roTx))
	run, err := sp.BuildParallel(header, txs, ibs, func(uint64) types.Hash { return types.Hash{} })
	require.NoError(t, err)
	var accts []string
	for _, a := range addrs {
		accts = append(accts, fmt.Sprintf("%x %d %s", a, ibs.GetNonce(a), ibs.GetBalance(a).Hex()))
	}
	var v uint256.Int
	key := types.Hash{}
	ibs.GetState(contract, &key, &v)
	accts = append(accts, "slot0 "+v.Hex())
	return run, ibs.IntermediateRoot(), accts
}

// TestExecLeanBuildDifferential: a block built through the production
// entry point (BuildParallel: the plan path, the direct path and the EVM
// path on 32 workers) has the same receipts, gas, failures, post-state and
// root with N42_EXEC_LEAN on and off.
func TestExecLeanBuildDifferential(t *testing.T) {
	const nSenders, perSender = 40, 40
	txs, funded, poor, contract := leanBlock(t, nSenders, perSender)

	db := memdb.NewTestDB(t)
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		for _, a := range funded {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(1_000_000_000_000_000_000))
		}
		ibs.CreateAccount(contract, true)
		ibs.SetCode(contract, leanCounterCode)
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	}))
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(benchTransferConfig(), bc, nil)
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Time:       uint64(time.Now().Unix()),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(1),
		GasLimit:   1_000_000_000,
		Coinbase:   types.Address{0xC0, 0x1B, 0xAE},
	}
	addrs := append(append([]types.Address{}, funded...), poor, header.Coinbase, types.Address{0xB0, 1}, types.Address{0xB0, 2}, types.Address{0xB0, 3})

	plans0 := planTransfers.Load()
	offRun, offRoot, offAccts := leanRun(t, false, sp, db, header, txs, addrs, contract)
	plans1 := planTransfers.Load()
	onRun, onRoot, onAccts := leanRun(t, true, sp, db, header, txs, addrs, contract)
	if plans1 == plans0 || planTransfers.Load() == plans1 {
		t.Fatalf("the plan path did not run: %d, %d", plans1-plans0, planTransfers.Load()-plans1)
	}
	require.Equal(t, 1, offRun.Failed, "the unpaid transfer fails")
	require.Equal(t, offRun.Failed, onRun.Failed)
	require.Equal(t, offRun.UsedGas, onRun.UsedGas)
	require.Equal(t, len(offRun.Included), len(onRun.Included))
	for i := range offRun.Included {
		require.Equal(t, offRun.Included[i].Hash(), onRun.Included[i].Hash(), "included %d", i)
	}
	require.Equal(t, offRun.Receipts, onRun.Receipts)
	require.Equal(t, offRun.Nopay, onRun.Nopay)
	require.Equal(t, offAccts, onAccts)
	require.Equal(t, offRoot, onRoot)
	calls := 0
	for i := 0; i < nSenders*perSender; i++ {
		if i%97 == 3 {
			calls++
		}
	}
	require.Equal(t, fmt.Sprintf("slot0 %#x", calls), offAccts[len(offAccts)-1], "every contract call counted")
}
