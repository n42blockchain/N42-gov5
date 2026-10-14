package main

import (
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

type failingAccountCursor struct {
	kv.Cursor
	err error
}

func (c failingAccountCursor) First() ([]byte, []byte, error) { return nil, nil, c.err }
func (c failingAccountCursor) Close()                         {}

type exportCursorTx struct {
	kv.Tx
	err error
}

func (tx exportCursorTx) Cursor(bucket string) (kv.Cursor, error) {
	if bucket == modules.Account {
		return failingAccountCursor{err: tx.err}, nil
	}
	return failingAccountCursor{}, nil
}

func TestAccountCursorFailureWithNoKeyIsNotEndOfExport(t *testing.T) {
	want := errors.New("cursor I/O failure")
	_, _, err := exportAccounts(exportCursorTx{err: want}, qmdb.New(), json.NewEncoder(io.Discard), 0, log.New(), time.Now())
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want cursor failure", err)
	}
}
