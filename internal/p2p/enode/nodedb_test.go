package enode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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
