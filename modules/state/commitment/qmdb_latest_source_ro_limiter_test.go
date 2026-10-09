// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestLatestAccountEvictedNestedBeginRoDeadlock reproduces the S85 leader wedge
// (docs/QS_HANDOVER_20261004.md "S85 result"). QMDBLatestAccountSource.LatestAccount
// is called from inside a caller's read transaction (the txpool's
// GetAccountsInfo -> db.View), which already holds one roTxsLimiter permit. When
// the entry was evicted by a flush the caller's transaction predates, it opens a
// SECOND read transaction (qmdb_latest_source.go, s.db.BeginRo) while still
// holding the first. With as many concurrent readers as the limiter has permits
// (the node sizes it max(32, 8*GOMAXPROCS); the wedged leaders had 9,580 ingest
// goroutines parked in BeginRo), every permit is held by a reader waiting for one
// more: no reader ever finishes, and the miner's Block-STM workers, which also
// need permits, never start. The fallback now opens its transaction with
// kv.BeginRoNested, which takes no permit.
func TestLatestAccountEvictedNestedBeginRoDeadlock(t *testing.T) {
	const permits = 8
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	sem := semaphore.NewWeighted(permits)
	db := mdbx.NewMDBX(log.New()).InMem(t.TempDir()).RoTxsLimiter(sem).MustOpen()
	wedged := false
	t.Cleanup(func() {
		if !wedged {
			db.Close()
		}
	})

	rc := NewQMDBRootComputer()
	rc.SetEvictLag(0)
	src := NewQMDBLatestAccountSource(rc, db)

	// Block 1 applied but not yet persisted: its entries are resident.
	dirty := map[types.Address]*account.StateAccount{}
	addrs := make([]types.Address, permits)
	for i := range addrs {
		addrs[i] = types.Address{0xA0, byte(i)}
		dirty[addrs[i]] = testAcct(uint64(i+1), 100)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		_, err := rc.ComputeRoot(dirty, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.SetCold(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // on a wedge, unblocks the parked Acquire calls so the DB can close

	// One reader per permit, each in a transaction that predates the flush.
	var started, finished sync.WaitGroup
	release := make(chan struct{})
	started.Add(permits)
	finished.Add(permits)
	for i := 0; i < permits; i++ {
		go func() {
			defer finished.Done()
			_ = db.View(ctx, func(tx kv.Tx) error {
				started.Done()
				<-release
				_, _ = src.LatestAccount(tx, addrs[i][:])
				return nil
			})
		}()
	}
	started.Wait()

	// The owner persists block 1 and evicts it.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)

	close(release)
	done := make(chan struct{})
	go func() { finished.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		wedged = true // leak the parked readers; Close would wait for them forever
		t.Fatalf("deadlock: %d readers each hold a roTxsLimiter permit and wait for a second one", permits)
	}
}
