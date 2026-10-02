// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashstate_cursor_errors_test.go covers the tx.Cursor(table)-open error
// branches in hashAllAccounts, hashAllStorage, IsHPHBootstrapped, and
// InitHashState via a thin wrapper that fails Cursor() for one named
// table — same safe technique as clearBucketErrorTx (the real Cursor call
// never happens for that table, so no MDBX cursor is opened or corrupted).

package ethel

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

type cursorErrorTx struct {
	kv.RwTx
	failTable string
	err       error
}

func (tx *cursorErrorTx) Cursor(table string) (kv.Cursor, error) {
	if table == tx.failTable {
		return nil, tx.err
	}
	return tx.RwTx.Cursor(table)
}

var errCursorOpen = errors.New("injected cursor-open failure")

func TestHashAllAccounts_CursorError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &cursorErrorTx{RwTx: tx, failTable: "Account", err: errCursorOpen}

	if err := RebuildHashedState(wrapped); !errors.Is(err, errCursorOpen) {
		t.Fatalf("RebuildHashedState (hashAllAccounts cursor) error = %v, want %v", err, errCursorOpen)
	}
}

func TestHashAllStorage_CursorError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &cursorErrorTx{RwTx: tx, failTable: "Storage", err: errCursorOpen}

	if err := RebuildHashedState(wrapped); !errors.Is(err, errCursorOpen) {
		t.Fatalf("RebuildHashedState (hashAllStorage cursor) error = %v, want %v", err, errCursorOpen)
	}
}

func TestIsHPHBootstrapped_CursorError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &cursorErrorTx{RwTx: tx, failTable: "TrieAccount", err: errCursorOpen}

	_, err = IsHPHBootstrapped(wrapped)
	if !errors.Is(err, errCursorOpen) {
		t.Fatalf("IsHPHBootstrapped error = %v, want %v", err, errCursorOpen)
	}
}

func TestInitHashState_CursorError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &cursorErrorTx{RwTx: tx, failTable: kv.HashedAccounts, err: errCursorOpen}

	err = InitHashState(wrapped)
	if !errors.Is(err, errCursorOpen) {
		t.Fatalf("InitHashState error = %v, want %v", err, errCursorOpen)
	}
}

func TestInitHashState_AccountCursorError(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	wrapped := &cursorErrorTx{RwTx: tx, failTable: "Account", err: errCursorOpen}

	err = InitHashState(wrapped)
	if !errors.Is(err, errCursorOpen) {
		t.Fatalf("InitHashState (account cursor) error = %v, want %v", err, errCursorOpen)
	}
}
