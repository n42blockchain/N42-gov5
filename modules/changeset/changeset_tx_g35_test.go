// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the kv.Tx-backed walking/truncation helpers (ForRange, ForEach,
// ForPrefix, AvailableFrom, Truncate) using the in-memory test DB.

package changeset

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestG35AvailableFromEmpty(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	from, err := AvailableFrom(tx)
	if err != nil {
		t.Fatalf("AvailableFrom: %v", err)
	}
	if from == 0 {
		t.Fatalf("expected sentinel max-uint64 for empty bucket, got %d", from)
	}

	sfrom, err := AvailableStorageFrom(tx)
	if err != nil {
		t.Fatalf("AvailableStorageFrom: %v", err)
	}
	if sfrom == 0 {
		t.Fatalf("expected sentinel max-uint64 for empty storage bucket, got %d", sfrom)
	}
}

func writeAccountChange(t *testing.T, tx interface {
	Put(table string, k, v []byte) error
}, blockN uint64, addr types.Address, val []byte) {
	t.Helper()
	s := NewAccountChangeSet()
	if err := s.Add(addr.Bytes(), val); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := EncodeAccounts(blockN, s, func(k, v []byte) error {
		return tx.Put(modules.AccountChangeSet, k, v)
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
}

func TestG35ForRangeForEachForPrefix(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr1 := types.Address{1}
	addr2 := types.Address{2}
	writeAccountChange(t, tx, 1, addr1, []byte("v1"))
	writeAccountChange(t, tx, 2, addr2, []byte("v2"))
	writeAccountChange(t, tx, 5, addr1, []byte("v5"))

	// AvailableFrom should now report the earliest block.
	from, err := AvailableFrom(tx)
	if err != nil {
		t.Fatalf("AvailableFrom: %v", err)
	}
	if from != 1 {
		t.Fatalf("expected earliest block 1, got %d", from)
	}

	var seen []uint64
	if err := ForRange(tx, modules.AccountChangeSet, 0, 5, func(blockN uint64, k, v []byte) error {
		seen = append(seen, blockN)
		return nil
	}); err != nil {
		t.Fatalf("ForRange: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 entries in [0,5), got %d: %v", len(seen), seen)
	}

	var all []uint64
	if err := ForEach(tx, modules.AccountChangeSet, nil, func(blockN uint64, k, v []byte) error {
		all = append(all, blockN)
		return nil
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total entries, got %d: %v", len(all), all)
	}

	var pref []uint64
	startKey := modules.EncodeBlockNumber(1)
	if err := ForPrefix(tx, modules.AccountChangeSet, startKey, func(blockN uint64, k, v []byte) error {
		pref = append(pref, blockN)
		return nil
	}); err != nil {
		t.Fatalf("ForPrefix: %v", err)
	}
	if len(pref) == 0 {
		t.Fatalf("expected ForPrefix to find entries")
	}

	addrs, err := GetModifiedAccounts(tx, 0, 10)
	if err != nil {
		t.Fatalf("GetModifiedAccounts: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("expected 2 distinct modified addresses, got %d", len(addrs))
	}
}

func TestG35GetModifiedAccountsEmpty(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addrs, err := GetModifiedAccounts(tx, 0, 10)
	if err != nil {
		t.Fatalf("GetModifiedAccounts: %v", err)
	}
	if addrs != nil {
		t.Fatalf("expected nil result for empty changeset, got %v", addrs)
	}
}

func TestG35Truncate(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	addr1 := types.Address{3}
	writeAccountChange(t, tx, 1, addr1, []byte("v1"))
	writeAccountChange(t, tx, 10, addr1, []byte("v10"))

	if err := Truncate(tx, 5); err != nil {
		t.Fatalf("Truncate: %v", err)
	}

	var remaining []uint64
	if err := ForEach(tx, modules.AccountChangeSet, nil, func(blockN uint64, k, v []byte) error {
		remaining = append(remaining, blockN)
		return nil
	}); err != nil {
		t.Fatalf("ForEach after truncate: %v", err)
	}
	if len(remaining) != 1 || remaining[0] != 1 {
		t.Fatalf("expected only block 1 to remain, got %v", remaining)
	}
}
