package mpttrie

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

const denseTestTable = "AccountsDense"

// writeDenseTestDB creates a temp-dir MDBX env with a single dense
// table populated with one or more entries, and returns the dir.
func writeDenseTestDB(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	return writeDenseTestDBNamed(t, denseTestTable, entries)
}

// writeDenseTestDBNamed creates a temp-dir MDBX env with a single dense
// table (named `table`) populated with the given entries, and returns
// the dir.
func writeDenseTestDBNamed(t *testing.T, table string, entries map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[table] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open write env: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	for k, v := range entries {
		if err := tx.Put(table, []byte(k), v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return dir
}

// makeDenseV1Entry builds one MarshalTrieNodeDense-encoded value with
// a single child at `digit` holding a 32-byte hash.
func makeDenseV1Entry(digit int, hash [32]byte) []byte {
	const stride = 33
	stateMask := uint16(1 << digit)
	treeMask := uint16(0)
	slotData := make([]byte, stride)
	slotData[0] = 0xa0
	copy(slotData[1:], hash[:])
	return trie.MarshalTrieNodeDense(stateMask, treeMask, slotData, nil)
}

func TestOpenDense_HasGetClose(t *testing.T) {
	hash := [32]byte{1, 2, 3, 4}
	val := makeDenseV1Entry(5, hash)
	dir := writeDenseTestDB(t, map[string][]byte{
		"": val, // root path
	})

	r, err := OpenDense(dir, denseTestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}

	has, err := r.Has()
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !has {
		t.Fatal("expected Has() to report true")
	}

	branch, ok, err := r.Get(nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("expected Get to find the entry at root path")
	}
	if branch.StateMask != 1<<5 {
		t.Fatalf("StateMask = %x, want %x", branch.StateMask, 1<<5)
	}
	if branch.Slots[5] == nil || branch.Slots[5][0] != 0xa0 {
		t.Fatalf("unexpected slot 5: %v", branch.Slots[5])
	}

	// Miss case.
	_, ok, err = r.Get([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("Get(miss): %v", err)
	}
	if ok {
		t.Fatal("expected miss for unknown path")
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenDense_HasEmptyTable(t *testing.T) {
	dir := writeDenseTestDB(t, nil)
	r, err := OpenDense(dir, denseTestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}
	defer r.Close()

	has, err := r.Has()
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if has {
		t.Fatal("expected Has() to report false for an empty table")
	}
}

func TestOpenDense_HasPhantomKeyRejected(t *testing.T) {
	// A key whose first byte is > 0x0f is not a valid nibble path and
	// should be treated as a phantom Meta key, per Has()'s defensive
	// check.
	dir := writeDenseTestDB(t, map[string][]byte{
		"accounts:state_root": {0xaa},
	})
	r, err := OpenDense(dir, denseTestTable)
	if err != nil {
		t.Fatalf("OpenDense: %v", err)
	}
	defer r.Close()

	has, err := r.Has()
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if has {
		t.Fatal("expected phantom key to be rejected by Has()")
	}
}

func TestOpenDenseShared(t *testing.T) {
	hash := [32]byte{9, 9, 9}
	val := makeDenseV1Entry(0, hash)
	dir := writeDenseTestDB(t, map[string][]byte{"": val})

	logger := log.New()
	env, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[denseTestTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open shared env: %v", err)
	}
	defer env.Close()

	r := OpenDenseShared(env, denseTestTable)
	_, ok, err := r.Get(nil)
	if err != nil || !ok {
		t.Fatalf("Get via shared reader failed: ok=%v err=%v", ok, err)
	}

	// Close on a shared reader is a no-op; the env must remain usable.
	if err := r.Close(); err != nil {
		t.Fatalf("Close (shared, should be no-op): %v", err)
	}
	if _, _, err := r.Get(nil); err != nil {
		t.Fatalf("env still usable after shared Close: %v", err)
	}
}

func TestOpenDense_MissingDir(t *testing.T) {
	if _, err := OpenDense(t.TempDir()+"/nope", denseTestTable); err == nil {
		t.Fatal("expected error opening nonexistent dense dir")
	}
}
