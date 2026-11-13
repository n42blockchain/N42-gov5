// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// BenchmarkReceiptsFutureTimings reports the microseconds to done (resUs)
// and to encodedDone (rowUs) of a 326k-receipt future, split off and on.
func BenchmarkReceiptsFutureTimings(b *testing.B) {
	const n = 326000
	mk := func(logs bool) []*block.Receipt {
		rs := make([]*block.Receipt, n)
		for i := range rs {
			r := &block.Receipt{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: uint64(i+1) * 21000}
			if logs {
				r.Logs = []*block.Log{{Address: types.Address{0xa0, byte(i >> 8), byte(i)},
					Topics: []types.Hash{{0xdd}, {byte(i), byte(i >> 8)}, {byte(i >> 4)}}, Data: make([]byte, 32)}}
			}
			rs[i] = r
		}
		return rs
	}
	for _, shape := range []struct {
		name string
		logs bool
	}{{"transfer", false}, {"erc20", true}} {
		rs := mk(shape.logs)
		for _, split := range []bool{false, true} {
			name := shape.name + "/off"
			if split {
				name = shape.name + "/split"
			}
			b.Run(name, func(b *testing.B) {
				defer SetReceiptsResultSplitForTest(split)()
				var res, row int64
				for i := 0; i < b.N; i++ {
					f := startReceiptsResult(nil, 1, types.Hash{1}, rs)
					<-f.done
					<-f.encodedDone
					a, c := f.timings()
					res += a
					row += c
				}
				b.ReportMetric(float64(res)/float64(b.N), "resFutUs")
				b.ReportMetric(float64(row)/float64(b.N), "rowFutUs")
			})
		}
	}
}
