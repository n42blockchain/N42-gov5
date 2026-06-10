// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// TestVirtualTdSemantics: with the marker mode on, ReadTd must answer 0 for a
// block whose HEADER exists and nil for an unknown block — preserving the
// ErrUnknownAncestor contract verbatim while storing zero TD rows. Legacy rows
// and the marker helpers are covered too.
func TestVirtualTdSemantics(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	prev := VirtualTd
	defer func() { VirtualTd = prev }()

	// A real stored header (the existence source of truth).
	h := &block.Header{
		Number:     uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
		Extra:      make([]byte, 32),
	}
	WriteHeader(tx, h)
	known := h.Hash()
	unknown := types.Hash{0xde, 0xad}

	// Mode off: no TD row -> nil for everything (legacy behavior).
	VirtualTd = false
	if td, _ := ReadTd(tx, known, 7); td != nil {
		t.Fatalf("mode off: expected nil, got %v", td)
	}

	// Mode on: known header -> synthesized 0; unknown -> nil (ErrUnknownAncestor).
	VirtualTd = true
	td, err := ReadTd(tx, known, 7)
	if err != nil || td == nil || !td.IsZero() {
		t.Fatalf("mode on, known header: want 0, got %v err %v", td, err)
	}
	if td, _ := ReadTd(tx, unknown, 7); td != nil {
		t.Fatalf("mode on, unknown block: want nil, got %v", td)
	}
	if td, _ := ReadTd(tx, known, 8); td != nil {
		t.Fatalf("mode on, wrong height: want nil, got %v", td)
	}

	// WriteTd is a no-op in virtual mode: no physical row appears.
	if err := WriteTd(tx, known, 7, uint256.NewInt(0)); err != nil {
		t.Fatalf("WriteTd: %v", err)
	}
	if v, _ := tx.GetOne(modules.HeaderTD, modules.HeaderKey(7, known)); v != nil {
		t.Fatal("virtual mode must not write a TD row")
	}

	// ...but a LEGACY row (written with mode off) still decodes with mode on —
	// the mixed-table guarantee for old chains.
	VirtualTd = false
	legacy := types.Hash{0x01}
	if err := WriteTd(tx, legacy, 9, uint256.NewInt(314)); err != nil {
		t.Fatalf("WriteTd legacy: %v", err)
	}
	VirtualTd = true
	td, err = ReadTd(tx, legacy, 9)
	if err != nil || td == nil || td.Uint64() != 314 {
		t.Fatalf("legacy row under virtual mode: want 314, got %v err %v", td, err)
	}

	// Marker helpers round-trip.
	if HasVirtualTdMarker(tx) {
		t.Fatal("marker should be absent")
	}
	if err := WriteVirtualTdMarker(tx); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !HasVirtualTdMarker(tx) {
		t.Fatal("marker should be present")
	}
	VirtualTd = false
	SetupVirtualTdFromDB(tx)
	if !VirtualTd {
		t.Fatal("SetupVirtualTdFromDB did not enable the mode")
	}
}
