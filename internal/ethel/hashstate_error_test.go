// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

type seekErrorCursor struct {
	kv.Cursor
	err error
}

func (c *seekErrorCursor) Seek([]byte) ([]byte, []byte, error) {
	return nil, nil, c.err
}

type seekErrorTx struct {
	kv.Tx
	err error
}

func (tx *seekErrorTx) Cursor(table string) (kv.Cursor, error) {
	c, err := tx.Tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	return &seekErrorCursor{Cursor: c, err: tx.err}, nil
}

type seekErrorDB struct {
	kv.RoDB
	err error
}

func (db *seekErrorDB) BeginRo(ctx context.Context) (kv.Tx, error) {
	tx, err := db.RoDB.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	return &seekErrorTx{Tx: tx, err: db.err}, nil
}

func TestScanPlainStateShardPropagatesInitialSeekError(t *testing.T) {
	db := memdb.NewTestDB(t)
	wantErr := errors.New("injected seek failure")
	wrapped := &seekErrorDB{RoDB: db, err: wantErr}

	err := scanPlainStateShard(t.Context(), wrapped, "Account", 0, 1, func(_, _ []byte) error {
		t.Fatal("visitor called after failed Seek")
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("scan error = %v, want %v", err, wantErr)
	}
}
