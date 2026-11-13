// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// erc20Receipts is n receipts with one ERC-20 style log each (3 topics,
// 32 bytes of data).
func erc20Receipts(n int) Receipts {
	rs := transferReceipts(n)
	for i, r := range rs {
		var l Log
		l.Address = types.Address{0xa0, byte(i >> 8), byte(i)}
		l.Topics = []types.Hash{{0xdd, 0xf2}, {byte(i), byte(i >> 8), 1}, {byte(i >> 4), 2}}
		l.Data = make([]byte, 32)
		l.Data[31] = byte(i)
		l.Data[30] = byte(i >> 8)
		r.Logs = []*Log{&l}
	}
	return rs
}

func BenchmarkReceiptsDigest326k(b *testing.B) {
	const n = 326000
	shapes := map[string]Receipts{"transfer": transferReceipts(n), "erc20": erc20Receipts(n)}
	for _, shape := range []string{"transfer", "erc20"} {
		rs := shapes[shape]
		for _, m := range []struct {
			name          string
			root, compact bool
		}{{"root", true, false}, {"compact", false, true}, {"all", true, true}} {
			b.Run(shape+"/"+m.name, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					ReceiptsDigest(rs, m.root, m.compact)
				}
			})
		}
		b.Run(shape+"/bloomOnly", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				CreateBloom(rs)
			}
		})
		for _, w := range []int{1, 8} {
			b.Run(fmt.Sprintf("%s/parallel%d", shape, w), func(b *testing.B) {
				if rootBloomParallelFn == nil {
					b.Skip()
				}
				for i := 0; i < b.N; i++ {
					rootBloomParallelFn(rs, w)
				}
			})
		}
	}
}

var rootBloomParallelFn = ReceiptsRootBloomParallel
