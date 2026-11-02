// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// benchAddr is the i-th address of a domain, its leading bytes spread like
// a hash's.
func benchAddr(domain byte, i int) types.Address {
	var a types.Address
	binary.BigEndian.PutUint64(a[:8], uint64(i)*0x9e3779b97f4a7c15^uint64(domain)<<56)
	a[8] = domain
	binary.BigEndian.PutUint64(a[12:], uint64(i))
	return a
}

// BenchmarkPrepareStateWrite: a full E1N26 transfer block's state write
// (8000 senders, 22857 recipients, run 16) prepared off the transaction.
// With fold set the recipients' credits stay pending past FinalizeTx, as
// in an import under N42_FOLD_NOJOURNAL (detached pending credits), so the
// write materialises their objects (the Load phase).
func BenchmarkPrepareStateWrite(b *testing.B) {
	for _, fold := range []bool{false, true} {
		b.Run(fmt.Sprintf("fold=%v", fold), func(b *testing.B) {
			defer SetFoldNoJournalForTest(fold)()
			benchPrepareStateWrite(b, fold)
		})
	}
}

func benchPrepareStateWrite(b *testing.B, detached bool) {
	defer SetHistoryIndexDeferredForTest(true)()
	const senders, recipients = 8000, 22857
	db := memdb.NewTestDB(b)
	rules := params.MainnetChainConfig.Rules(1)
	b.ReportAllocs()
	var load, commit, encode time.Duration
	var pending int
	defer func() {
		n := float64(b.N)
		b.ReportMetric(float64(load.Microseconds())/n, "load-us")
		b.ReportMetric(float64(commit.Microseconds())/n, "commit-us")
		b.ReportMetric(float64(encode.Microseconds())/n, "encode-us")
		b.ReportMetric(float64(pending)/n, "pending")
	}()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		var ibs *IntraBlockState
		_ = db.View(context.Background(), func(tx kv.Tx) error {
			ibs = New(NewPlainStateReader(tx))
			ibs.SetDetachedPendingCredits(detached)
			for s := 0; s < senders; s++ {
				a := benchAddr(1, s)
				ibs.AddBalance(a, uint256.NewInt(1e18))
				ibs.SetNonce(a, 20)
			}
			for r := 0; r < recipients; r++ {
				ibs.AddBalance(benchAddr(2, r), uint256.NewInt(7))
			}
			ibs.FinalizeTx(rules, NewNoopWriter())
			runtime.GC()
			b.StartTimer()
			p, err := PrepareStateWrite(ibs, rules, 1000)
			if err != nil {
				b.Fatal(err)
			}
			ph := p.Phases()
			load += ph.Load
			commit += ph.Commit
			encode += ph.Encode
			pending += ph.Pending
			return nil
		})
	}
}
