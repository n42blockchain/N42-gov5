package snapsync

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func testDB(t *testing.T) kv.RwDB {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return memdb.NewTestDB(t)
}

func TestSavePivotBlock(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePivotBlock(tx, 12345)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded uint64
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadPivotBlock(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if loaded != 12345 {
		t.Errorf("expected pivot block 12345, got %d", loaded)
	}
}

func TestSaveAccountCursor(t *testing.T) {
	db := testDB(t)
	cursor := bytes.Repeat([]byte{0xAB}, 20)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveAccountCursor(tx, cursor)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded []byte
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadAccountCursor(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(loaded, cursor) {
		t.Errorf("cursor mismatch: got %x, want %x", loaded, cursor)
	}
}

func TestSaveState(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveState(tx, StateRunning)
	}); err != nil {
		t.Fatal(err)
	}

	var state string
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		state, err = LoadState(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if state != StateRunning {
		t.Errorf("expected state %q, got %q", StateRunning, state)
	}
}

func TestClearProgress(t *testing.T) {
	db := testDB(t)

	// Save some progress.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := SavePivotBlock(tx, 100); err != nil {
			return err
		}
		if err := SaveAccountCursor(tx, []byte{1, 2, 3}); err != nil {
			return err
		}
		return SaveState(tx, StateRunning)
	}); err != nil {
		t.Fatal(err)
	}

	// Clear all progress.
	if err := ClearProgress(db); err != nil {
		t.Fatal(err)
	}

	// Verify cleared.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		pivot, err := LoadPivotBlock(tx)
		if err != nil {
			return err
		}
		if pivot != 0 {
			t.Errorf("expected pivot 0 after clear, got %d", pivot)
		}

		cursor, err := LoadAccountCursor(tx)
		if err != nil {
			return err
		}
		if cursor != nil {
			t.Errorf("expected nil cursor after clear, got %x", cursor)
		}

		state, err := LoadState(tx)
		if err != nil {
			return err
		}
		if state != "" {
			t.Errorf("expected empty state after clear, got %q", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParseAccountForTasks(t *testing.T) {
	// Empty account.
	inc, code := parseAccountForTasks(nil)
	if inc != 0 || code != nil {
		t.Errorf("empty: inc=%d, code=%v", inc, code)
	}

	// Account with only nonce (fieldset=0x01, 8 bytes nonce).
	data := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, 1}
	inc, code = parseAccountForTasks(data)
	if inc != 0 || code != nil {
		t.Errorf("nonce-only: inc=%d, code=%v", inc, code)
	}
}
