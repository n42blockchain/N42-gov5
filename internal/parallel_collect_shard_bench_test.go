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

// transferFoldFixture is the final MVS of a block of n plain transfers
// from distinct senders to a pool of recipients: every sender written in
// full (balance and nonce), every recipient only credited -- the shape of
// a qs block.
type transferFoldFixture struct {
	mvs   *parallel.MVS
	store bulkMapReader
	known bulkKnownCache
	n     int
}

func newTransferFoldFixture(n, recipients int) *transferFoldFixture {
	f := &transferFoldFixture{mvs: parallel.NewMVS(), store: bulkMapReader{}, known: bulkKnownCache{}, n: n}
	for r := 0; r < recipients; r++ {
		to := bulkTestAddr(-1 - r)
		if r%3 != 0 {
			acc := account.NewAccount()
			acc.Balance.SetUint64(uint64(r))
			f.store[to] = &acc
			f.known[to] = &acc
		} else {
			f.known[to] = nil
		}
	}
	for i := 0; i < n; i++ {
		from, to := bulkTestAddr(i), bulkTestAddr(-1-i%recipients)
		acc := account.NewAccount()
		acc.Balance.SetUint64(1_000_000_000)
		acc.Nonce = uint64(i % 5)
		f.store[from] = &acc
		f.known[from] = &acc
		post := acc
		post.Balance.SetUint64(1_000_000_000 - uint64(i) - 21000)
		post.Nonce++
		f.mvs.Write(parallel.LocationKey{Address: from, Field: parallel.FieldBalance}, i, 0, post.MarshalV2())
		f.mvs.WriteDelta(parallel.LocationKey{Address: to, Field: parallel.FieldBalance}, i, 0, uint256.NewInt(uint64(i+1)))
	}
	return f
}

func (f *transferFoldFixture) newState() *state.IntraBlockState {
	pf := state.NewAccountPrefetch(f.store)
	pf.SetKnown(f.known)
	ibs := state.New(pf)
	ibs.SetAccountPrefetch(pf)
	return ibs
}

// BenchmarkApplyMVSTransfers163k folds a 163k-transfer block's final
// versions (distinct senders, 23k recipients: the S128 shape) into a fresh
// state and reports the fold's split.
func BenchmarkApplyMVSTransfers163k(b *testing.B) {
	f := newTransferFoldFixture(163_000, 23_000)
	for _, w := range []int{0, 8, 16} {
		b.Run(fmt.Sprintf("shard=%d", w), func(b *testing.B) {
			prev := collectShardWorkers
			collectShardWorkers = w
			defer func() { collectShardWorkers = prev }()
			benchApplyMVSFold(b, f)
		})
	}
}

func benchApplyMVSFold(b *testing.B, f *transferFoldFixture) {
	var walk, acct, sto, total time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		b.StopTimer()
		ibs := f.newState()
		b.StartTimer()
		t0 := time.Now()
		split, err := applyMVSToIBSTimed(f.mvs, f.n, ibs)
		total += time.Since(t0)
		if err != nil {
			b.Fatal(err)
		}
		walk += split.walk
		acct += split.accounts
		sto += split.storage
	}
	n := float64(b.N)
	b.ReportMetric(float64(total.Microseconds())/n/1000, "fold-ms")
	b.ReportMetric(float64(walk.Microseconds())/n/1000, "walk-ms")
	b.ReportMetric(float64(acct.Microseconds())/n/1000, "acct-ms")
	b.ReportMetric(float64(sto.Microseconds())/n/1000, "sto-ms")
}
