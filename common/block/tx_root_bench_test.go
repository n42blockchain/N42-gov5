// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"fmt"
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
