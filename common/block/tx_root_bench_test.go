// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// benchTxs builds n value transfers, the workload the qs fleet runs.
func benchTxs(n int) []*transaction.Transaction {
	to := types.HexToAddress("0x2000000000000000000000000000000000000001")
	from := types.HexToAddress("0x1000000000000000000000000000000000000001")
	out := make([]*transaction.Transaction, n)
	for i := range out {
		out[i] = transaction.NewTransaction(uint64(i), from, &to,
			uint256.NewInt(1), 21000, uint256.NewInt(1_000_000_007), nil)
	}
	return out
}

// BenchmarkTxRoot measures what ValidateBody pays per block. It is on the
// SERIAL import path (block_validator.go ValidateBody, before Process) and the
// leader pays it again when it assembles, so it is one of the few costs where
// rule 22 says a saving would actually show up.
//
// The two sub-benchmarks split the cost: EthTransactions.EncodeIndex RLP-encodes
// every transaction on every root computation, and the HashBuilder walk is the
// rest. Which half dominates decides whether the fix is a parallel trie (as a
// peer client did, splitting rlp(index) keys by all but the last byte into
// groups of 256) or simply not re-encoding.
func BenchmarkTxRoot(b *testing.B) {
	UseEthereumTxRoot = true
	for _, n := range []int{11000, 22857} {
		txs := benchTxs(n)
		b.Run(fmt.Sprintf("root/txs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = TxRoot(txs)
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n)/1000, "us/tx")
		})
		b.Run(fmt.Sprintf("encodeonly/txs=%d", n), func(b *testing.B) {
			list := transaction.EthTransactions(txs)
			var buf bytes.Buffer
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for j := 0; j < n; j++ {
					buf.Reset()
					list.EncodeIndex(j, &buf)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n)/1000, "us/tx")
		})
	}
}

// TestTxRootConcurrentRealTransactions exercises the parallel leaf encode over
// REAL transactions, which the hash package's equivalence test cannot reach
// (importing common/transaction from common/hash is an import cycle).
//
// Two risks are covered here rather than by inspection. EncodeIndex is now
// called from several goroutines at once, so EncodeEthereumTransaction and
// everything it reaches -- rlp.EncodeToBytes and its encbuf pool -- must be
// safe for that; and the root must not depend on which worker encoded which
// leaf. Run under -race, which is where the first of those would show.
func TestTxRootConcurrentRealTransactions(t *testing.T) {
	UseEthereumTxRoot = true
	// Above the parallel-encode threshold, so this takes the worker pool.
	txs := benchTxs(5000)
	want := TxRoot(txs)

	const runners = 8
	got := make([]types.Hash, runners)
	var wg sync.WaitGroup
	wg.Add(runners)
	for i := 0; i < runners; i++ {
		go func(i int) {
			defer wg.Done()
			got[i] = TxRoot(txs)
		}(i)
	}
	wg.Wait()
	for i, h := range got {
		if h != want {
			t.Fatalf("runner %d computed %s, want %s", i, h.Hex(), want.Hex())
		}
	}
}

// TestTxRootStableAcrossSizes pins that the root does not change with the
// number of encode workers, which varies with GOMAXPROCS on the machine the
// node happens to run on. A root that depended on it would make a block valid
// on one node and invalid on another.
func TestTxRootStableAcrossSizes(t *testing.T) {
	UseEthereumTxRoot = true
	for _, n := range []int{2047, 2048, 2049, 5000} {
		txs := benchTxs(n)
		first := TxRoot(txs)
		prev := runtime.GOMAXPROCS(1)
		one := TxRoot(txs)
		runtime.GOMAXPROCS(prev)
		again := TxRoot(txs)
		if one != first || again != first {
			t.Fatalf("n=%d: roots differ by GOMAXPROCS: %s / %s / %s",
				n, first.Hex(), one.Hex(), again.Hex())
		}
	}
}
