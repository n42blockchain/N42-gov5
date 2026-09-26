// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// poolBenchTransactions pre-signs n distinct legacy transfers, fresh-decoded
// so each is an uncached recovery target (mirrors hintLoadTransactions in
// internal/hint_recovery_interference_bench_test.go, kept local here since
// that one lives in package internal and cannot be imported).
func poolBenchTransactions(b *testing.B, n int) ([]*Transaction, Signer) {
	b.Helper()
	chainID := big.NewInt(94)
	signer := LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}
	txs := make([]*Transaction, n)
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			b.Fatalf("key %d: %v", i, err)
		}
		tx, err := SignNewTx(key, signer, &LegacyTx{
			Nonce:    uint64(i),
			GasPrice: uint256.NewInt(10_000_000_000),
			Gas:      21000,
			To:       &to,
			Value:    uint256.NewInt(1),
		})
		if err != nil {
			b.Fatalf("sign %d: %v", i, err)
		}
		enc, err := EncodeEthereumTransaction(tx)
		if err != nil {
			b.Fatalf("encode %d: %v", i, err)
		}
		fresh, err := DecodeEthereumTransaction(enc)
		if err != nil {
			b.Fatalf("decode %d: %v", i, err)
		}
		txs[i] = fresh
	}
	return txs, signer
}

// BenchmarkHintRecoveryPoolSizes reports recoveries/s at worker-pool sizes
// 4 and 8 (S59 part 3): a manually-sized pool (bypassing the process-wide
// env-parsed HintRecoveryPool singleton, which can only be configured once
// per process) fans b.N distinct, never-repeated transactions out across n
// workers and waits for all of them, so b.Elapsed/b.N x n approximates one
// recovery's own wall time under that pool size, and n x (1/that time) is
// the pool's own achievable recoveries/s.
func BenchmarkHintRecoveryPoolSizes(b *testing.B) {
	for _, n := range []int{4, 8} {
		n := n
		b.Run(fmt.Sprintf("workers=%d", n), func(b *testing.B) {
			txs, signer := poolBenchTransactions(b, b.N+1)
			jobs := make(chan func(), n*4)
			stop := make(chan struct{})
			for w := 0; w < n; w++ {
				go func() {
					for {
						select {
						case job := <-jobs:
							job()
						case <-stop:
							return
						}
					}
				}()
			}
			defer close(stop)

			// Submit all b.N jobs up front and wait for all of them: this
			// exercises the pool's own n-way parallelism (unlike submitting
			// and waiting one at a time, which would be serial regardless of
			// n). The jobs channel is sized n*4 above; a producer goroutine
			// feeds it so submission never blocks the workers from draining.
			b.ReportAllocs()
			b.ResetTimer()
			var wg sync.WaitGroup
			wg.Add(b.N)
			go func() {
				for i := 0; i < b.N; i++ {
					tx := txs[i]
					jobs <- func() {
						_, _ = RecoverSenderDeduped(signer, tx)
						wg.Done()
					}
				}
			}()
			wg.Wait()
			b.StopTimer()
			recoveriesPerSec := float64(b.N) / b.Elapsed().Seconds()
			b.ReportMetric(recoveriesPerSec, "recoveries/s")
		})
	}
}
