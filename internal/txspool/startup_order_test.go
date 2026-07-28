package txspool

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// TestRequestPromoteExecutablesBlocksWithoutScheduler pins the mechanism behind
// the startup wedge: reqPromoteCh is unbuffered and only scheduleLoop drains it,
// so every ingestion path parks indefinitely until that loop exists.
func TestRequestPromoteExecutablesBlocksWithoutScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := &TxsPool{
		ctx:          ctx,
		cancel:       cancel,
		reqPromoteCh: make(chan *accountSet),
		reorgDoneCh:  make(chan chan struct{}),
	}

	returned := make(chan struct{})
	go func() {
		pool.requestPromoteExecutables(newAccountSet())
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("requestPromoteExecutables returned with no scheduler draining reqPromoteCh; " +
			"the startup ordering guard in NewTxsPool is no longer load-bearing, re-check this test")
	case <-time.After(200 * time.Millisecond):
	}

	// Only cancellation can release it — there is no other reader.
	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("requestPromoteExecutables did not unblock after context cancellation")
	}
}

// TestRequestPromoteExecutablesProceedsWithScheduler is the positive half: with a
// drainer in place (what NewTxsPool now guarantees before the journal restore),
// the same call completes.
func TestRequestPromoteExecutablesProceedsWithScheduler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := &TxsPool{
		ctx:          ctx,
		cancel:       cancel,
		reqPromoteCh: make(chan *accountSet),
		reorgDoneCh:  make(chan chan struct{}),
	}

	// Stand in for scheduleLoop: take the promote request, hand back a done chan.
	go func() {
		<-pool.reqPromoteCh
		done := make(chan struct{})
		close(done)
		select {
		case pool.reorgDoneCh <- done:
		case <-ctx.Done():
		}
	}()

	returned := make(chan struct{})
	go func() {
		pool.requestPromoteExecutables(newAccountSet())
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("requestPromoteExecutables blocked despite a running scheduler")
	}
}

// TestLoadFromDBSkipsWhenSchedulerNotLive covers the guard that keeps a
// re-introduced ordering mistake survivable: the restore is skipped, the journal
// is left on disk for the next start, and the caller returns instead of hanging.
func TestLoadFromDBSkipsWhenSchedulerNotLive(t *testing.T) {
	db := newJournalTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tx := newPersistedTestTx(0)
	enc, err := tx.Marshal()
	if err != nil {
		t.Fatalf("marshal test tx: %v", err)
	}
	hash := tx.Hash()
	if err := db.Update(ctx, func(dbTx kv.RwTx) error {
		return dbTx.Put(modules.TxPoolJournal, hash.Bytes(), enc)
	}); err != nil {
		t.Fatalf("seed journal entry: %v", err)
	}

	pool := newJournalTestPool(ctx, db) // schedulerLive is false

	done := make(chan int, 1)
	go func() { done <- pool.loadFromDB() }()

	select {
	case loaded := <-done:
		if loaded != 0 {
			t.Fatalf("loadFromDB restored %d txs without a live scheduler, want 0", loaded)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loadFromDB hung without a live scheduler; the guard is missing")
	}

	// The journal must survive the skip — dropping it here would lose the txs
	// the shutdown flush went to the trouble of persisting.
	if err := db.View(ctx, func(dbTx kv.Tx) error {
		v, gErr := dbTx.GetOne(modules.TxPoolJournal, hash.Bytes())
		if gErr != nil {
			return gErr
		}
		if len(v) == 0 {
			t.Fatal("skipped restore cleared the journal entry; it must survive for the next start")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify journal survived: %v", err)
	}
}
