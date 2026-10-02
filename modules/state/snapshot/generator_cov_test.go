// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package snapshot

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestGenerator_CopiesAccountStorage exercises the storage-copy path
// (copyAccountStorage / keyHasPrefix) by seeding both an account and
// matching storage rows, then checking the SnapshotStorage table.
func TestGenerator_CopiesAccountStorage(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addr := types.HexToAddress("0x4444444444444444444444444444444444444444")
	acc := &account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(7)}
	pb := acc.ToProtoMessage()
	enc, err := proto.Marshal(pb)
	if err != nil {
		t.Fatal(err)
	}

	// Storage keys for this address: addr(20) + slot(32).
	slot1 := append(append([]byte{}, addr.Bytes()...), testHash(1).Bytes()...)
	slot2 := append(append([]byte{}, addr.Bytes()...), testHash(2).Bytes()...)
	// A storage row for a *different* address must not be copied.
	otherAddr := types.HexToAddress("0x5555555555555555555555555555555555555555")
	otherSlot := append(append([]byte{}, otherAddr.Bytes()...), testHash(1).Bytes()...)

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.Put(modules.Account, addr.Bytes(), enc); err != nil {
			return err
		}
		if err := tx.Put(modules.Storage, slot1, []byte("value-1")); err != nil {
			return err
		}
		if err := tx.Put(modules.Storage, slot2, []byte("value-2")); err != nil {
			return err
		}
		return tx.Put(modules.Storage, otherSlot, []byte("other-value"))
	}); err != nil {
		t.Fatal(err)
	}

	root := types.HexToHash("0xffff")
	gen := NewGenerator(db, root, 7)
	gen.Run(ctx)

	// The Done channel must be closed once Run returns.
	select {
	case <-gen.Done():
	default:
		t.Fatal("expected Done() channel to be closed after Run returns")
	}

	var v1, v2, vOther []byte
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		v1, err = rawdb.ReadSnapshotStorage(tx, slot1)
		if err != nil {
			return err
		}
		v2, err = rawdb.ReadSnapshotStorage(tx, slot2)
		if err != nil {
			return err
		}
		vOther, err = rawdb.ReadSnapshotStorage(tx, otherSlot)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if string(v1) != "value-1" {
		t.Errorf("slot1: got %q, want %q", v1, "value-1")
	}
	if string(v2) != "value-2" {
		t.Errorf("slot2: got %q, want %q", v2, "value-2")
	}
	if vOther != nil {
		t.Errorf("storage for an address with no Account row must not be copied, got %q", vOther)
	}
}

// TestGenerator_AlreadyComplete verifies Run short-circuits immediately
// when generation was already marked complete.
func TestGenerator_AlreadyComplete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.SetSnapshotGenComplete(tx)
	}); err != nil {
		t.Fatal(err)
	}

	gen := NewGenerator(db, testHash(1), 1)
	gen.Run(ctx)

	select {
	case <-gen.Done():
	default:
		t.Fatal("expected Done() channel to be closed")
	}
	if gen.Progress() != 0 {
		t.Errorf("expected no progress when already complete, got %d", gen.Progress())
	}
}

// TestGenerator_EmptyAccountTable exercises the k==nil-on-first-iteration
// completion branch (no accounts at all).
func TestGenerator_EmptyAccountTable(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	gen := NewGenerator(db, testHash(2), 2)
	gen.Run(ctx)

	var complete bool
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		complete, err = rawdb.IsSnapshotGenComplete(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected generation over an empty account table to complete")
	}
}
