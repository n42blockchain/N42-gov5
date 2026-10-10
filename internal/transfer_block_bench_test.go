// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// P3 (docs/QS_MULTI_MILLION_DESIGN.md): an offline, single-process model of
// a follower's exec wave at the fleet's block shape -- 163,200 plain legacy
// transfers from 8,000 senders (about 20 per sender, interleaved the way the
// generators interleave them) over a hot set of 22,857 recipients, under the
// mainnet_qmdb chain rules. It drives ExecuteParallelAhead, the first half
// of the lag executor's import, and reports the Block-STM run (the exec
// wave) separately from the whole call.
//
// The state is plain MDBX (memdb), not the QMDB tree: every account is read
// from the base once per block through the block-wide BaseCache, so the base
// layer contributes ~31k reads per block on either backend.
//
// Sizes: N42_BENCH_TXS, N42_BENCH_SENDERS, N42_BENCH_RECIPIENTS.

package internal

import (
	"context"
	"crypto/ecdsa"
	"os"
	"strconv"
	"sync"
	"syscall"
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

func benchEnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type transferBlockFixture struct {
	cfg        *params.ChainConfig
	txs        []*transaction.Transaction
	senders    []types.Address
	recipients []types.Address
	header     *block.Header
}

var (
	transferFixtureOnce sync.Once
	transferFixture     *transferBlockFixture
)

// transferBlockRecipient is the i-th hot recipient, hash-derived like the
// flood's (cmd/txflood deriveRecipient): random bytes, so the MVS shards and
// map buckets see the fleet's spread.
func transferBlockRecipient(i int) types.Address {
	return types.BytesToAddress(crypto.Keccak256([]byte("p3-bench-recipient"), []byte(strconv.Itoa(i))))
}

// buildTransferBlockFixture signs the block once per process (signing 163k
// transactions dominates setup and is not what is measured).
func buildTransferBlockFixture(tb testing.TB) *transferBlockFixture {
	transferFixtureOnce.Do(func() {
		nTxs := benchEnvInt("N42_BENCH_TXS", 163200)
		nSenders := benchEnvInt("N42_BENCH_SENDERS", 8000)
		nRecipients := benchEnvInt("N42_BENCH_RECIPIENTS", 22857)
		cfg := params.MainnetQMDBChainConfig
		header := &block.Header{
			Number:     uint256.NewInt(1),
			Time:       uint64(time.Now().Unix()),
			Difficulty: uint256.NewInt(0),
			BaseFee:    uint256.NewInt(7),
			GasLimit:   uint64(nTxs) * 21000 * 2,
			Coinbase:   types.Address{0xC0, 0x1B, 0xAE},
		}
		signer := transaction.MakeSignerWithTimestamp(cfg, header.Number.ToBig(), header.Time)
		keys := make([]*ecdsa.PrivateKey, nSenders)
		senders := make([]types.Address, nSenders)
		for i := range keys {
			k, err := crypto.GenerateKey()
			if err != nil {
				panic(err)
			}
			keys[i] = k
			senders[i] = crypto.PubkeyToAddress(k.PublicKey)
		}
		recipients := make([]types.Address, nRecipients)
		for i := range recipients {
			recipients[i] = transferBlockRecipient(i)
		}
		txs := make([]*transaction.Transaction, nTxs)
		var wg sync.WaitGroup
		const signers = 4
		for w := 0; w < signers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := w; i < nTxs; i += signers {
					s := i % nSenders
					to := recipients[(i*7919)%nRecipients]
					inner := &transaction.LegacyTx{
						Nonce:    uint64(i / nSenders),
						GasPrice: uint256.NewInt(1_000_000_000),
						Gas:      21000,
						To:       &to,
						Value:    uint256.NewInt(1 + uint64(i%1000)),
					}
					signed, err := transaction.SignTx(transaction.NewTx(inner), signer, keys[s])
					if err != nil {
						panic(err)
					}
					txs[i] = signed
				}
			}(w)
		}
		wg.Wait()
		transferFixture = &transferBlockFixture{cfg: cfg, txs: txs, senders: senders, recipients: recipients, header: header}
	})
	return transferFixture
}

// fundTransferBlockState writes the funded senders, the existing hot
// recipients and filler accounts into db.
func fundTransferBlockState(tb testing.TB, db kv.RwDB, fx *transferBlockFixture) {
	tb.Helper()
	filler := benchEnvInt("N42_BENCH_FILLER", 200000)
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		rich := new(uint256.Int).Mul(uint256.NewInt(1_000_000_000_000_000_000), uint256.NewInt(1000))
		for _, a := range fx.senders {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, rich)
			ibs.SetNonce(a, 0)
		}
		for _, a := range fx.recipients {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(1))
		}
		for i := 0; i < filler; i++ {
			a := types.BytesToAddress(crypto.Keccak256([]byte("p3-bench-filler"), []byte(strconv.Itoa(i))))
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(uint64(i+1)))
		}
		return ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer)
	})
	if err != nil {
		tb.Fatalf("fund accounts: %v", err)
	}
}

// BenchmarkTransferBlockExec runs the 163k-transfer block through
// ExecuteParallelAhead per iteration; execMs is the Block-STM run alone.
func BenchmarkTransferBlockExec(b *testing.B) {
	fx := buildTransferBlockFixture(b)
	db := memdb.NewTestDB(b)
	fundTransferBlockState(b, db, fx)
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	hashFn := func(uint64) types.Hash { return types.Hash{} }

	runOnce := func() *parallelRun {
		roTx, err := db.BeginRo(context.Background())
		if err != nil {
			b.Fatalf("begin ro: %v", err)
		}
		defer roTx.Rollback()
		ibs := state.New(state.NewPlainStateReader(roTx))
		run, err := sp.ExecuteParallelAhead(blk, ibs, hashFn)
		if err != nil {
			b.Fatalf("ExecuteParallelAhead: %v", err)
		}
		if len(run.Receipts) != len(fx.txs) {
			b.Fatalf("receipts %d of %d", len(run.Receipts), len(fx.txs))
		}
		return run
	}
	// Warm-up: sender recovery memoises on the transactions, pools fill.
	runOnce()

	var execTotal time.Duration
	cpu0 := processCPU()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		run := runOnce()
		execTotal += run.phases.Exec
	}
	b.StopTimer()
	// Process CPU (user+sys, GC included) per block: on a shared box it moves
	// far less than wall time with other tenants' load.
	b.ReportMetric(float64((processCPU()-cpu0).Milliseconds())/float64(b.N), "cpuMs/op")
	b.ReportMetric(float64(execTotal.Milliseconds())/float64(b.N), "execMs/op")
	b.ReportMetric(float64(len(fx.txs)), "txs/op")
	b.ReportMetric(float64(fastTransfers.Load()), "fastTotal")
}

// processCPU is the process's user+sys CPU time so far.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
