// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/internal/parallel"
)

// newFleetFoldFixture is a block of n transfers from senders accounts
// (each sending n/senders times) to recipients accounts: the qs fleet's
// shape (fDirty ~8k, ~8k credited recipients at 163k transfers).
func newFleetFoldFixture(n, senders, recipients int) *transferFoldFixture {
	f := &transferFoldFixture{mvs: parallel.NewMVS(), store: bulkMapReader{}, known: bulkKnownCache{}, n: n}
	for r := 0; r < recipients; r++ {
		to := bulkTestAddr(-1 - r)
		acc := account.NewAccount()
		acc.Balance.SetUint64(uint64(r))
		f.store[to] = &acc
		f.known[to] = &acc
	}
	for s := 0; s < senders; s++ {
		acc := account.NewAccount()
		acc.Balance.SetUint64(1_000_000_000_000)
		f.store[bulkTestAddr(s)] = &acc
		f.known[bulkTestAddr(s)] = &acc
	}
	for i := 0; i < n; i++ {
		s := i % senders
		from, to := bulkTestAddr(s), bulkTestAddr(-1-i%recipients)
		post := account.NewAccount()
		post.Balance.SetUint64(1_000_000_000_000 - uint64(i)*21000)
		post.Nonce = uint64(i/senders + 1)
		f.mvs.Write(parallel.LocationKey{Address: from, Field: parallel.FieldBalance}, i, 0, post.MarshalV2())
		f.mvs.WriteDelta(parallel.LocationKey{Address: to, Field: parallel.FieldBalance}, i, 0, uint256.NewInt(uint64(i+1)))
	}
	return f
}

// BenchmarkApplyMVSFleetShape folds the fleet's block shape (163k
// transfers, 8k senders, 8.1k recipients) with the no-journal fold.
func BenchmarkApplyMVSFleetShape(b *testing.B) {
	f := newFleetFoldFixture(163_000, 8_000, 8_100)
	for _, w := range []int{0, 16} {
		b.Run(fmt.Sprintf("shard=%d", w), func(b *testing.B) {
			prev := collectShardWorkers
			collectShardWorkers = w
			defer func() { collectShardWorkers = prev }()
			var acct, total time.Duration
			for it := 0; it < b.N; it++ {
				b.StopTimer()
				ibs := f.newState()
				ibs.SetFoldNoJournal(true)
				b.StartTimer()
				t0 := time.Now()
				split, err := applyMVSToIBSTimed(f.mvs, f.n, ibs)
				total += time.Since(t0)
				if err != nil {
					b.Fatal(err)
				}
				acct += split.accounts
			}
			b.ReportMetric(float64(total.Microseconds())/float64(b.N)/1000, "fold-ms")
			b.ReportMetric(float64(acct.Microseconds())/float64(b.N)/1000, "acct-ms")
		})
	}
}
