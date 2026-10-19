// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// oneLookupWorld is a state shaped for the plan path's conflicts: funded
// senders (poor and tight ones when invalid transactions are allowed),
// keyed accounts absent from the state that are funded inside the block and
// then send, existing EOA recipients and fresh recipient addresses.
type oneLookupWorld struct {
	keys     []*ecdsa.PrivateKey
	addrs    []types.Address
	balances []*uint256.Int // nil: absent from the state (funded in the block)
	eoas     []types.Address
	fresh    []types.Address
}

func newOneLookupWorld(t *testing.T, rng *rand.Rand, invalid bool) *oneLookupWorld {
	w := &oneLookupWorld{}
	rich := new(uint256.Int).Mul(uint256.NewInt(1_000_000_000_000_000_000), uint256.NewInt(100))
	for i := 0; i < 48; i++ {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		w.keys = append(w.keys, k)
		w.addrs = append(w.addrs, crypto.PubkeyToAddress(k.PublicKey))
		var bal *uint256.Int
		switch {
		case i >= 40: // absent: funded by an earlier transaction of the block
		case invalid && i%8 == 0: // poor: cannot pay for the gas
			bal = uint256.NewInt(uint64(rng.Intn(1_000_000)))
		case invalid && i%8 == 1: // tight: the gas and a little value
			bal = uint256.NewInt(21000*1_000_000_000 + uint64(rng.Intn(5000)))
		default:
			bal = rich
		}
		w.balances = append(w.balances, bal)
	}
	for i := 0; i < 16; i++ {
		w.eoas = append(w.eoas, types.BytesToAddress(crypto.Keccak256([]byte("one-lookup-eoa"), []byte{byte(i)})))
		w.fresh = append(w.fresh, types.BytesToAddress(crypto.Keccak256([]byte("one-lookup-fresh"), []byte{byte(i)})))
	}
	return w
}

func (w *oneLookupWorld) fund(t *testing.T, db kv.RwDB) {
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		for i, a := range w.addrs {
			if w.balances[i] != nil {
				ibs.CreateAccount(a, true)
				ibs.SetBalance(a, w.balances[i])
			}
		}
		for _, a := range w.eoas {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(1))
		}
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	}))
}

// block draws n transfers: senders sending chains, to other senders (a
// recipient that sends later or sent earlier), to existing EOAs, to fresh
// addresses, to themselves, and fundings of the absent keyed accounts,
// which then send. With invalid, a few nonces are wrong as well.
func (w *oneLookupWorld) block(t *testing.T, rng *rand.Rand, cfg *params.ChainConfig, header *block.Header, n int, invalid bool) []*transaction.Transaction {
	signer := transaction.MakeSignerWithTimestamp(cfg, header.Number.ToBig(), header.Time)
	chainID, _ := uint256.FromBig(cfg.ChainID)
	baseFee := header.BaseFee.Uint64()
	next := make([]uint64, len(w.addrs))
	funded := make([]bool, len(w.addrs))
	for i := range funded {
		funded[i] = w.balances[i] != nil
	}
	txs := make([]*transaction.Transaction, 0, n)
	for len(txs) < n {
		s := rng.Intn(len(w.addrs))
		if !funded[s] {
			continue
		}
		var to types.Address
		value := uint256.NewInt(1 + uint64(rng.Intn(1_000_000)))
		switch r := rng.Intn(20); {
		case r < 7:
			to = w.addrs[rng.Intn(40)]
		case r < 11:
			to = w.eoas[rng.Intn(len(w.eoas))]
		case r < 14:
			to = w.fresh[rng.Intn(len(w.fresh))]
		case r < 15:
			to = w.addrs[s]
		case s >= 40:
			to = w.eoas[rng.Intn(len(w.eoas))] // a funded-in-block sender keeps small values
		default:
			// Fund an absent keyed account, which may send from then on.
			d := 40 + rng.Intn(len(w.addrs)-40)
			to = w.addrs[d]
			value = uint256.NewInt(1_000_000_000_000_000_000)
			funded[d] = true
		}
		nonce := next[s]
		consumes := true
		if invalid && rng.Intn(25) == 0 {
			nonce += 1 + uint64(rng.Intn(2)) // a gap: rejected
			consumes = false
		}
		var inner transaction.TxData
		if rng.Intn(2) == 0 {
			inner = &transaction.LegacyTx{Nonce: nonce, GasPrice: uint256.NewInt(baseFee + uint64(rng.Intn(50))), Gas: params.TxGas, To: &to, Value: value}
		} else {
			tip := uint64(rng.Intn(5))
			inner = &transaction.DynamicFeeTx{ChainID: chainID, Nonce: nonce, GasTipCap: uint256.NewInt(tip), GasFeeCap: uint256.NewInt(baseFee + tip + uint64(rng.Intn(20))), Gas: params.TxGas, To: &to, Value: value}
		}
		if consumes {
			next[s]++
		}
		signed, err := transaction.SignTx(transaction.NewTx(inner), signer, w.keys[s])
		require.NoError(t, err)
		txs = append(txs, signed)
	}
	return txs
}

// TestTransferOneLookupEquivalence runs random transfer blocks full of the
// plan path's conflicts -- sender chains, recipients that send, self
// transfers, accounts created and then spent from inside the block, and
// (on the builder's path) insufficient balances and nonce gaps -- with
// N42_TRANSFER_ONE_LOOKUP off and on, and requires the same state root,
// receipts, inclusion, gas and fee credits, and the EVM path's.
func TestTransferOneLookupEquivalence(t *testing.T) {
	collector := types.BytesToAddress(crypto.Keccak256([]byte("one-lookup-collector")))
	londonCollector := benchTransferConfig()
	londonCollector.Eip1559FeeCollector = &collector
	londonCollector.Eip1559FeeCollectorTransition = big.NewInt(0)
	configs := []struct {
		name string
		cfg  *params.ChainConfig
		time uint64
	}{
		{"mainnet_qmdb", params.MainnetQMDBChainConfig, uint64(time.Now().Unix())},
		{"london_fee_collector", londonCollector, 1_700_000_000},
	}
	for ci, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			for seed := int64(0); seed < 3; seed++ {
				rng := rand.New(rand.NewSource(seed*17 + int64(ci)))
				header := &block.Header{
					Number:     uint256.NewInt(1),
					Time:       c.time,
					Difficulty: uint256.NewInt(0),
					BaseFee:    uint256.NewInt(uint64(7 + rng.Intn(100))),
					GasLimit:   100_000_000,
					Coinbase:   types.Address{0xC0, 0x1B, 0xAE, byte(seed)},
				}

				// The builder's path, invalid transactions included.
				w := newOneLookupWorld(t, rng, true)
				db := memdb.NewTestDB(t)
				w.fund(t, db)
				txs := w.block(t, rng, c.cfg, header, 600, true)
				slow := runFastPathBlockMode(t, db, c.cfg, header, txs, false, false)
				restore := setTransferOneLookupForTest(false)
				off := runFastPathBlockMode(t, db, c.cfg, header, txs, true, true)
				restore()
				restore = setTransferOneLookupForTest(true)
				plans0 := planTransfers.Load()
				on := runFastPathBlockMode(t, db, c.cfg, header, txs, true, true)
				plans := planTransfers.Load() - plans0
				restore()
				require.Greater(t, plans, uint64(len(txs)/4), "few plans applied")
				require.Greater(t, off.failed, 0, "no transaction failed: the invalid cases were not exercised")
				require.Equal(t, off, on, "builder run, seed %d", seed)
				require.Equal(t, slow, on, "builder run against the EVM, seed %d", seed)

				// The ahead (import) path over a valid block.
				if ci != 0 {
					continue // ExecuteParallelAhead runs the QMDB chain's blocks
				}
				w = newOneLookupWorld(t, rng, false)
				db = memdb.NewTestDB(t)
				w.fund(t, db)
				fx := &transferBlockFixture{cfg: c.cfg, header: header, txs: w.block(t, rng, c.cfg, header, 1500, false)}
				restore = setTransferOneLookupForTest(false)
				aheadOff := runFast2Ahead(t, db, fx, true)
				restore()
				for round := 0; round < 2; round++ {
					restore = setTransferOneLookupForTest(true)
					aheadOn := runFast2Ahead(t, db, fx, true)
					restore()
					require.Greater(t, aheadOn.plans, uint64(len(fx.txs)/4), "few plans applied on the ahead path")
					aheadOn.plans = aheadOff.plans
					require.Equal(t, aheadOff, aheadOn, "ahead run, seed %d round %d", seed, round)
				}
				require.Equal(t, len(fx.txs), len(aheadOff.receiptBytes))
			}
		})
	}
}
