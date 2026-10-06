package state

import (
	"context"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// Model the miner's unsorted map materialization and spread-recipient block.
// Setup and rollback are excluded; the timed operation writes a whole index.
func BenchmarkWriteHistoryBaselineSpread(b *testing.B) {
	benchmarkWriteHistorySpread(b, false, writeIndexBeforeCursor)
}

func BenchmarkWriteHistoryBaselineSpreadWarm(b *testing.B) {
	benchmarkWriteHistorySpread(b, true, writeIndexBeforeCursor)
}

func benchmarkWriteHistorySpread(b *testing.B, warm bool, writer func(uint64, *changeset.ChangeSet, string, kv.RwTx) error) {
	const accounts = 163000
	order := rand.New(rand.NewSource(42)).Perm(accounts)
	cs := changeset.NewAccountChangeSet()
	for _, n := range order {
		key := make([]byte, 20)
		binary.BigEndian.PutUint64(key[12:], uint64(n))
		if err := cs.Add(key, nil); err != nil {
			b.Fatal(err)
		}
	}
	original := append([]changeset.Change(nil), cs.Changes...)
	db := memdb.NewTestDB(b)
	if warm {
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			return writer(50, cs, modules.AccountsHistory, tx)
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		copy(cs.Changes, original)
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		err = writer(100, cs, modules.AccountsHistory, tx)
		b.StopTimer()
		tx.Rollback()
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func BenchmarkWriteHistorySpread(b *testing.B) {
	benchmarkWriteHistorySpread(b, false, writeIndex)
}
func BenchmarkWriteHistorySpreadWarm(b *testing.B) {
	benchmarkWriteHistorySpread(b, true, writeIndex)
}
