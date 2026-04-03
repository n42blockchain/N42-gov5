package ethel

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func TestProgressReadWrite(t *testing.T) {
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Initial: no progress.
	if p := ReadProgress(tx); p != 0 {
		t.Errorf("initial progress: got %d, want 0", p)
	}

	// Write and read back.
	if err := WriteProgress(tx, 12345); err != nil {
		t.Fatal(err)
	}
	if p := ReadProgress(tx); p != 12345 {
		t.Errorf("after write: got %d, want 12345", p)
	}

	// Overwrite.
	if err := WriteProgress(tx, 99999); err != nil {
		t.Fatal(err)
	}
	if p := ReadProgress(tx); p != 99999 {
		t.Errorf("after overwrite: got %d, want 99999", p)
	}

	tx.Rollback()
}
