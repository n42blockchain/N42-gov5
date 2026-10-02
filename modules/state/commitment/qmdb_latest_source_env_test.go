// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers two small qmdb_latest_source.go entry points left at 0% by
// qmdb_latest_source_test.go: QMDBOnlyAccountWrites' env-var truthy set, and
// NewLookupSourceLocked's locked Get path (LookupLocked instead of Lookup).

package commitment

import (
	"context"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

func TestQMDBOnlyAccountWrites(t *testing.T) {
	const envKey = "N42_STATE_WRITE_QMDB_ONLY"
	orig, had := os.LookupEnv(envKey)
	t.Cleanup(func() {
		if had {
			os.Setenv(envKey, orig)
		} else {
			os.Unsetenv(envKey)
		}
	})

	cases := map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"1":     true,
		"true":  true,
		"TRUE":  true,
		"yes":   true,
		"on":    true,
	}
	for val, want := range cases {
		if val == "" {
			os.Unsetenv(envKey)
		} else {
			os.Setenv(envKey, val)
		}
		if got := QMDBOnlyAccountWrites(); got != want {
			t.Errorf("QMDBOnlyAccountWrites() with %s=%q = %v, want %v", envKey, val, got, want)
		}
	}
}

func TestLookupSourceLocked(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	addr := qmAddr(1)
	applyAndPersist(t, rc, db, map[types.Address]*account.StateAccount{addr: qmAcct(1, 100)})

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	unlock := rc.LockReaders()
	src := NewLookupSourceLocked(rc, tx)
	kh := qmdb.Hash(AccountKeyHash(addr))
	v, ok := src.Get(kh)
	unlock()
	if !ok {
		t.Fatal("expected the locked lookup source to find the account")
	}
	if len(v) == 0 {
		t.Fatal("expected a non-empty encoded account")
	}

	// A key that was never written must report !ok.
	unlock = rc.LockReaders()
	_, ok = NewLookupSourceLocked(rc, tx).Get(qmdb.Hash(AccountKeyHash(qmAddr(99))))
	unlock()
	if ok {
		t.Fatal("expected !ok for an unwritten key")
	}
}
