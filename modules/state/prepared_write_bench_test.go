// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// prepBenchState is a transfer block's state at the write: n senders
// debited with their nonce bumped and n recipients credited, finalized.
func prepBenchState(b testing.TB, n int) *IntraBlockState {
	accts := make(map[types.Address]*account.StateAccount, 2*n)
	for i := 0; i < n; i++ {
		a := new(account.StateAccount)
		a.Initialised = true
		a.Nonce = uint64(i)
		a.Balance.SetUint64(1e18)
		accts[testAddr("s", i)] = a
		r := new(account.StateAccount)
		r.Initialised = true
		r.Balance.SetUint64(5)
		accts[testAddr("r", i)] = r
	}
	ibs := New(mapStateReader{accts: accts})
	for i := 0; i < n; i++ {
		s := testAddr("s", i)
		ibs.SubBalance(s, uint256.NewInt(21000))
		ibs.SetNonce(s, uint64(i)+1)
		ibs.AddBalance(testAddr("r", i), uint256.NewInt(7))
	}
	if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()); err != nil {
		b.Fatal(err)
	}
	return ibs
}

func BenchmarkPrepareStateWrite(b *testing.B) {
	defer SetHistoryIndexDeferredForTest(true)()
	rules := &params.Rules{IsSpuriousDragon: true}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ibs := prepBenchState(b, 40000)
		b.StartTimer()
		if _, err := PrepareStateWrite(ibs, rules, 100); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrepareStateWriteShards(b *testing.B) {
	defer SetHistoryIndexDeferredForTest(true)()
	defer SetPrepShardsForTest(8, 0)()
	rules := &params.Rules{IsSpuriousDragon: true}
	var commit, encode int64
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		ibs := prepBenchState(b, 40000)
		b.StartTimer()
		p, err := PrepareStateWrite(ibs, rules, 100)
		if err != nil {
			b.Fatal(err)
		}
		commit += int64(p.Commit)
		encode += int64(p.Encode)
	}
	b.ReportMetric(float64(commit)/float64(b.N)/1e6, "commitMs")
	b.ReportMetric(float64(encode)/float64(b.N)/1e6, "encodeMs")
}
