package modestate

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

func TestFullArchiveBackedByPlainState(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for _, m := range []rpccaps.Mode{rpccaps.Full, rpccaps.Archive} {
		r, err := StateReaderFor(m, tx, nil, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if _, ok := r.(state.StateReader); !ok {
			t.Fatalf("%s: not a state.StateReader", m)
		}
		// Empty DB → account absent (nil, nil), exercising the read path.
		acc, aerr := r.ReadAccountData(types.Address{0x01})
		if aerr != nil {
			t.Errorf("%s ReadAccountData: %v", m, aerr)
		}
		if acc != nil {
			t.Errorf("%s: expected absent account on empty db", m)
		}
	}
}

func TestFullNeedsTx(t *testing.T) {
	if _, err := StateReaderFor(rpccaps.Full, nil, nil, nil, 0); err == nil {
		t.Error("Full with nil tx should error")
	}
}

func TestM1NeedsSegment(t *testing.T) {
	if _, err := StateReaderFor(rpccaps.M1, nil, nil, nil, 0); err == nil {
		t.Error("M1 with nil segment should error")
	}
}

func TestM0NotStateBacked(t *testing.T) {
	_, err := StateReaderFor(rpccaps.M0, nil, nil, nil, 0)
	if !errors.Is(err, ErrNotStateBacked) {
		t.Errorf("M0 should return ErrNotStateBacked, got %v", err)
	}
}
