// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers StoreWitness/GetWitness, Close/StopInsert/insertStopped, and the
// rawBlockBytes SSZ-shim methods used to carry pre-encoded RLP block bytes
// through the sync wire framing.

package internal

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state/witness"
)

func TestStoreAndGetWitness(t *testing.T) {
	cache, err := lru.New[types.Hash, *witness.BlockWitness](4)
	if err != nil {
		t.Fatal(err)
	}
	bc := &BlockChain{witnessCache: cache}

	hash := types.HexToHash("0x1")
	if _, ok := bc.GetWitness(hash); ok {
		t.Fatalf("GetWitness() before Store = ok, want not found")
	}

	// nil witness must not be stored.
	bc.StoreWitness(hash, nil)
	if _, ok := bc.GetWitness(hash); ok {
		t.Fatalf("StoreWitness(nil) unexpectedly cached an entry")
	}

	w := &witness.BlockWitness{}
	bc.StoreWitness(hash, w)
	got, ok := bc.GetWitness(hash)
	if !ok || got != w {
		t.Fatalf("GetWitness() = (%v, %v), want the stored witness", got, ok)
	}
}

func TestCloseCancelsAndWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bc := &BlockChain{ctx: ctx, cancel: cancel}
	if err := bc.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatalf("Close() did not cancel the context")
	}
}

func TestStopInsertAndInsertStopped(t *testing.T) {
	bc := &BlockChain{}
	if bc.insertStopped() {
		t.Fatalf("insertStopped() = true before StopInsert")
	}
	bc.StopInsert()
	if !bc.insertStopped() {
		t.Fatalf("insertStopped() = false after StopInsert")
	}
}

func TestRawBlockBytesRoundTrip(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04}
	r := &rawBlockBytes{data: data}

	got, err := r.MarshalSSZ()
	if err != nil || string(got) != string(data) {
		t.Fatalf("MarshalSSZ() = (%v, %v), want (%v, nil)", got, err, data)
	}
	if r.SizeSSZ() != len(data) {
		t.Fatalf("SizeSSZ() = %d, want %d", r.SizeSSZ(), len(data))
	}

	buf, err := r.MarshalSSZTo([]byte{0xff})
	if err != nil || string(buf) != string(append([]byte{0xff}, data...)) {
		t.Fatalf("MarshalSSZTo() = (%v, %v), want prefixed data", buf, err)
	}

	var r2 rawBlockBytes
	if err := r2.UnmarshalSSZ(data); err != nil {
		t.Fatal(err)
	}
	if string(r2.data) != string(data) {
		t.Fatalf("UnmarshalSSZ() data = %v, want %v", r2.data, data)
	}
	// UnmarshalSSZ must copy, not alias, the input buffer.
	data[0] = 0xee
	if r2.data[0] == 0xee {
		t.Fatalf("UnmarshalSSZ() aliased the input buffer")
	}
}
