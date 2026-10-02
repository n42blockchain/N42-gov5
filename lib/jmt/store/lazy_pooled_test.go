package store

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/jmt"
)

func TestLazyDBStoreGetHasMissAndHit(t *testing.T) {
	db := newTestMDBXDB(t)
	ctx := t.Context()

	h := jmt.Hash{0x55}
	data := []byte("lazy-payload")

	// Write via a short RW tx first.
	rwTx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewMDBXStore(rwTx, JMTNodeTable).Put(h, data); err != nil {
		t.Fatal(err)
	}
	if err := rwTx.Commit(); err != nil {
		t.Fatal(err)
	}

	lazy := NewLazyDBStore(ctx, db, JMTNodeTable)

	if has, err := lazy.Has(h); err != nil || !has {
		t.Fatalf("Has = %v, %v; want true, nil", has, err)
	}
	got, err := lazy.Get(h)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}

	missHash := jmt.Hash{0x66}
	if has, err := lazy.Has(missHash); err != nil || has {
		t.Fatalf("Has(miss) = %v, %v; want false, nil", has, err)
	}
	if _, err := lazy.Get(missHash); err != jmt.ErrNotFound {
		t.Fatalf("Get(miss) err = %v, want ErrNotFound", err)
	}

	// Put/Delete are no-ops but must not error.
	if err := lazy.Put(h, data); err != nil {
		t.Fatalf("Put (no-op): %v", err)
	}
	if err := lazy.Delete(h); err != nil {
		t.Fatalf("Delete (no-op): %v", err)
	}
}

func TestPooledDBStoreGetHasAndRefresh(t *testing.T) {
	db := newTestMDBXDB(t)
	ctx := t.Context()

	h := jmt.Hash{0x77}
	data := []byte("pooled-payload")

	rwTx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewMDBXStore(rwTx, JMTNodeTable).Put(h, data); err != nil {
		t.Fatal(err)
	}
	if err := rwTx.Commit(); err != nil {
		t.Fatal(err)
	}

	pooled := NewPooledDBStore(ctx, db, JMTNodeTable)
	defer pooled.Close()

	got, err := pooled.Get(h)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
	if has, err := pooled.Has(h); err != nil || !has {
		t.Fatalf("Has = %v, %v; want true, nil", has, err)
	}

	missHash := jmt.Hash{0x88}
	if _, err := pooled.Get(missHash); err != jmt.ErrNotFound {
		t.Fatalf("Get(miss) err = %v, want ErrNotFound", err)
	}

	// Put/Delete no-ops.
	if err := pooled.Put(h, data); err != nil {
		t.Fatalf("Put (no-op): %v", err)
	}
	if err := pooled.Delete(h); err != nil {
		t.Fatalf("Delete (no-op): %v", err)
	}

	// Explicit refresh.
	pooled.RefreshTx()
	got, err = pooled.Get(h)
	if err != nil {
		t.Fatalf("Get after RefreshTx: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get after RefreshTx = %q, want %q", got, data)
	}
}

func TestPooledDBStoreStaleTxRefresh(t *testing.T) {
	db := newTestMDBXDB(t)
	ctx := t.Context()

	pooled := NewPooledDBStore(ctx, db, JMTNodeTable)
	defer pooled.Close()
	// Force the next Get/Has to treat the current tx as stale, exercising
	// refreshIfStale's rollback-and-reopen path without a real sleep.
	pooled.maxAge = 0
	time.Sleep(time.Millisecond)

	h := jmt.Hash{0x99}
	if _, err := pooled.Get(h); err != jmt.ErrNotFound {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
}

func TestPooledDBStoreCloseIsIdempotent(t *testing.T) {
	db := newTestMDBXDB(t)
	ctx := t.Context()

	pooled := NewPooledDBStore(ctx, db, JMTNodeTable)
	pooled.Close()
	pooled.Close() // must not panic on a second close
}
