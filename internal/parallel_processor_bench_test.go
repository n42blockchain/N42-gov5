// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S27 (docs/QS_BLOCK_TIME_BUDGET.md 6db/6dc): the offline proof for the
// flood's 10.36 GB/block allocation rate. BenchmarkParallelBlockTransfers
// drives the SAME entry point the importer/builder use (StateProcessor.
// BuildParallel -> runParallel -> parallelApplyTx) over a realistic block:
// N funded senders, each sending one transfer to one of a much smaller pool
// of recipients (the flood's own ~163k-transfers-over-~23k-recipients
// shape, scaled down for benchmark iteration time).

package internal

import (
	"context"
	"math/big"
	"testing"
	"time"

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

// benchTransferConfig is a minimal, all-forks-at-genesis chain config
// matching senderRecoveryChainID (94, see sender_recovery_test.go) so the
// signer buildSignedTxs-style transactions were signed under is the SAME
// signer runParallel derives internally.
func benchTransferConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(94),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
	}
}

// buildTransferBlockTxs builds nSenders signed DynamicFeeTx transfers, one
// per sender, `value` each, addressed round-robin across nRecipients
// distinct recipient addresses (never overlapping a sender's own address).
// Round-tripped through the wire encoding, exactly like buildSignedTxs, so
// the resulting objects carry a signature and no memoised From -- what a
// block arriving over the wire (or a pool entry rebuilt from RLP) looks
// like to parallelApplyTx.
func buildTransferBlockTxs(tb testing.TB, nSenders, nRecipients int, value uint64) ([]*transaction.Transaction, []types.Address) {
	tb.Helper()
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs := make([]*transaction.Transaction, nSenders)
	senders := make([]types.Address, nSenders)
	recipients := make([]types.Address, nRecipients)
	for i := range recipients {
		recipients[i] = types.Address{0xBB, byte(i), byte(i >> 8), byte(i >> 16)}
	}

	for i := 0; i < nSenders; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			tb.Fatalf("generate key %d: %v", i, err)
		}
		senders[i] = crypto.PubkeyToAddress(key.PublicKey)
		to := recipients[i%nRecipients]
		chainID, _ := uint256.FromBig(senderRecoveryChainID)
		inner := &transaction.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     0,
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1_000_000_000),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(value),
		}
		signed, err := transaction.SignNewTx(key, signer, inner)
		if err != nil {
			tb.Fatalf("sign tx %d: %v", i, err)
		}
		enc, err := transaction.EncodeEthereumTransaction(signed)
		if err != nil {
			tb.Fatalf("encode tx %d: %v", i, err)
		}
		decoded, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			tb.Fatalf("decode tx %d: %v", i, err)
		}
		txs[i] = decoded
	}
	return txs, senders
}

// fundBenchAccounts writes a funded balance for every sender directly into
// the plain-state tables, committed before the benchmark loop starts (the
// underlying DB is never written to by BuildParallel itself -- see below --
// so this funding is done exactly once, not per b.N iteration).
func fundBenchAccounts(tb testing.TB, db kv.RwDB, senders []types.Address, balance *uint256.Int) {
	tb.Helper()
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		for _, addr := range senders {
			ibs.CreateAccount(addr, true)
			ibs.SetBalance(addr, balance)
		}
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	})
	if err != nil {
		tb.Fatalf("fund accounts: %v", err)
	}
}

// runTransferBlockOnce runs one BuildParallel pass over txs against bc's
// already-funded state, in a throwaway top-level IntraBlockState (a real
// leader builds a fresh one per block too, and BuildParallel does not
// persist anything back to bc.ChainDB -- see runParallel's own doc comment:
// "neither block end nor Finalize runs -- the builder's assemble does
// that" -- so re-running the same senders at nonce 0 against the SAME
// underlying DB is safe and repeatable across b.N iterations).
func runTransferBlockOnce(tb testing.TB, sp *StateProcessor, bc *BlockChain, header *block.Header, txs []*transaction.Transaction) *parallelRun {
	tb.Helper()
	roTx, err := bc.ChainDB.BeginRo(context.Background())
	if err != nil {
		tb.Fatalf("begin ro: %v", err)
	}
	defer roTx.Rollback()
	ibs := state.New(state.NewPlainStateReader(roTx))
	run, err := sp.BuildParallel(header, txs, ibs, func(uint64) types.Hash { return types.Hash{} })
	if err != nil {
		tb.Fatalf("BuildParallel: %v", err)
	}
	if run.Failed != 0 {
		tb.Fatalf("BuildParallel: %d of %d transactions failed", run.Failed, len(txs))
	}
	if len(run.Included) != len(txs) {
		tb.Fatalf("BuildParallel: included %d of %d transactions", len(run.Included), len(txs))
	}
	return run
}

// BenchmarkParallelBlockTransfers is S27's offline proof for items (1)-(4):
// 20,000 simple transfers from 20,000 funded senders to a shared pool of
// 2,857 recipients (~7:1, matching the flood's own ~163k-tx/~23k-recipient
// ratio), through StateProcessor.BuildParallel -- the same entry point the
// miner's fill and, via ProcessParallel, the importer both use -- at the
// product default 32 workers. Run with -benchmem; compare B/op, allocs/op
// and ns/op before and after each item lands.
func BenchmarkParallelBlockTransfers(b *testing.B) {
	const (
		nSenders    = 20000
		nRecipients = 2857
		valuePerTx  = 1
		fundedWith  = uint64(1_000_000_000_000_000) // 21000 gas * 1e9 gasFeeCap = 2.1e13 worst case; ample margin
	)
	txs, senders := buildTransferBlockTxs(b, nSenders, nRecipients, valuePerTx)

	db := memdb.NewTestDB(b)
	fundBenchAccounts(b, db, senders, uint256.NewInt(fundedWith))
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

	// One warm-up pass outside the timed loop: the first BuildParallel call
	// on a fresh process pays one-time setup (goroutine pool spin-up,
	// package-level sync.Once reads) that every later block does not.
	runTransferBlockOnce(b, sp, bc, header, txs)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runTransferBlockOnce(b, sp, bc, header, txs)
	}
	b.StopTimer()
	b.ReportMetric(float64(nSenders), "txs/op")
}
