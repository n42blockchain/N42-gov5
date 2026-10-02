package mpttrie

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func TestReader_BuiltAt(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 10)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	builtAt, err := r.BuiltAt()
	if err != nil {
		t.Fatalf("BuiltAt: %v", err)
	}
	if builtAt.IsZero() {
		t.Fatal("expected non-zero BuiltAt timestamp")
	}
	if time.Since(builtAt) > time.Hour || time.Since(builtAt) < -time.Hour {
		t.Fatalf("BuiltAt timestamp looks wrong: %v", builtAt)
	}
}

func TestOpenUnifiedDB(t *testing.T) {
	// Build a unified MDBX env containing both AccountsTrie and
	// StoragesTrie buckets plus prefixed Meta keys, mirroring the
	// layout OpenUnifiedDB expects.
	tmp := t.TempDir()
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(tmp).
		Label(kv.ChainDB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open write env: %v", err)
	}
	accRoot := [32]byte{1, 2, 3}
	storRoot := [32]byte{4, 5, 6}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	if err := tx.Put("Meta", []byte("accounts:state_root"), accRoot[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Put("Meta", []byte("storage:state_root"), storRoot[:]); err != nil {
		t.Fatal(err)
	}
	if err := tx.Put("AccountsTrie", []byte{0x1}, []byte{0xaa}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	env, accounts, storage, err := OpenUnifiedDB(tmp)
	if err != nil {
		t.Fatalf("OpenUnifiedDB: %v", err)
	}
	defer env.Close()

	gotAcc, err := accounts.StateRoot()
	if err != nil {
		t.Fatalf("accounts.StateRoot: %v", err)
	}
	if gotAcc != accRoot {
		t.Fatalf("accounts root = %x, want %x", gotAcc, accRoot)
	}
	gotStor, err := storage.StateRoot()
	if err != nil {
		t.Fatalf("storage.StateRoot: %v", err)
	}
	if gotStor != storRoot {
		t.Fatalf("storage root = %x, want %x", gotStor, storRoot)
	}

	// Close() on a shared (owned=false) reader must be a no-op; the env
	// stays usable afterwards.
	if err := accounts.Close(); err != nil {
		t.Fatalf("accounts.Close (shared, should be no-op): %v", err)
	}
	if _, err := storage.StateRoot(); err != nil {
		t.Fatalf("env still usable after shared Close: %v", err)
	}
}

func TestOpenUnifiedDB_MissingDir(t *testing.T) {
	_, _, _, err := OpenUnifiedDB(t.TempDir() + "/does-not-exist")
	if err == nil {
		t.Fatal("expected error opening nonexistent unified dir")
	}
}
