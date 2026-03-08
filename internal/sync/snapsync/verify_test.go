package snapsync

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func TestVerifyDownloadedState_Empty(t *testing.T) {
	db := testDB(t)

	_, err := VerifyDownloadedState(context.Background(), db)
	if !errors.Is(err, ErrEmptyState) {
		t.Fatalf("expected ErrEmptyState, got %v", err)
	}
}

func TestVerifyDownloadedState_WithAccounts(t *testing.T) {
	db := testDB(t)

	// Insert some accounts.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 5; i++ {
			key := bytes.Repeat([]byte{byte(i + 1)}, 20)
			val := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, byte(i)} // fieldset + nonce
			if err := tx.Put(modules.Account, key, val); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := VerifyDownloadedState(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AccountCount != 5 {
		t.Errorf("expected 5 accounts, got %d", stats.AccountCount)
	}
	if stats.StorageCount != 0 {
		t.Errorf("expected 0 storage, got %d", stats.StorageCount)
	}
	if stats.CodeCount != 0 {
		t.Errorf("expected 0 code, got %d", stats.CodeCount)
	}
}

func TestVerifyDownloadedState_WithAllTables(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		// 3 accounts.
		for i := 0; i < 3; i++ {
			key := bytes.Repeat([]byte{byte(i + 1)}, 20)
			if err := tx.Put(modules.Account, key, []byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
				return err
			}
		}
		// 2 code entries.
		for i := 0; i < 2; i++ {
			key := bytes.Repeat([]byte{byte(i + 0x10)}, 32)
			if err := tx.Put(modules.Code, key, []byte{0x60, 0x00}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := VerifyDownloadedState(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AccountCount != 3 {
		t.Errorf("expected 3 accounts, got %d", stats.AccountCount)
	}
	if stats.CodeCount != 2 {
		t.Errorf("expected 2 code entries, got %d", stats.CodeCount)
	}
}

func TestVerifyAccountIntegrity_Valid(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for i := 0; i < 10; i++ {
			key := bytes.Repeat([]byte{byte(i + 1)}, 20)
			val := []byte{0x01, 0, 0, 0, 0, 0, 0, 0, byte(i)}
			if err := tx.Put(modules.Account, key, val); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := VerifyAccountIntegrity(context.Background(), db, 10); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestVerifyAccountIntegrity_InvalidKeyLength(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		// Insert account with wrong key length (16 bytes instead of 20).
		key := bytes.Repeat([]byte{0xAA}, 16)
		return tx.Put(modules.Account, key, []byte{0x01})
	}); err != nil {
		t.Fatal(err)
	}

	err := VerifyAccountIntegrity(context.Background(), db, 10)
	if !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted, got %v", err)
	}
}

func TestVerifyAccountIntegrity_EmptyValue(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		key := bytes.Repeat([]byte{0xBB}, 20)
		return tx.Put(modules.Account, key, []byte{})
	}); err != nil {
		t.Fatal(err)
	}

	err := VerifyAccountIntegrity(context.Background(), db, 10)
	if !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted, got %v", err)
	}
}

func TestVerifyAccountIntegrity_DefaultSampleSize(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		key := bytes.Repeat([]byte{0x01}, 20)
		return tx.Put(modules.Account, key, []byte{0x01})
	}); err != nil {
		t.Fatal(err)
	}

	// Pass 0 to trigger default sample size (100).
	if err := VerifyAccountIntegrity(context.Background(), db, 0); err != nil {
		t.Fatalf("expected no error with default sample size, got %v", err)
	}
}

func TestVerifyAccountIntegrity_EmptyTable(t *testing.T) {
	db := testDB(t)

	// No accounts inserted - should pass (nothing to check).
	if err := VerifyAccountIntegrity(context.Background(), db, 10); err != nil {
		t.Fatalf("expected no error for empty table, got %v", err)
	}
}
