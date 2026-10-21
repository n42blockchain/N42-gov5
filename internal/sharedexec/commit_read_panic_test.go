package sharedexec

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type certifiedPanicReadDB struct {
	kv.RwDB
	armed        atomic.Bool
	entered      chan struct{}
	release      chan struct{}
	enterOnce    sync.Once
	receiptReads atomic.Int64
	writes       atomic.Int64
}

type certifiedPanicReadTx struct {
	kv.Tx
	db *certifiedPanicReadDB
}

func (d *certifiedPanicReadDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.RwDB.View(ctx, func(tx kv.Tx) error {
		return fn(&certifiedPanicReadTx{Tx: tx, db: d})
	})
}

func (t *certifiedPanicReadTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.Receipts {
		t.db.receiptReads.Add(1)
		if t.db.armed.CompareAndSwap(true, false) {
			t.db.enterOnce.Do(func() { close(t.db.entered) })
			<-t.db.release
			panic("injected certified read panic")
		}
	}
	return t.Tx.GetOne(table, key)
}

func (d *certifiedPanicReadDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	d.writes.Add(1)
	return d.RwDB.Update(ctx, fn)
}

func (d *certifiedPanicReadDB) UpdateNosync(ctx context.Context, fn func(kv.RwTx) error) error {
	d.writes.Add(1)
	return d.RwDB.UpdateNosync(ctx, fn)
}

func (d *certifiedPanicReadDB) BeginRw(ctx context.Context) (kv.RwTx, error) {
	d.writes.Add(1)
	return d.RwDB.BeginRw(ctx)
}

func (d *certifiedPanicReadDB) BeginRwNosync(ctx context.Context) (kv.RwTx, error) {
	d.writes.Add(1)
	return d.RwDB.BeginRwNosync(ctx)
}

func awaitCertifiedReadPanic(t *testing.T, db *certifiedPanicReadDB) {
	t.Helper()
	select {
	case <-db.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("certified duplicate reader did not reach injected panic")
	}
}

func TestCertifiedDuplicateReadPanicFailsWaitersAndAllowsFreshRetry(t *testing.T) {
	var db *certifiedPanicReadDB
	f, _, request := certifiedCanonicalFixtureWithDB(t, true, func(base kv.RwDB) kv.RwDB {
		db = &certifiedPanicReadDB{RwDB: base, entered: make(chan struct{}), release: make(chan struct{})}
		return db
	})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(db.release) }) }
	t.Cleanup(release)
	db.writes.Store(0)
	db.receiptReads.Store(0)
	db.armed.Store(true)

	results := make(chan error, 3)
	var callers sync.WaitGroup
	start := func() {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, err := f.server.CommitCertified(context.Background(), request)
			results <- err
		}()
	}
	_, initialJoins := f.server.CertifiedDuplicateReadCounts()
	start()
	awaitCertifiedReadPanic(t, db)
	job := certifiedFlightForTest(t, f.server, request)
	if job == nil {
		t.Fatal("panicking physical reader did not register a flight")
	}
	start()
	start()
	awaitCertifiedJoins(t, f.server, initialJoins+2)
	if certifiedFlightForTest(t, f.server, request) != job {
		t.Fatal("identical callers did not join the blocked panicking flight")
	}
	release()
	callers.Wait()
	for range 3 {
		if err := <-results; err != errCertifiedDuplicateReadPanic {
			t.Fatalf("panic waiter did not receive isolated read error: %v", err)
		}
	}
	f.server.wg.Wait()

	f.server.mu.Lock()
	remaining := len(f.server.certifiedReads)
	f.server.mu.Unlock()
	if remaining != 0 || len(f.server.certifiedReadSlots) != 0 || db.writes.Load() != 0 {
		t.Fatalf("panicking read leaked flight/slot or wrote to DB: flights=%d slots=%d writes=%d", remaining, len(f.server.certifiedReadSlots), db.writes.Load())
	}
	if !f.server.execute.TryLock() {
		t.Fatal("panicking read retained the shared execution lock")
	}
	f.server.execute.Unlock()
	if db.receiptReads.Load() != 1 {
		t.Fatalf("panicking physical read count differs: %d", db.receiptReads.Load())
	}

	flightsBefore, joinsBefore := f.server.CertifiedDuplicateReadCounts()
	result, err := f.server.CommitCertified(context.Background(), request)
	if err != nil || !result.Committed {
		t.Fatalf("fresh request did not retry verification after panic: %+v %v", result, err)
	}
	f.server.wg.Wait()
	flightsAfter, joinsAfter := f.server.CertifiedDuplicateReadCounts()
	f.server.mu.Lock()
	remaining = len(f.server.certifiedReads)
	f.server.mu.Unlock()
	if flightsAfter != flightsBefore+1 || joinsAfter != joinsBefore || db.receiptReads.Load() < 2 || db.writes.Load() != 0 || remaining != 0 || len(f.server.certifiedReadSlots) != 0 {
		t.Fatalf("fresh retry did not complete a clean physical read: flights=%d/%d joins=%d/%d receipt_reads=%d writes=%d jobs=%d slots=%d", flightsBefore, flightsAfter, joinsBefore, joinsAfter, db.receiptReads.Load(), db.writes.Load(), remaining, len(f.server.certifiedReadSlots))
	}
	if !f.server.execute.TryLock() {
		t.Fatal("fresh retry retained the shared execution lock")
	}
	f.server.execute.Unlock()
}
