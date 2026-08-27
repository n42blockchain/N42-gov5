// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package main

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestAPIBlockValueToBigIntPreserves256Bits(t *testing.T) {
	want := new(big.Int).Lsh(big.NewInt(1), 200)
	want.Add(want, big.NewInt(42))
	encoded := hexutil.Big(*want)

	got := apiBlockValueToBigInt(&encoded)
	if got.Cmp(want) != 0 {
		t.Fatalf("block value = %s, want %s", got, want)
	}
	if zero := apiBlockValueToBigInt(nil); zero.Sign() != 0 {
		t.Fatalf("nil block value = %s, want 0", zero)
	}
}

// staticChaindb is a chaindbProvider that always returns the same kv.RoDB.
// It lets the Backend be tested without standing up a full ethel.Node.
type staticChaindb struct{ db kv.RoDB }

func (s staticChaindb) DB() kv.RoDB { return s.db }

// TestEthELBackend_ReadOnlyMethods verifies the Backend's read-only methods
// round-trip correctly against an in-memory MDBX populated via rawdb. The
// test pins:
//
//   - the depshim/common.Hash → common/types.Hash conversion at the seam,
//   - the rawdb accessors used by ethELBackend,
//   - the chaindbProvider lazy-deref (a nil provider means "not ready").
func TestEthELBackend_ReadOnlyMethods(t *testing.T) {
	db := memdb.New("")
	defer db.Close()

	hashes := []types.Hash{
		types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
	}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	for i, h := range hashes {
		num := uint64(100 + i)
		if err := rawdb.WriteCanonicalHash(tx, h, num); err != nil {
			t.Fatalf("WriteCanonicalHash[%d]: %v", num, err)
		}
		if err := rawdb.WriteHeaderNumber(tx, h, num); err != nil {
			t.Fatalf("WriteHeaderNumber[%d]: %v", num, err)
		}
	}
	rawdb.WriteHeadBlockHash(tx, hashes[2])
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	backend := newEthELBackend(staticChaindb{db: db})
	ctx := context.Background()

	if ready, _ := backend.Ready(ctx); !ready {
		t.Fatalf("Ready: expected true with chaindata open")
	}

	got, err := backend.CurrentHeadNumber(ctx)
	if err != nil {
		t.Fatalf("CurrentHeadNumber: %v", err)
	}
	if got != 102 {
		t.Fatalf("CurrentHeadNumber: got %d, want 102", got)
	}

	for i, h := range hashes {
		dh := depcommon.Hash(h)
		if ok, err := backend.HasBlock(ctx, dh); err != nil || !ok {
			t.Fatalf("HasBlock[%d]: ok=%v err=%v", i, ok, err)
		}
		if ok, err := backend.IsCanonicalHash(ctx, dh); err != nil || !ok {
			t.Fatalf("IsCanonicalHash[%d]: ok=%v err=%v", i, ok, err)
		}
	}

	unknown := depcommon.Hash(types.HexToHash("0xdeadbeef"))
	if ok, err := backend.HasBlock(ctx, unknown); err != nil || ok {
		t.Fatalf("HasBlock[unknown]: ok=%v err=%v", ok, err)
	}
	if ok, err := backend.IsCanonicalHash(ctx, unknown); err != nil || ok {
		t.Fatalf("IsCanonicalHash[unknown]: ok=%v err=%v", ok, err)
	}

	// Nil provider should report not-ready and return zero values without
	// erroring.
	nilBackend := newEthELBackend(nil)
	if ready, _ := nilBackend.Ready(ctx); ready {
		t.Fatalf("nil provider Ready: expected false")
	}
	if n, err := nilBackend.CurrentHeadNumber(ctx); err != nil || n != 0 {
		t.Fatalf("nil provider CurrentHeadNumber: n=%d err=%v", n, err)
	}
}
