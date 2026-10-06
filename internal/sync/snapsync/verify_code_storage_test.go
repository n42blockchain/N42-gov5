package snapsync

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func TestVerifyCodeIntegrity_EmptyTable(t *testing.T) {
	db := testDB(t)
	if err := VerifyCodeIntegrity(context.Background(), db); err != nil {
		t.Fatalf("expected no error for empty table, got %v", err)
	}
}

func TestVerifyCodeIntegrity_NoCodeAccount(t *testing.T) {
	db := testDB(t)
	acc := account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(0)}
	buf := make([]byte, 128)
	acc.EncodeForStorage(buf)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		key := bytes.Repeat([]byte{0x01}, 20)
		return tx.Put(modules.Account, key, buf)
	}); err != nil {
		t.Fatal(err)
	}

	if err := VerifyCodeIntegrity(context.Background(), db); err != nil {
		t.Fatalf("expected no error for account with empty code hash, got %v", err)
	}
}

func TestVerifyCodeIntegrity_MissingCode(t *testing.T) {
	db := testDB(t)
	codeHash := types.Hash{0xAB, 0xCD}
	acc := account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(0), CodeHash: codeHash}
	buf := make([]byte, 128)
	acc.EncodeForStorage(buf)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		key := bytes.Repeat([]byte{0x02}, 20)
		return tx.Put(modules.Account, key, buf)
	}); err != nil {
		t.Fatal(err)
	}

	err := VerifyCodeIntegrity(context.Background(), db)
	if !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted for missing code, got %v", err)
	}
}

func TestVerifyCodeIntegrity_PresentCode(t *testing.T) {
	db := testDB(t)
	codeHash := types.Hash{0xAB, 0xCD}
	acc := account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(0), CodeHash: codeHash}
	buf := make([]byte, 128)
	acc.EncodeForStorage(buf)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		key := bytes.Repeat([]byte{0x03}, 20)
		if err := tx.Put(modules.Account, key, buf); err != nil {
			return err
		}
		return tx.Put(modules.Code, codeHash[:], []byte{0x60, 0x00})
	}); err != nil {
		t.Fatal(err)
	}

	if err := VerifyCodeIntegrity(context.Background(), db); err != nil {
		t.Fatalf("expected no error when code is present, got %v", err)
	}
}

func TestVerifyStorageIntegrity_EmptyTable(t *testing.T) {
	db := testDB(t)
	if err := VerifyStorageIntegrity(context.Background(), db, 10); err != nil {
		t.Fatalf("expected no error for empty table, got %v", err)
	}
}

func TestVerifyStorageIntegrity_InvalidKeyLength(t *testing.T) {
	db := testDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.Storage, bytes.Repeat([]byte{0x01}, 10), []byte{0x01})
	}); err != nil {
		t.Fatal(err)
	}

	err := VerifyStorageIntegrity(context.Background(), db, 10)
	if !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted for bad key length, got %v", err)
	}
}

func TestVerifyStorageIntegrity_OrphanedStorage(t *testing.T) {
	db := testDB(t)
	addr := bytes.Repeat([]byte{0x02}, 20)
	slot := bytes.Repeat([]byte{0x03}, 32)
	key := append(append([]byte{}, addr...), slot...)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.Storage, key, []byte{0x01})
	}); err != nil {
		t.Fatal(err)
	}

	err := VerifyStorageIntegrity(context.Background(), db, 10)
	if !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted for orphaned storage, got %v", err)
	}
}

func TestVerifyStorageIntegrity_Valid(t *testing.T) {
	db := testDB(t)
	addr := bytes.Repeat([]byte{0x04}, 20)
	slot := bytes.Repeat([]byte{0x05}, 32)
	key := append(append([]byte{}, addr...), slot...)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := tx.Put(modules.Account, addr, []byte{0x01}); err != nil {
			return err
		}
		return tx.Put(modules.Storage, key, []byte{0x01})
	}); err != nil {
		t.Fatal(err)
	}

	if err := VerifyStorageIntegrity(context.Background(), db, 10); err != nil {
		t.Fatalf("expected no error for valid storage entry, got %v", err)
	}
}
