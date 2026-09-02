// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// benchWriteTxs builds n signed transfers, the shape a full 480M-gas block
// carries. Signed: an unsigned transaction encodes to a third of the size and
// understates every encoding cost (see common/block's tx_root_bench_test.go).
func benchWriteTxs(n int) []*transaction.Transaction {
	to := types.HexToAddress("0x2000000000000000000000000000000000000001")
	from := types.HexToAddress("0x1000000000000000000000000000000000000001")
	r := uint256.MustFromHex("0x8b5e7f3a1c9d4e2b6a0f8c3d5e7a9b1c2d4e6f8a0b1c3d5e7f9a1b3c5d7e9f01")
	s := uint256.MustFromHex("0x1f3d5b7997b5d3f1e0c2a4869b7d5f3120e2c4a68b9d7f5311e3c5a7098b6d4f")
	out := make([]*transaction.Transaction, n)
	for i := range out {
		tx := transaction.NewTransaction(uint64(i), from, &to,
			uint256.NewInt(1), 21000, uint256.NewInt(1_000_000_007), nil)
		tx, err := tx.WithSignatureValues(uint256.NewInt(27), r, s)
		if err != nil {
			panic(err)
		}
		out[i] = tx
	}
	return out
}

// BenchmarkWriteTransactions prices the call that a fleet round measured at
// 13.34 ms a block -- 72% of the write phase and 9.4% of the whole import.
// Per transaction it does a compact marshal, a types.CopyBytes and an MDBX
// Append. Only the marshal is benchmarked here: driving the MDBX side needed a
// database per iteration, and creating and closing one repeatedly in a single
// process made MDBX read a truncated meta page part-way through a run. A
// benchmark too flaky to answer its question is worse than none, so the whole
// call is measured on the fleet instead ("blockwrite phases", field `block`).
func BenchmarkWriteTransactions(b *testing.B) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	b.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	for _, n := range []int{22857} {
		txs := benchWriteTxs(n)

		b.Run(fmt.Sprintf("marshalonly/txs=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, tx := range txs {
					if d := tx.MarshalCompactStorage(); d == nil {
						b.Fatal("nil compact encoding")
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(n)/1000, "us/tx")
		})

	}
}
