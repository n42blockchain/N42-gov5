// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S32 (docs/QS_BLOCK_TIME_BUDGET.md 6di/6dj): the offline proof for reusing
// pool-resident transaction objects when decoding a pushed block.
// BenchmarkDecodePushedBlock drives a realistic 160,000-transfer block
// (matching the fleet's own shape) through the fresh decode (today) and the
// reuse decode at 0% / 99.4% / 100% pool-hit rates.

package block

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// buildPushedBlockTxData builds n DynamicFeeTx transfers (structurally
// valid, fixed V/R/S -- this benchmark measures DECODE cost, not signature
// verification, which DecodeEthereumTransaction never performs), each
// round-tripped through the exact wire encoding a block body holds, and
// returns both the raw TxData bytes and the decoded objects (to seed a
// fake pool at a chosen hit rate).
func buildPushedBlockTxData(b *testing.B, n int) (data [][]byte, decoded []*transaction.Transaction) {
	b.Helper()
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	data = make([][]byte, n)
	decoded = make([]*transaction.Transaction, n)
	for i := 0; i < n; i++ {
		tx := transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(94),
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1_000_000_000),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(uint64(i) + 1),
			V:         uint256.NewInt(1),
			R:         uint256.NewInt(uint64(i) + 2),
			S:         uint256.NewInt(uint64(i) + 3),
		})
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			b.Fatalf("EncodeEthereumTransaction[%d]: %v", i, err)
		}
		dtx, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			b.Fatalf("DecodeEthereumTransaction[%d]: %v", i, err)
		}
		data[i] = enc
		decoded[i] = dtx
	}
	return data, decoded
}

// poolAtHitRate builds a fakePool holding the first pct% of decoded
// (rounded down), simulating a follower's pool that already holds most of
// a pushed block's own transactions.
func poolAtHitRate(decoded []*transaction.Transaction, pct float64) *fakePool {
	p := newFakePool()
	n := int(float64(len(decoded)) * pct / 100)
	for i := 0; i < n; i++ {
		p.put(decoded[i])
	}
	return p
}

const pushedBlockTxCount = 160_000

// BenchmarkDecodePushedBlock reports ns/op, B/op, allocs/op (per the whole
// block; divide by pushedBlockTxCount for the per-transaction figures the
// task asks for) for the fresh decode (today's only path) and the reuse
// decode at 0%, 99.4% (this fleet's own measured shape) and 100% pool-hit
// rates.
func BenchmarkDecodePushedBlock(b *testing.B) {
	data, decoded := buildPushedBlockTxData(b, pushedBlockTxCount)

	b.Run("fresh", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := decodeBlockTxs(data); err != nil {
				b.Fatalf("decodeBlockTxs: %v", err)
			}
		}
	})

	for _, pct := range []float64{0, 99.4, 100} {
		pct := pct
		pool := poolAtHitRate(decoded, pct)
		b.Run(fmt.Sprintf("reuse_%.1fpct", pct), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, reused, dec, err := decodeBlockTxsReuse(data, pool.GetTx)
				if err != nil {
					b.Fatalf("decodeBlockTxsReuse: %v", err)
				}
				if i == 0 {
					b.ReportMetric(float64(reused), "reused")
					b.ReportMetric(float64(dec), "decoded")
				}
			}
		})
	}
}
