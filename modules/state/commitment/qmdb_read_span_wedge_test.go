// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// TestReadSpanPermitWedge reproduces the S86 fleet wedge (round 35zzzbl):
// a reader-lock span whose workers wait for a read-transaction permit while
// the lock is held.
//
// The leader's Block-STM run (and its credit prefetch, and a follower's
// deferred sender check) takes the QMDB tree's reader lock and then opens one
// read transaction per worker. The import of the previous block, on another
// goroutine, queues for the writer side (SetCold / FlushTo / EvictFlushed),
// and from then on every new reader lock waits. The txpool's batch reads hold
// a limiter permit through db.View and take the reader lock in Lookup, so
// they park on their permits; with ingest flooding, every permit is held by
// one of them. The span's workers then wait for a permit that never frees:
// span -> permits -> pool readers -> writer -> span.
//
// The span now takes its permit before the lock and opens its workers'
// transactions under it (kv.BeginRoNested), so it finishes, the writer
// flushes and evicts (sending some pool readers through the nested fallback
// of QMDBLatestAccountSource), and every reader completes.
func TestReadSpanPermitWedge(t *testing.T) {
	const (
		permits = 8
		readers = permits * 4
		workers = 4
	)
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

	// Block 1 applied, not yet persisted: the import is about to flush it.
	dirty := map[types.Address]*account.StateAccount{}
	addrs := make([]types.Address, readers)
	for i := range addrs {
		addrs[i] = types.Address{0xB0, byte(i)}
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
	defer cancel()

	// The miner's span: permit (now) and reader lock.
	span, err := rc.BeginReadSpan(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	// The import of the block: queues for the writer side behind the span.
	writerDone := make(chan error, 1)
	go func() {
		err := db.Update(context.Background(), func(tx kv.RwTx) error {
			rc.SetCold(tx)
			_, err := rc.FlushTo(tx)
			return err
		})
		if err == nil {
			rc.CommitFlushed()
			rc.EvictFlushed()
			rc.TakeUndo()
			rc.SetCold(nil)
		}
		writerDone <- err
	}()
	waitUntil(t, "writer queued on the reader lock", func() bool {
		if rc.readers.TryRLock() {
			rc.readers.RUnlock()
			return false
		}
		return true
	})

	// Ingest: many txpool-style batch reads, each holding a permit through
	// its view while it takes the reader lock.
	var readersDone sync.WaitGroup
	readersDone.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer readersDone.Done()
			_ = db.View(ctx, func(tx kv.Tx) error {
				_, err := src.LatestAccount(tx, addrs[i][:])
				return err
			})
		}()
	}
	waitUntil(t, "limiter drained by ingest reads", func() bool {
		if sem.TryAcquire(1) {
			sem.Release(1)
			return false
		}
		return true
	})

	// The span's Block-STM workers: one read transaction each, opened on
	// their own goroutines under the held reader lock.
	var workersDone sync.WaitGroup
	workersDone.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer workersDone.Done()
			tx, err := span.BeginRo(ctx)
			if err != nil {
				return
			}
			defer tx.Rollback()
			_, _, _ = rc.LookupLocked(qmdb.Hash(AccountKeyHash(addrs[w])), tx)
		}()
	}

	done := make(chan struct{})
	go func() {
		workersDone.Wait()
		span.End()
		readersDone.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		wedged = true // leak the parked goroutines; Close would wait for them forever
		buf := make([]byte, 1<<20)
		t.Logf("goroutines:\n%s", buf[:runtime.Stack(buf, true)])
		t.Fatalf("wedge: the span's workers wait for a permit under the reader lock; %d ingest reads hold every permit queued behind the writer", readers)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	// The flush landed and the readers saw the block.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		v, err := src.LatestAccount(tx, addrs[0][:])
		if err != nil {
			return err
		}
		if len(v) == 0 {
			t.Fatal("account missing after the flush")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("setup: %s did not happen", what)
		}
		time.Sleep(time.Millisecond)
	}
}
