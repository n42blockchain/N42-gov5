package enode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
)

func TestOpenDB_InMemory(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB in-memory failed: %v", err)
	}
	defer db.Close()

	if db.kv == nil {
		t.Fatal("expected non-nil kv database")
	}
}

func TestOpenDB_Persistent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	db, err := OpenDB(context.Background(), dbPath, "")
	if err != nil {
		t.Fatalf("OpenDB persistent failed: %v", err)
	}
	defer db.Close()

	if db.kv == nil {
		t.Fatal("expected non-nil kv database")
	}

	// verify database directory was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected database directory to exist")
	}
}

func TestOpenDB_PersistentReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "testdb")

	// first open
	db1, err := OpenDB(context.Background(), dbPath, "")
	if err != nil {
		t.Fatalf("first OpenDB failed: %v", err)
	}
	db1.Close()

	// reopen same path
	db2, err := OpenDB(context.Background(), dbPath, "")
	if err != nil {
		t.Fatalf("second OpenDB failed: %v", err)
	}
	defer db2.Close()

	if db2.kv == nil {
		t.Fatal("expected non-nil kv after reopen")
	}
}

func TestOpenDB_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	tmpDir := t.TempDir()
	// in-memory mode doesn't use ctx during Open for polling,
	// so it should still succeed
	db, err := OpenDB(ctx, "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB with cancelled context (in-memory) failed: %v", err)
	}
	defer db.Close()
}

func TestDB_Close_Idempotent(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}

	// close multiple times should not panic
	db.Close()
	db.Close()
	db.Close()
}

func TestDB_ExpirerStopsOnClose(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}

	// start the expirer goroutine
	db.ensureExpirer()

	// close should signal quit channel
	db.Close()

	// verify quit channel is closed
	select {
	case <-db.quit:
		// expected
	case <-time.After(time.Second):
		t.Fatal("quit channel not closed after Close()")
	}
}

func TestDBNodeSkipsCorruptRecord(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	var id ID
	id[0] = 0x42
	if err := db.kv.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(kv.NodeRecords, nodeKey(id), []byte{0xff, 0x00, 0x01})
	}); err != nil {
		t.Fatalf("failed to write corrupt node record: %v", err)
	}

	if got := db.Node(id); got != nil {
		t.Fatalf("Node returned %v, want nil for corrupt record", got)
	}
}

func TestDBQuerySeedsSkipsIncompleteNode(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	incomplete := NewV4(&key.PublicKey, nil, 0, 0)
	for i := range incomplete.id {
		incomplete.id[i] = 0xff
	}
	if err := db.UpdateNode(incomplete); err != nil {
		t.Fatalf("UpdateNode failed: %v", err)
	}

	seeds := db.QuerySeeds(1, time.Hour)
	if len(seeds) != 0 {
		t.Fatalf("QuerySeeds returned %d nodes, want 0", len(seeds))
	}
}

func TestDBQuerySeedsSkipsMalformedRecordKeys(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	if err := db.kv.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(kv.NodeRecords, []byte("n:broken"), []byte{0x01})
	}); err != nil {
		t.Fatalf("failed to write malformed node key: %v", err)
	}

	if seeds := db.QuerySeeds(1, time.Hour); len(seeds) != 0 {
		t.Fatalf("QuerySeeds returned %d nodes, want 0", len(seeds))
	}
}

func TestDBExpireNodesSkipsMalformedMetadataKeys(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenDB(context.Background(), "", tmpDir)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	defer db.Close()

	if err := db.kv.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(kv.Inodes, []byte("n:broken"), []byte{0x01})
	}); err != nil {
		t.Fatalf("failed to write malformed inode key: %v", err)
	}

	db.expireNodes()
}
