// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"encoding/binary"
	"runtime"
	"testing"

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
func BenchmarkPrepareStateWrite(b *testing.B) {
	defer SetHistoryIndexDeferredForTest(true)()
	const senders, recipients = 8000, 22857
	db := memdb.NewTestDB(b)
	rules := params.MainnetChainConfig.Rules(1)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		var ibs *IntraBlockState
		_ = db.View(context.Background(), func(tx kv.Tx) error {
			ibs = New(NewPlainStateReader(tx))
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
			if _, err := PrepareStateWrite(ibs, rules, 1000); err != nil {
				b.Fatal(err)
			}
			return nil
		})
	}
}
