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
	"github.com/n42blockchain/N42/modules/state"
)

// newLiveFoldFixture is the final MVS of n transfers from senders accounts
// (each sending in turn, so a sender has many versions) to recipients
// credited accounts -- the qs node0 shape of run 33 (52.5k transfers over
// all 22,857 accounts).
func newLiveFoldFixture(n, senders, recipients int) *transferFoldFixture {
	f := &transferFoldFixture{mvs: parallel.NewMVS(), store: bulkMapReader{}, known: bulkKnownCache{}, n: n}
	for r := 0; r < recipients; r++ {
		to := bulkTestAddr(-1 - r)
		acc := account.NewAccount()
		acc.Balance.SetUint64(uint64(r))
		f.store[to] = &acc
		f.known[to] = &acc
	}
	bal := make([]account.StateAccount, senders)
	for s := 0; s < senders; s++ {
		acc := account.NewAccount()
		acc.Balance.SetUint64(1_000_000_000_000)
		f.store[bulkTestAddr(s)] = &acc
		f.known[bulkTestAddr(s)] = &acc
		bal[s] = acc
	}
	for i := 0; i < n; i++ {
		s := i % senders
		from, to := bulkTestAddr(s), bulkTestAddr(-1-i%recipients)
		bal[s].Balance.Sub(&bal[s].Balance, uint256.NewInt(21000))
		bal[s].Nonce++
		f.mvs.Write(parallel.LocationKey{Address: from, Field: parallel.FieldBalance}, i, 0, bal[s].MarshalV2())
		f.mvs.WriteDelta(parallel.LocationKey{Address: to, Field: parallel.FieldBalance}, i, 0, uint256.NewInt(uint64(i+1)))
	}
	return f
}

// BenchmarkR68aFoldLive folds the live shapes with N42_AHEAD_COLLECT_CUT
// off (cutW=0) and on at several walk worker caps, under the no-journal
// fold and the sharded account pass (N42_COLLECT_SHARD=16), as on node0.
func BenchmarkR68aFoldLive(b *testing.B) {
	for _, sh := range []struct{ n, s, r int }{{52500, 5600, 5600}, {52500, 22857, 22857}, {163000, 22857, 22857}} {
		f := newLiveFoldFixture(sh.n, sh.s, sh.r)
		for _, mw := range []int{0, 12, 16, 24, 32} {
			cut := mw > 0
			b.Run(fmt.Sprintf("n=%d/s=%d/cutW=%d", sh.n, sh.s, mw), func(b *testing.B) {
				defer state.SetFoldObjectSlabForTest(cut)()
				pm := collectCutMaxWorkers
				collectCutMaxWorkers = mw
				defer func() { collectCutMaxWorkers = pm }()
				prev := collectShardWorkers
				collectShardWorkers = 16
				defer func() { collectShardWorkers = prev }()
				var walk, acct, total time.Duration
				for it := 0; it < b.N; it++ {
					b.StopTimer()
					ibs := f.newState()
					ibs.SetDetachedPendingCredits(true)
					ibs.SetFoldNoJournal(true)
					b.StartTimer()
					t0 := time.Now()
					split, err := applyMVSToIBSTimed(f.mvs, f.n, ibs)
					total += time.Since(t0)
					if err != nil {
						b.Fatal(err)
					}
					walk += split.walk
					acct += split.accounts
				}
				n := float64(b.N)
				b.ReportMetric(float64(total.Microseconds())/n/1000, "fold-ms")
				b.ReportMetric(float64(walk.Microseconds())/n/1000, "walk-ms")
				b.ReportMetric(float64(acct.Microseconds())/n/1000, "acct-ms")
			})
		}
	}
}
