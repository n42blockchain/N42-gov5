package layered

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

func TestLayeredDB_AccessorsAndRoDBInterface(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()

	if db.dbFor("Account") != db.stateDB {
		t.Fatal("expected hot table to route to stateDB")
	}
	if db.dbFor("AccountChangeSet") != db.historyDB {
		t.Fatal("expected cold table to route to historyDB")
	}
	if db.StateDB() != db.stateDB {
		t.Fatal("StateDB() mismatch")
	}
	if db.HistoryDB() != db.historyDB {
		t.Fatal("HistoryDB() mismatch")
	}
	if db.ReadOnly() {
		t.Fatal("expected stateDB to report not read-only")
	}
	if db.PageSize() == 0 {
		t.Fatal("expected non-zero page size")
	}
	if db.CHandle() == nil {
		t.Log("CHandle() returned nil; acceptable for memdb backend")
	}

	tables := db.AllTables()
	if _, ok := tables["Account"]; !ok {
		t.Fatal("expected merged AllTables to contain Account")
	}
}

func TestLayeredDB_ExtractCache(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()

	if c := ExtractCache(db); c != db.Cache() {
		t.Fatal("ExtractCache should return the LayeredDB's cache")
	}

	// A non-LayeredDB implementation should yield nil.
	if c := ExtractCache(db.StateDB()); c != nil {
		t.Fatal("ExtractCache on a non-LayeredDB should return nil")
	}
}

func TestLayeredDB_CollectMetrics(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("k"), []byte("v"))
	}); err != nil {
		t.Fatal(err)
	}
	_ = db.View(ctx, func(tx kv.Tx) error {
		_, err := tx.GetOne("Account", []byte("k"))
		return err
	})

	db.CollectMetrics() // must not panic
}

func TestLayeredDB_BeginRwNosyncAndUpdateNosync(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.UpdateNosync(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("nosync-key"), []byte("nosync-val"))
	}); err != nil {
		t.Fatal(err)
	}

	err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("nosync-key"))
		if err != nil {
			return err
		}
		if string(v) != "nosync-val" {
			t.Errorf("got %q, want nosync-val", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Exercise BeginRwNosync directly too.
	rwTx, err := db.BeginRwNosync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rwTx.Rollback()
}

func TestLayeredTx_ColdTableAndTxLevelMethods(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put("Account", []byte("hot1"), []byte("v1")); err != nil {
			return err
		}
		if err := tx.Put("AccountChangeSet", []byte("cold1"), []byte("cv1")); err != nil {
			return err
		}
		has, err := tx.Has("Account", []byte("hot1"))
		if err != nil || !has {
			t.Fatalf("Has(hot1) = %v, %v; want true, nil", has, err)
		}
		hasCold, err := tx.Has("AccountChangeSet", []byte("cold1"))
		if err != nil || !hasCold {
			t.Fatalf("Has(cold1) = %v, %v; want true, nil", hasCold, err)
		}

		seq, err := tx.IncrementSequence("Account", 1)
		if err != nil {
			t.Fatalf("IncrementSequence: %v", err)
		}
		if rs, err := tx.ReadSequence("Account"); err != nil || rs != seq+1 {
			t.Fatalf("ReadSequence = %d, %v; want %d, nil", rs, err, seq+1)
		}

		if err := tx.Append("Account", []byte("zzz-append"), []byte("av")); err != nil {
			t.Fatalf("Append: %v", err)
		}

		if _, err := tx.Cursor("Account"); err != nil {
			t.Fatalf("Cursor: %v", err)
		}
		if _, err := tx.RwCursor("Account"); err != nil {
			t.Fatalf("RwCursor: %v", err)
		}

		if id := tx.ViewID(); id == 0 {
			t.Log("ViewID() returned 0; acceptable depending on backend")
		}
		if _, err := tx.BucketSize("Account"); err != nil {
			t.Fatalf("BucketSize: %v", err)
		}
		if _, err := tx.DBSize(); err != nil {
			t.Fatalf("DBSize: %v", err)
		}
		if _, err := tx.ListBuckets(); err != nil {
			t.Fatalf("ListBuckets: %v", err)
		}

		var foundEach bool
		if err := tx.ForEach("Account", nil, func(k, v []byte) error {
			foundEach = true
			return nil
		}); err != nil {
			t.Fatalf("ForEach: %v", err)
		}
		if !foundEach {
			t.Fatal("expected ForEach to visit at least one entry")
		}

		if err := tx.ForPrefix("Account", []byte("hot"), func(k, v []byte) error { return nil }); err != nil {
			t.Fatalf("ForPrefix: %v", err)
		}
		if err := tx.ForAmount("Account", nil, 1, func(k, v []byte) error { return nil }); err != nil {
			t.Fatalf("ForAmount: %v", err)
		}

		if _, err := tx.Range("Account", nil, nil); err != nil {
			t.Fatalf("Range: %v", err)
		}
		if _, err := tx.RangeAscend("Account", nil, nil, -1); err != nil {
			t.Fatalf("RangeAscend: %v", err)
		}
		if _, err := tx.RangeDescend("Account", nil, nil, -1); err != nil {
			t.Fatalf("RangeDescend: %v", err)
		}
		if _, err := tx.Prefix("Account", []byte("hot")); err != nil {
			t.Fatalf("Prefix: %v", err)
		}

		if tx.CHandle() == nil {
			t.Log("CHandle() returned nil; acceptable for memdb backend")
		}

		tx.CollectMetrics() // must not panic

		if err := tx.CreateBucket("CustomBucket"); err != nil {
			t.Fatalf("CreateBucket: %v", err)
		}
		exists, err := tx.ExistsBucket("CustomBucket")
		if err != nil || !exists {
			t.Fatalf("ExistsBucket = %v, %v; want true, nil", exists, err)
		}
		if err := tx.ClearBucket("Account"); err != nil {
			t.Fatalf("ClearBucket: %v", err)
		}
		// DropBucket only permits deprecated buckets; exercising it here
		// just confirms the call is forwarded (an error is expected).
		if err := tx.DropBucket("CustomBucket"); err == nil {
			t.Log("DropBucket unexpectedly succeeded on a non-deprecated bucket")
		}

		if err := tx.Delete("Account", []byte("zzz-append")); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLayeredTx_RangeDupSortAndCursorDupSort(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	err := db.View(ctx, func(tx kv.Tx) error {
		// Account is not declared DupSort in the table cfg used by tests here,
		// but the call should still forward without panicking; an error from
		// the underlying implementation is an acceptable outcome to exercise
		// the passthrough.
		_, _ = tx.CursorDupSort("Account")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredRwTx_RollbackDiscardsPendingCache(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	rwTx, err := db.BeginRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rwTx.Put("Account", []byte("rb-key"), []byte("rb-val")); err != nil {
		t.Fatal(err)
	}
	rwTx.Rollback()

	err = db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("rb-key"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Fatalf("expected rolled-back key to be absent, got %q", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredRwTx_ClearBucketResetsCache(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put("Account", []byte("ck"), []byte("cv"))
	}); err != nil {
		t.Fatal(err)
	}
	// Warm the cache.
	_ = db.View(ctx, func(tx kv.Tx) error {
		_, err := tx.GetOne("Account", []byte("ck"))
		return err
	})

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.ClearBucket("Account")
	}); err != nil {
		t.Fatal(err)
	}

	err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("ck"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Fatalf("expected cleared bucket to return nil, got %q", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayeredRwTx_DeleteNilValueRoundTrip(t *testing.T) {
	db := newTestLayeredDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put("Account", []byte("del-key"), []byte("del-val")); err != nil {
			return err
		}
		return tx.Delete("Account", []byte("del-key"))
	}); err != nil {
		t.Fatal(err)
	}

	err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne("Account", []byte("del-key"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Fatalf("expected deleted key to be absent, got %q", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
