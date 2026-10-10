// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"testing"

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

// fastPathWorld is a funded state for the differential test: senders with
// rich, tight and poor balances (one of them EIP-7702 delegated), existing
// EOA recipients, contract recipients and the addresses a transaction can
// reach without any of them.
type fastPathWorld struct {
	keys      []*ecdsa.PrivateKey
	senders   []types.Address
	balances  []*uint256.Int
	eoas      []types.Address
	contracts []types.Address
}

func newFastPathWorld(t *testing.T, rng *rand.Rand) *fastPathWorld {
	w := &fastPathWorld{}
	for i := 0; i < 64; i++ {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		w.keys = append(w.keys, k)
		w.senders = append(w.senders, crypto.PubkeyToAddress(k.PublicKey))
		var bal *uint256.Int
		switch i % 8 {
		case 0: // poor: cannot pay for 21000 gas at 1 gwei
			bal = uint256.NewInt(uint64(rng.Intn(1_000_000)))
		case 1: // tight: gas plus a little value
			bal = uint256.NewInt(21000*1_000_000_000 + uint64(rng.Intn(5000)))
		default:
			bal = new(uint256.Int).Mul(uint256.NewInt(1_000_000_000_000_000_000), uint256.NewInt(100))
		}
		w.balances = append(w.balances, bal)
	}
	for i := 0; i < 30; i++ {
		w.eoas = append(w.eoas, types.BytesToAddress(crypto.Keccak256([]byte{0xE0, byte(i)})))
	}
	for i := 0; i < 3; i++ {
		w.contracts = append(w.contracts, types.BytesToAddress(crypto.Keccak256([]byte{0xC0, byte(i)})))
	}
	return w
}

// contractCodes: STOP, REVERT, and an SSTORE that a 21000-gas call cannot pay.
var fastPathContractCodes = [][]byte{
	{0x00},
	{0x60, 0x00, 0x60, 0x00, 0xfd},
	{0x60, 0x01, 0x60, 0x00, 0x55, 0x00},
}

func (w *fastPathWorld) fund(t *testing.T, db kv.RwDB) {
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		for i, a := range w.senders {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, w.balances[i])
		}
		// Sender 2 is an EIP-7702 delegated EOA (a valid sender from Prague).
		ibs.SetCode(w.senders[2], append([]byte{0xef, 0x01, 0x00}, w.eoas[0].Bytes()...))
		for _, a := range w.eoas {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(1))
		}
		for i, a := range w.contracts {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(5))
			ibs.SetNonce(a, 1)
			ibs.SetCode(a, fastPathContractCodes[i])
		}
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	}))
}

// randomBlock draws n transactions mixing every condition the fast path
// must either take or hand to the EVM.
func (w *fastPathWorld) randomBlock(t *testing.T, rng *rand.Rand, cfg *params.ChainConfig, header *block.Header, n int) []*transaction.Transaction {
	signer := transaction.MakeSignerWithTimestamp(cfg, header.Number.ToBig(), header.Time)
	chainID, _ := uint256.FromBig(cfg.ChainID)
	baseFee := header.BaseFee.Uint64()
	next := make([]uint64, len(w.senders))
	precompiles := []types.Address{types.BytesToAddress([]byte{1}), types.BytesToAddress([]byte{2}), types.BytesToAddress([]byte{4}), types.BytesToAddress([]byte{9})}
	txs := make([]*transaction.Transaction, 0, n)
	for len(txs) < n {
		s := rng.Intn(len(w.senders))
		// A pre-check failure (bad nonce, fee cap, funds, intrinsic gas)
		// does not consume the nonce; the generator tracks that so one bad
		// transaction does not fail the rest of its sender's chain.
		consumes := true
		nonce := next[s]
		switch r := rng.Intn(40); {
		case r == 0:
			nonce++ // too high
			consumes = false
		case r == 1 && nonce > 0:
			nonce-- // too low
			consumes = false
		}
		var to types.Address
		switch r := rng.Intn(20); {
		case r < 12:
			to = w.eoas[rng.Intn(len(w.eoas))]
		case r < 14:
			to = w.contracts[rng.Intn(len(w.contracts))]
		case r < 15:
			to = precompiles[rng.Intn(len(precompiles))]
		case r < 17:
			to = types.BytesToAddress(crypto.Keccak256([]byte{0xF0, byte(rng.Intn(256)), byte(len(txs))})) // fresh
		case r < 18:
			to = w.senders[s] // self
		default:
			to = w.senders[rng.Intn(len(w.senders))]
		}
		var value *uint256.Int
		switch r := rng.Intn(20); {
		case r < 5:
			value = new(uint256.Int)
		case r < 19:
			value = uint256.NewInt(uint64(1 + rng.Intn(10000)))
		default:
			value = new(uint256.Int).Mul(uint256.NewInt(1_000_000_000_000_000_000), uint256.NewInt(1000)) // more than anyone holds
			consumes = false
		}
		gas := uint64(21000)
		switch rng.Intn(24) {
		case 0:
			gas = 30000
		case 1:
			gas = 21001
		case 2:
			gas = 20999
			consumes = false
		}
		var data []byte
		if rng.Intn(16) == 0 {
			data = []byte{byte(rng.Intn(2))}
			if gas == 21000 {
				gas = 21100
			}
		}
		price := uint64(1_000_000_000)
		switch rng.Intn(16) {
		case 0:
			price = 0
			consumes = false
		case 1:
			price = baseFee - 1
			consumes = false
		case 2:
			price = baseFee
		case 3:
			price = baseFee + 1
		}
		var inner transaction.TxData
		if rng.Intn(2) == 0 {
			inner = &transaction.LegacyTx{Nonce: nonce, GasPrice: uint256.NewInt(price), Gas: gas, To: &to, Value: value, Data: data}
		} else {
			tip := []uint64{0, 1, 2, price, price, price + 1}[rng.Intn(6)]
			if tip > price {
				consumes = false
			}
			inner = &transaction.DynamicFeeTx{ChainID: chainID, Nonce: nonce, GasTipCap: uint256.NewInt(tip), GasFeeCap: uint256.NewInt(price), Gas: gas, To: &to, Value: value, Data: data}
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

type fastPathOutcome struct {
	stateRoot    types.Hash
	receiptsRoot types.Hash
	receiptBytes [][]byte
	receipts     block.Receipts
	included     []types.Hash
	failed       int
	usedGas      uint64
	coinbase     uint256.Int
	collector    uint256.Int
}

func runFastPathBlock(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction, fast bool) fastPathOutcome {
	saved := transferFastPath
	transferFastPath = fast
	defer func() { transferFastPath = saved }()

	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(cfg, bc, coreTFakerEngine{})
	var out fastPathOutcome
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		run, err := sp.BuildParallel(header, txs, ibs, func(uint64) types.Hash { return types.Hash{} })
		if err != nil {
			return err
		}
		out.failed = run.Failed
		out.usedGas = run.UsedGas
		out.receipts = run.Receipts
		for _, in := range run.Included {
			out.included = append(out.included, in.Hash())
		}
		for i := range run.Receipts {
			var buf bytes.Buffer
			run.Receipts.EncodeIndex(i, &buf)
			out.receiptBytes = append(out.receiptBytes, buf.Bytes())
		}
		out.receiptsRoot = DeriveSha(run.Receipts)
		out.coinbase = *ibs.GetBalance(header.Coinbase)
		if cfg.Eip1559FeeCollector != nil {
			out.collector = *ibs.GetBalance(*cfg.Eip1559FeeCollector)
		}
		out.stateRoot = ibs.IntermediateRoot()
		return nil
	}))
	return out
}

// TestTransferFastPathMatchesEVM executes random blocks on both paths and
// requires the same state root, receipts (root, bytes and fields), gas,
// inclusion and coinbase/collector credit.
func TestTransferFastPathMatchesEVM(t *testing.T) {
	collector := types.BytesToAddress(crypto.Keccak256([]byte("fee-collector")))
	london := benchTransferConfig()
	londonCollector := benchTransferConfig()
	londonCollector.Eip1559FeeCollector = &collector
	londonCollector.Eip1559FeeCollectorTransition = big.NewInt(0)
	configs := []struct {
		name string
		cfg  *params.ChainConfig
		time uint64
	}{
		{"mainnet_qmdb_prague", params.MainnetQMDBChainConfig, 1_800_000_000},
		{"london", london, 1_700_000_000},
		{"london_fee_collector", londonCollector, 1_700_000_000},
	}
	for ci, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			for seed := int64(0); seed < 4; seed++ {
				rng := rand.New(rand.NewSource(seed*31 + int64(ci)))
				w := newFastPathWorld(t, rng)
				db := memdb.NewTestDB(t)
				w.fund(t, db)
				header := &block.Header{
					Number:     uint256.NewInt(1),
					Time:       c.time,
					Difficulty: uint256.NewInt(0),
					BaseFee:    uint256.NewInt(uint64(7 + rng.Intn(100))),
					GasLimit:   100_000_000,
					Coinbase:   types.Address{0xC0, 0x1B, 0xAE, byte(seed)},
				}
				txs := w.randomBlock(t, rng, c.cfg, header, 400)

				before := fastTransfers.Load()
				slow := runFastPathBlock(t, db, c.cfg, header, txs, false)
				require.Equal(t, before, fastTransfers.Load(), "the EVM run took the fast path")
				fast := runFastPathBlock(t, db, c.cfg, header, txs, true)
				took := fastTransfers.Load() - before
				require.Greater(t, took, uint64(0), "the fast run never took the fast path")
				require.Less(t, took, uint64(len(fast.included)), "every included tx took the fast path: the fallbacks were not exercised")

				require.Equal(t, slow.included, fast.included, "included transactions")
				require.Equal(t, slow.failed, fast.failed, "failed count")
				require.Equal(t, slow.usedGas, fast.usedGas, "gas used")
				require.Equal(t, slow.receiptsRoot, fast.receiptsRoot, "receipts root")
				require.Equal(t, slow.receiptBytes, fast.receiptBytes, "receipt bytes")
				for i := range slow.receipts {
					a, b := slow.receipts[i], fast.receipts[i]
					require.Equal(t, a.Status, b.Status, "receipt %d status", i)
					require.Equal(t, a.GasUsed, b.GasUsed, "receipt %d gas", i)
					require.Equal(t, a.CumulativeGasUsed, b.CumulativeGasUsed, "receipt %d cumulative gas", i)
					require.Equal(t, a.Bloom, b.Bloom, "receipt %d bloom", i)
					require.Equal(t, len(a.Logs), len(b.Logs), "receipt %d logs", i)
					require.Equal(t, a.TxHash, b.TxHash, "receipt %d hash", i)
					require.Equal(t, a.TransactionIndex, b.TransactionIndex, "receipt %d index", i)
					require.Equal(t, a.Type, b.Type, "receipt %d type", i)
				}
				require.Equal(t, slow.coinbase, fast.coinbase, "coinbase balance")
				require.Equal(t, slow.collector, fast.collector, "collector balance")
				require.Equal(t, slow.stateRoot, fast.stateRoot, "state root")
				t.Logf("seed %d: %d txs, %d included, %d failed, %d fast", seed, len(txs), len(fast.included), fast.failed, took)
			}
		})
	}
}
