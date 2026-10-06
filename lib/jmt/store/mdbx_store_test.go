package store

import (
	"testing"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func newTestMDBXDB(t *testing.T) kv.RwDB {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	// JMTVersionRootsTable is deprecated and not part of the standard table
	// set; register it so tests can still exercise its (legacy) code paths.
	if _, ok := kv.ChaindataTablesCfg[JMTVersionRootsTable]; !ok {
		kv.ChaindataTablesCfg[JMTVersionRootsTable] = kv.TableCfgItem{}
	}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	return db
}

func TestMDBXStoreGetPutDeleteHas(t *testing.T) {
	db := newTestMDBXDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s := NewMDBXStore(tx, JMTNodeTable)

	h := jmt.Hash{0x01, 0x02}
	if _, err := s.Get(h); err != jmt.ErrNotFound {
		t.Fatalf("Get on missing node: err = %v, want ErrNotFound", err)
	}
	if has, err := s.Has(h); err != nil || has {
		t.Fatalf("Has on missing node = %v, %v; want false, nil", has, err)
	}

	data := []byte("node-payload")
	if err := s.Put(h, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get = %q, want %q", got, data)
	}
	if has, err := s.Has(h); err != nil || !has {
		t.Fatalf("Has = %v, %v; want true, nil", has, err)
	}

	if err := s.Delete(h); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(h); err != jmt.ErrNotFound {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestMDBXStorePutBatch(t *testing.T) {
	db := newTestMDBXDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s := NewMDBXStore(tx, JMTNodeTable)

	entries := map[jmt.Hash][]byte{
		{0x03}: []byte("c"),
		{0x01}: []byte("a"),
		{0x02}: []byte("b"),
	}
	if err := s.PutBatch(entries); err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	for h, want := range entries {
		got, err := s.Get(h)
		if err != nil {
			t.Fatalf("Get(%x): %v", h, err)
		}
		if string(got) != string(want) {
			t.Fatalf("Get(%x) = %q, want %q", h, got, want)
		}
	}
}

func TestMDBXStorePutBatchEmpty(t *testing.T) {
	db := newTestMDBXDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	s := NewMDBXStore(tx, JMTNodeTable)
	if err := s.PutBatch(nil); err != nil {
		t.Fatalf("PutBatch(nil): %v", err)
	}
}

func TestJMTRootAndVersionRoundTrip(t *testing.T) {
	db := newTestMDBXDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Defaults before anything written.
	root, err := ReadJMTRoot(tx)
	if err != nil {
		t.Fatalf("ReadJMTRoot: %v", err)
	}
	if root != jmt.EmptyHash {
		t.Fatalf("ReadJMTRoot default = %x, want EmptyHash", root)
	}
	version, err := ReadJMTVersion(tx)
	if err != nil {
		t.Fatalf("ReadJMTVersion: %v", err)
	}
	if version != 0 {
		t.Fatalf("ReadJMTVersion default = %d, want 0", version)
	}

	want := jmt.Hash{0xaa, 0xbb}
	if err := WriteJMTRoot(tx, want); err != nil {
		t.Fatalf("WriteJMTRoot: %v", err)
	}
	got, err := ReadJMTRoot(tx)
	if err != nil {
		t.Fatalf("ReadJMTRoot: %v", err)
	}
	if got != want {
		t.Fatalf("ReadJMTRoot = %x, want %x", got, want)
	}

	if err := WriteJMTVersion(tx, 42); err != nil {
		t.Fatalf("WriteJMTVersion: %v", err)
	}
	version, err = ReadJMTVersion(tx)
	if err != nil {
		t.Fatalf("ReadJMTVersion: %v", err)
	}
	if version != 42 {
		t.Fatalf("ReadJMTVersion = %d, want 42", version)
	}
}

func TestJMTVersionRootIndex(t *testing.T) {
	db := newTestMDBXDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// No entries yet.
	root, err := ReadJMTVersionRoot(tx, 100)
	if err != nil {
		t.Fatalf("ReadJMTVersionRoot: %v", err)
	}
	if root != jmt.EmptyHash {
		t.Fatalf("ReadJMTVersionRoot on empty table = %x, want EmptyHash", root)
	}

	root, actual, err := ReadJMTVersionRootAt(tx, 100)
	if err != nil {
		t.Fatalf("ReadJMTVersionRootAt: %v", err)
	}
	if root != jmt.EmptyHash || actual != 0 {
		t.Fatalf("ReadJMTVersionRootAt on empty table = %x,%d, want EmptyHash,0", root, actual)
	}

	h10 := jmt.Hash{0x10}
	h20 := jmt.Hash{0x20}
	h30 := jmt.Hash{0x30}
	if err := WriteJMTVersionRoot(tx, 10, h10); err != nil {
		t.Fatalf("WriteJMTVersionRoot(10): %v", err)
	}
	if err := WriteJMTVersionRoot(tx, 20, h20); err != nil {
		t.Fatalf("WriteJMTVersionRoot(20): %v", err)
	}
	if err := WriteJMTVersionRoot(tx, 30, h30); err != nil {
		t.Fatalf("WriteJMTVersionRoot(30): %v", err)
	}

	// Exact match.
	root, err = ReadJMTVersionRoot(tx, 20)
	if err != nil {
		t.Fatalf("ReadJMTVersionRoot(20): %v", err)
	}
	if root != h20 {
		t.Fatalf("ReadJMTVersionRoot(20) = %x, want %x", root, h20)
	}

	// Exact match via "At" too.
	root, actual, err = ReadJMTVersionRootAt(tx, 20)
	if err != nil {
		t.Fatalf("ReadJMTVersionRootAt(20): %v", err)
	}
	if root != h20 || actual != 20 {
		t.Fatalf("ReadJMTVersionRootAt(20) = %x,%d, want %x,20", root, actual, h20)
	}

	// Between heights — falls back to the previous entry.
	root, actual, err = ReadJMTVersionRootAt(tx, 25)
	if err != nil {
		t.Fatalf("ReadJMTVersionRootAt(25): %v", err)
	}
	if root != h20 || actual != 20 {
		t.Fatalf("ReadJMTVersionRootAt(25) = %x,%d, want %x,20", root, actual, h20)
	}

	// Past the last entry — falls back to the last (highest) entry.
	root, actual, err = ReadJMTVersionRootAt(tx, 1000)
	if err != nil {
		t.Fatalf("ReadJMTVersionRootAt(1000): %v", err)
	}
	if root != h30 || actual != 30 {
		t.Fatalf("ReadJMTVersionRootAt(1000) = %x,%d, want %x,30", root, actual, h30)
	}

	// Before the first entry.
	root, actual, err = ReadJMTVersionRootAt(tx, 5)
	if err != nil {
		t.Fatalf("ReadJMTVersionRootAt(5): %v", err)
	}
	if root != jmt.EmptyHash || actual != 0 {
		t.Fatalf("ReadJMTVersionRootAt(5) = %x,%d, want EmptyHash,0", root, actual)
	}

	// Missing exact-height lookup falls back to EmptyHash (not "at or before").
	root, err = ReadJMTVersionRoot(tx, 25)
	if err != nil {
		t.Fatalf("ReadJMTVersionRoot(25): %v", err)
	}
	if root != jmt.EmptyHash {
		t.Fatalf("ReadJMTVersionRoot(25) = %x, want EmptyHash (no exact entry)", root)
	}
}
