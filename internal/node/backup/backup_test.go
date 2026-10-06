package backup

import (
	"context"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbx2 "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// backupSeedSource creates an on-disk ChainDB-labelled MDBX environment at
// dir, writes one row into a plain table and one into a dupsort table, then
// closes it so a later Accede-mode open (as OpenPair performs on the source)
// finds a clean, already-initialized environment.
func backupSeedSource(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	db := mdbx2.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.TablesCfgByLabel(kv.ChainDB) }).
		MustOpen()
	defer db.Close()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put(kv.HeaderNumber, []byte("hash-a"), []byte("1")); err != nil {
			return err
		}
		c, err := tx.RwCursorDupSort(kv.AccountChangeSet)
		if err != nil {
			return err
		}
		return c.AppendDup([]byte("block-1"), []byte("account-change"))
	}); err != nil {
		t.Fatalf("seed source db: %v", err)
	}
}

// TestOpenPairAndKv2kvCopiesPlainAndDupsortTables exercises OpenPair's
// readonly-accede source + fresh writable destination setup, then Kv2kv's
// plain-table Append and dupsort-table AppendDup copy paths end to end.
func TestOpenPairAndKv2kvCopiesPlainAndDupsortTables(t *testing.T) {
	from := t.TempDir()
	to := t.TempDir()
	backupSeedSource(t, from)

	src, dst := OpenPair(from, to, kv.ChainDB, datasize.ByteSize(0))
	defer src.Close()
	defer dst.Close()

	ctx := context.Background()
	if err := Kv2kv(ctx, src, dst, []string{kv.HeaderNumber, kv.AccountChangeSet}, 1); err != nil {
		t.Fatalf("Kv2kv: %v", err)
	}

	if err := dst.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(kv.HeaderNumber, []byte("hash-a"))
		if err != nil {
			return err
		}
		if string(v) != "1" {
			t.Fatalf("HeaderNumber value = %q, want %q", v, "1")
		}
		c, err := tx.CursorDupSort(kv.AccountChangeSet)
		if err != nil {
			return err
		}
		k, v, err := c.First()
		if err != nil {
			return err
		}
		if string(k) != "block-1" || string(v) != "account-change" {
			t.Fatalf("AccountChangeSet first entry = (%q, %q)", k, v)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify destination: %v", err)
	}
}

// TestKv2kvCopiesAllTablesWhenNoneSelected exercises the branch of Kv2kv
// that iterates every non-deprecated table in the source database when the
// caller passes an empty table filter.
func TestKv2kvCopiesAllTablesWhenNoneSelected(t *testing.T) {
	from := t.TempDir()
	to := t.TempDir()
	backupSeedSource(t, from)

	src, dst := OpenPair(from, to, kv.ChainDB, datasize.ByteSize(0))
	defer src.Close()
	defer dst.Close()

	ctx := context.Background()
	if err := Kv2kv(ctx, src, dst, nil, 1); err != nil {
		t.Fatalf("Kv2kv with no table filter: %v", err)
	}

	if err := dst.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(kv.HeaderNumber, []byte("hash-a"))
		if err != nil {
			return err
		}
		if string(v) != "1" {
			t.Fatalf("HeaderNumber value = %q, want %q", v, "1")
		}
		return nil
	}); err != nil {
		t.Fatalf("verify destination: %v", err)
	}
}

// TestWarmupTableSkipsBelowThreshold confirms WarmupTable returns promptly
// without launching any reader goroutines when the table has fewer than
// 10,000 rows (the documented skip threshold), exercising the function
// against a real on-disk database without needing to seed 10k rows.
func TestWarmupTableSkipsBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	backupSeedSource(t, dir)

	db := mdbx2.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.TablesCfgByLabel(kv.ChainDB) }).
		MustOpen()
	defer db.Close()

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		WarmupTable(ctx, db, kv.HeaderNumber, log.LvlTrace, 4)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WarmupTable did not return promptly for a small table")
	}
}

// TestClearTableRemovesRows exercises ClearTable's warmup-then-clear path on
// a real on-disk database, confirming rows written to a plain table are gone
// afterward.
func TestClearTableRemovesRows(t *testing.T) {
	dir := t.TempDir()
	backupSeedSource(t, dir)

	db := mdbx2.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.TablesCfgByLabel(kv.ChainDB) }).
		MustOpen()
	defer db.Close()

	ctx := context.Background()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return ClearTable(ctx, db, tx, kv.HeaderNumber)
	}); err != nil {
		t.Fatalf("ClearTable: %v", err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(kv.HeaderNumber, []byte("hash-a"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Fatalf("expected HeaderNumber to be cleared, got %q", v)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify cleared table: %v", err)
	}
}

// TestClearTablesClearsMultiple exercises the ClearTables fan-out helper
// over two plain tables in one call.
func TestClearTablesClearsMultiple(t *testing.T) {
	dir := t.TempDir()
	backupSeedSource(t, dir)

	db := mdbx2.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.TablesCfgByLabel(kv.ChainDB) }).
		MustOpen()
	defer db.Close()

	ctx := context.Background()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(kv.BadHeaderNumber, []byte("hash-b"), []byte("2"))
	}); err != nil {
		t.Fatalf("seed second table: %v", err)
	}

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return ClearTables(ctx, db, tx, kv.HeaderNumber, kv.BadHeaderNumber)
	}); err != nil {
		t.Fatalf("ClearTables: %v", err)
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		for _, table := range []string{kv.HeaderNumber, kv.BadHeaderNumber} {
			v, err := tx.GetOne(table, []byte("hash-a"))
			if err != nil {
				return err
			}
			if v != nil {
				t.Fatalf("expected table %s to be cleared", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("verify cleared tables: %v", err)
	}
}
