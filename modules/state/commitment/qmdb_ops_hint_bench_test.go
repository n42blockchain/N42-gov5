// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"math/rand"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// benchShapeBlock is the bench's lane block: 8,000 senders (nonce and
// balance change) and 22,857 pure recipients (balance grows), plus the fee
// recipient, every account already in the tree. Round r gives each block
// its own values.
type benchShapeBlock struct {
	addrs []types.Address
	nonce []uint64
}

func newBenchShapeBlock() *benchShapeBlock {
	rnd := rand.New(rand.NewSource(42))
	b := &benchShapeBlock{}
	for i := 0; i < 8000+22857+1; i++ {
		var a types.Address
		rnd.Read(a[:])
		b.addrs = append(b.addrs, a)
		b.nonce = append(b.nonce, uint64(rnd.Intn(1000)))
	}
	return b
}

// round returns round r's dirty set and the run's finals (all but the fee
// recipient, the last address).
func (b *benchShapeBlock) round(r int) (map[types.Address]*account.StateAccount, []HintAccount) {
	accts := make(map[types.Address]*account.StateAccount, len(b.addrs))
	finals := make([]HintAccount, 0, len(b.addrs)-1)
	for i, a := range b.addrs {
		acc := account.StateAccount{Initialised: true, CodeHash: emptyCodeHashForTest}
		if i < 8000 {
			acc.Nonce = b.nonce[i] + uint64(40*r)
			acc.Balance.SetUint64(1e18 - uint64(r)*1e12)
		} else {
			acc.Balance.SetUint64(1e15 + uint64(r)*uint64(i))
		}
		cp := acc
		accts[a] = &cp
		if i < len(b.addrs)-1 {
			finals = append(finals, HintAccount{Addr: a, Acct: acc})
		}
	}
	return accts, finals
}

// BenchmarkRootOpsHint: the bench-shaped lane block's root (op build +
// apply on the sharded apply with 16 workers) today, with the run's hint,
// and with the hint built before the tree lock. Reported per block: the op
// build under the lock (opsMs), before it (preMs), the apply (applyMs), the
// exclusive hold of the tree lock (holdMs) and the hint's own build, which
// runs off the import chain (hintMs).
//
//	GOMAXPROCS=8 nice -n 19 go test -tags nosqlite,noboltdb -run NONE \
//	  -bench BenchmarkRootOpsHint -benchtime 40x ./modules/state/commitment/
func BenchmarkRootOpsHint(b *testing.B) {
	for _, c := range []struct{ name, fromRun, unlocked string }{
		{"today", "off", "off"}, {"hint", "on", "off"}, {"unlocked", "off", "on"}, {"hint+unlocked", "on", "on"},
	} {
		b.Run(c.name, func(b *testing.B) {
			prevW := qmdb.ShardApplyWorkers
			qmdb.ShardApplyWorkers = 16
			defer func() { qmdb.ShardApplyWorkers = prevW }()
			defer SetRootOpsFromRunForTest(c.fromRun)()
			defer SetRootOpsUnlockedForTest(c.unlocked)()
			blk := newBenchShapeBlock()
			rc := NewQMDBRootComputer()
			rc.t.SetIndex(qmdb.NewIndexFor(rc.t, 1<<20))
			accts, _ := blk.round(0)
			if _, err := rc.ComputeRoot(accts, nil); err != nil {
				b.Fatal(err)
			}
			rounds := make([]map[types.Address]*account.StateAccount, b.N)
			hints := make([]*AccountOpsHint, b.N)
			var hintBuild time.Duration
			for i := 0; i < b.N; i++ {
				var finals []HintAccount
				rounds[i], finals = blk.round(i + 1)
				if c.fromRun != "off" || c.unlocked != "off" {
					hints[i] = NewAccountOpsHint()
					if hints[i].Caches() {
						t0 := time.Now()
						hints[i].Build(finals)
						hintBuild += time.Since(t0)
					} else {
						hints[i].MarkBuilt()
					}
				}
			}
			var ops, pre, apply, hold time.Duration
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if hints[i] != nil {
					_, err = rc.ComputeRootHinted(rounds[i], nil, hints[i])
				} else {
					_, err = rc.ComputeRoot(rounds[i], nil)
				}
				if err != nil {
					b.Fatal(err)
				}
				p := LastComputeRootPhases()
				ops += p.Collect
				pre += p.PreOps
				apply += p.Apply
				hold += p.Hold
			}
			b.StopTimer()
			n := float64(b.N) * 1e6
			b.ReportMetric(float64(ops.Nanoseconds())/n, "opsMs")
			b.ReportMetric(float64(pre.Nanoseconds())/n, "preMs")
			b.ReportMetric(float64(apply.Nanoseconds())/n, "applyMs")
			b.ReportMetric(float64(hold.Nanoseconds())/n, "holdMs")
			b.ReportMetric(float64(hintBuild.Nanoseconds())/n, "hintMs")
		})
	}
}
