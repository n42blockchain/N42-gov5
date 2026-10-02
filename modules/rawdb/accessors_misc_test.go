// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// GetAccount decodes a stored account row, returns found=false for a missing
// address and propagates a decode error on malformed data.
func TestGetAccount(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")

	var out account.StateAccount
	found, err := GetAccount(tx, addr, &out)
	if err != nil || found {
		t.Fatalf("missing account: found=%v err=%v", found, err)
	}

	in := &account.StateAccount{Nonce: 7}
	in.Balance = *uint256.NewInt(123)
	buf := make([]byte, in.EncodingLengthForStorage())
	in.EncodeForStorage(buf)
	if err := tx.Put(modules.Account, addr[:], buf); err != nil {
		t.Fatal(err)
	}

	found, err = GetAccount(tx, addr, &out)
	if err != nil || !found {
		t.Fatalf("GetAccount = %v, %v", found, err)
	}
	if out.Nonce != 7 || out.Balance.Uint64() != 123 {
		t.Fatalf("decoded account mismatch: %+v", out)
	}

	// Malformed data surfaces the decode error.
	if err := tx.Put(modules.Account, addr[:], []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAccount(tx, addr, &out); err == nil {
		t.Fatal("expected decode error for malformed account data")
	}
}

// DeleteBlockAccessList removes a stored BAL; deleting an absent one is a
// harmless no-op.
func TestDeleteBlockAccessList(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	hash := types.Hash{0x07}

	if err := WriteBlockAccessList(tx, hash, []byte("bal-payload")); err != nil {
		t.Fatal(err)
	}
	if got := ReadBlockAccessList(tx, hash); string(got) != "bal-payload" {
		t.Fatalf("ReadBlockAccessList = %q", got)
	}

	if err := DeleteBlockAccessList(tx, hash); err != nil {
		t.Fatal(err)
	}
	if got := ReadBlockAccessList(tx, hash); got != nil {
		t.Fatalf("ReadBlockAccessList after delete = %q", got)
	}

	// Deleting again (already absent) must not error.
	if err := DeleteBlockAccessList(tx, hash); err != nil {
		t.Fatalf("DeleteBlockAccessList on absent key: %v", err)
	}

	// Writing an empty raw is a no-op: nothing to read back.
	empty := types.Hash{0x08}
	if err := WriteBlockAccessList(tx, empty, nil); err != nil {
		t.Fatal(err)
	}
	if got := ReadBlockAccessList(tx, empty); got != nil {
		t.Fatalf("ReadBlockAccessList after empty write = %q", got)
	}
}

// EncodeBlobSidecars/DecodeBlobSidecars are the exported wrappers around the
// storage/wire codec used outside this package (gossip path).
func TestEncodeDecodeBlobSidecarsExported(t *testing.T) {
	sidecars := []*block.BlobSidecar{
		{
			Index:       3,
			BlockNumber: 100,
			BlockHash:   types.HexToHash("0xabc"),
			TxHash:      types.HexToHash("0xdef"),
		},
	}

	data, err := EncodeBlobSidecars(sidecars)
	if err != nil {
		t.Fatalf("EncodeBlobSidecars: %v", err)
	}
	got, err := DecodeBlobSidecars(data)
	if err != nil {
		t.Fatalf("DecodeBlobSidecars: %v", err)
	}
	if len(got) != 1 || got[0].Index != 3 || got[0].BlockNumber != 100 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// Empty input encodes to a valid zero-count buffer and decodes back to
	// an empty (nil or zero-length) slice.
	emptyData, err := EncodeBlobSidecars(nil)
	if err != nil {
		t.Fatalf("EncodeBlobSidecars(nil): %v", err)
	}
	emptyGot, err := DecodeBlobSidecars(emptyData)
	if err != nil {
		t.Fatalf("DecodeBlobSidecars(empty): %v", err)
	}
	if len(emptyGot) != 0 {
		t.Fatalf("expected empty result, got %d entries", len(emptyGot))
	}
}
