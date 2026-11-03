//go:build linux

package mdbx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	mdbxlib "github.com/erigontech/mdbx-go/mdbx"
	"golang.org/x/sys/unix"

	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

const writebackTestTable = "EarlyWriteback"

func openWritebackTestDB(t *testing.T, path string, mapped bool) *MdbxKV {
	t.Helper()
	opts := NewMDBX(log.New()).Path(path).Label(kv.ChainDB).
		MapSize(64 * datasize.MB).GrowthStep(4 * datasize.MB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg {
			return kv.TableCfg{writebackTestTable: {}}
		})
	if mapped {
		opts = opts.WriteMap()
	}
	db := opts.MustOpen().(*MdbxKV)
	t.Cleanup(db.Close)
	return db
}

func seedWritebackTestDB(t *testing.T, db *MdbxKV) {
	t.Helper()
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := tx.Put(writebackTestTable, []byte("stable"), []byte("before")); err != nil {
			return err
		}
		value := bytes.Repeat([]byte{0x2a}, 512)
		for i := uint64(0); i < 8192; i++ {
			var key [8]byte
			binary.BigEndian.PutUint64(key[:], i)
			if err := tx.Put(writebackTestTable, key[:], value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkWritebackValue(t *testing.T, tx kv.Getter, key, expected string) {
	t.Helper()
	value, err := tx.GetOne(writebackTestTable, []byte(key))
	if err != nil || string(value) != expected {
		t.Fatalf("%s = %q, err=%v; want %q", key, value, err, expected)
	}
}

func TestEarlyWritebackSnapshotRollbackAndCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db := openWritebackTestDB(t, path, true)
	seedWritebackTestDB(t, db)
	snapshot, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()

	for _, commit := range []bool{false, true} {
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := tx.Put(writebackTestTable, []byte("stable"), []byte("after")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(writebackTestTable, []byte("new"), bytes.Repeat([]byte{0x5a}, 4<<20)); err != nil {
			t.Fatal(err)
		}
		requested, err := tx.(*MdbxTx).StartWriteback()
		if err != nil || !requested {
			t.Fatalf("writeback requested=%v err=%v", requested, err)
		}
		checkWritebackValue(t, snapshot, "stable", "before")
		checkWritebackValue(t, snapshot, "new", "")
		if commit {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		} else {
			tx.Rollback()
		}
		checkWritebackValue(t, snapshot, "stable", "before")
		want := "before"
		if commit {
			want = "after"
		}
		if err := db.View(context.Background(), func(ro kv.Tx) error {
			checkWritebackValue(t, ro, "stable", want)
			if !commit {
				checkWritebackValue(t, ro, "new", "")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot.Rollback()
	db.Close()
	db = openWritebackTestDB(t, path, true)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		checkWritebackValue(t, tx, "stable", "after")
		value, err := tx.GetOne(writebackTestTable, []byte("new"))
		if err != nil || len(value) != 4<<20 || bytes.Count(value, []byte{0x5a}) != len(value) {
			t.Fatalf("committed overflow value changed after reopen: bytes=%d err=%v", len(value), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyWritebackErrorsAbortTransaction(t *testing.T) {
	db := openWritebackTestDB(t, filepath.Join(t.TempDir(), "db"), true)
	seedWritebackTestDB(t, db)
	for _, failure := range []error{unix.EIO, unix.ENOSPC, unix.EINTR} {
		err := db.Update(context.Background(), func(tx kv.RwTx) error {
			if err := tx.Put(writebackTestTable, []byte("stable"), []byte("failed")); err != nil {
				return err
			}
			requested, err := tx.(*MdbxTx).startWriteback(func(fd int, off, size int64, flags int) error {
				if off != 0 || size != 0 || flags != unix.SYNC_FILE_RANGE_WRITE {
					t.Fatalf("unexpected range or flags: %d %d %d", off, size, flags)
				}
				return failure
			})
			if requested {
				t.Fatal("failed request reported success")
			}
			return err
		})
		if !errors.Is(err, failure) {
			t.Fatalf("error %v does not preserve %v", err, failure)
		}
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			checkWritebackValue(t, tx, "stable", "before")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, unsupported := range []error{unix.ENOSYS, unix.EOPNOTSUPP} {
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			requested, err := tx.(*MdbxTx).startWriteback(func(int, int64, int64, int) error { return unsupported })
			if requested || err != nil {
				t.Fatalf("unsupported writeback: requested=%v err=%v", requested, err)
			}
			return tx.Put(writebackTestTable, []byte("stable"), []byte("fallback"))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		checkWritebackValue(t, tx, "stable", "fallback")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEarlyWritebackRequiresWritableMappedTransaction(t *testing.T) {
	if dbg.WriteMap() {
		t.Skip("global WRITE_MAP overrides the unmapped configuration")
	}
	db := openWritebackTestDB(t, filepath.Join(t.TempDir(), "db"), false)
	if db.opts.HasFlag(mdbxlib.WriteMap) {
		t.Fatal("test database unexpectedly mapped writable")
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	requested, err := tx.(*MdbxTx).startWriteback(func(int, int64, int64, int) error {
		t.Fatal("unmapped transaction invoked writeback")
		return nil
	})
	if requested || err != nil {
		t.Fatalf("unmapped writeback: requested=%v err=%v", requested, err)
	}
	tx.Rollback()
	if _, err := tx.(*MdbxTx).StartWriteback(); err == nil {
		t.Fatal("closed transaction accepted")
	}
	ro, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Rollback()
	if _, err := ro.(*MdbxTx).StartWriteback(); err == nil {
		t.Fatal("read-only transaction accepted")
	}
}

func TestEarlyWritebackCrashBeforeCommit(t *testing.T) {
	if path := os.Getenv("N42_TEST_EARLY_WRITEBACK_CHILD"); path != "" {
		db := openWritebackTestDB(t, path, true)
		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := tx.Put(writebackTestTable, []byte("stable"), []byte("uncommitted")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(writebackTestTable, []byte("new"), bytes.Repeat([]byte{0x7f}, 8<<20)); err != nil {
			t.Fatal(err)
		}
		if requested, err := tx.(*MdbxTx).StartWriteback(); err != nil || !requested {
			t.Fatalf("child writeback requested=%v err=%v", requested, err)
		}
		fd, err := db.env.FD()
		if err != nil {
			t.Fatal(err)
		}
		// Make the uncommitted data pages reach storage, then exit without any
		// deferred Rollback/Close. This is process-loss recovery, not a power cut.
		if err := unix.Fsync(int(fd)); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	}
	path := filepath.Join(t.TempDir(), "db")
	db := openWritebackTestDB(t, path, true)
	seedWritebackTestDB(t, db)
	db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEarlyWritebackCrashBeforeCommit$", "-test.count=1", "-test.timeout=20s")
	child.Env = append(os.Environ(), "N42_TEST_EARLY_WRITEBACK_CHILD="+path)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child did not reach abrupt exit: %v\n%s", err, output)
	}
	db = openWritebackTestDB(t, path, true)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		checkWritebackValue(t, tx, "stable", "before")
		checkWritebackValue(t, tx, "new", "")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
