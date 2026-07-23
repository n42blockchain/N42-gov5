// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func TestSaveLoadConsensusState(t *testing.T) {
	db := memdb.NewTestDB(t)

	original := &ConsensusState{
		View:                15,
		Phase:               PhaseTimedOut,
		ConsecutiveTimeouts: 3,
		LockedQC: QuorumCertificate{
			View:               10,
			BlockHash:          types.Hash{0xAA, 0xBB},
			AggregateSignature: bytes.Repeat([]byte{0x11}, 96),
			Signers:            []bool{true, false, true, true},
		},
		LastCommittedQC: QuorumCertificate{
			View:               8,
			BlockHash:          types.Hash{0xCC, 0xDD},
			AggregateSignature: bytes.Repeat([]byte{0x22}, 96),
			Signers:            []bool{true, true, false, true},
		},
	}

	// Save
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, original)
	})
	if err != nil {
		t.Fatalf("SaveConsensusState failed: %v", err)
	}

	// Load
	var loaded *ConsensusState
	err = db.View(context.Background(), func(tx kv.Tx) error {
		var loadErr error
		loaded, loadErr = LoadConsensusState(tx)
		return loadErr
	})
	if err != nil {
		t.Fatalf("LoadConsensusState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state should not be nil")
	}

	// Verify all fields
	if loaded.View != original.View {
		t.Fatalf("view mismatch: got %d, want %d", loaded.View, original.View)
	}
	if loaded.ConsecutiveTimeouts != original.ConsecutiveTimeouts {
		t.Fatalf("consecutive timeouts mismatch: got %d, want %d",
			loaded.ConsecutiveTimeouts, original.ConsecutiveTimeouts)
	}
	if loaded.Phase != original.Phase {
		t.Fatalf("phase mismatch: got %s, want %s", loaded.Phase, original.Phase)
	}

	// Verify LockedQC
	if loaded.LockedQC.View != original.LockedQC.View {
		t.Fatalf("locked QC view mismatch: got %d, want %d",
			loaded.LockedQC.View, original.LockedQC.View)
	}
	if loaded.LockedQC.BlockHash != original.LockedQC.BlockHash {
		t.Fatal("locked QC block hash mismatch")
	}
	if !bytes.Equal(loaded.LockedQC.AggregateSignature, original.LockedQC.AggregateSignature) {
		t.Fatal("locked QC aggregate signature mismatch")
	}
	if len(loaded.LockedQC.Signers) != len(original.LockedQC.Signers) {
		t.Fatalf("locked QC signers length mismatch: got %d, want %d",
			len(loaded.LockedQC.Signers), len(original.LockedQC.Signers))
	}
	for i, s := range original.LockedQC.Signers {
		if loaded.LockedQC.Signers[i] != s {
			t.Fatalf("locked QC signer %d mismatch", i)
		}
	}

	// Verify LastCommittedQC
	if loaded.LastCommittedQC.View != original.LastCommittedQC.View {
		t.Fatalf("committed QC view mismatch: got %d, want %d",
			loaded.LastCommittedQC.View, original.LastCommittedQC.View)
	}
	if loaded.LastCommittedQC.BlockHash != original.LastCommittedQC.BlockHash {
		t.Fatal("committed QC block hash mismatch")
	}
	if !bytes.Equal(loaded.LastCommittedQC.AggregateSignature, original.LastCommittedQC.AggregateSignature) {
		t.Fatal("committed QC aggregate signature mismatch")
	}
}

func TestLoadConsensusState_NoState(t *testing.T) {
	db := memdb.NewTestDB(t)

	var loaded *ConsensusState
	err := db.View(context.Background(), func(tx kv.Tx) error {
		var loadErr error
		loaded, loadErr = LoadConsensusState(tx)
		return loadErr
	})
	if err != nil {
		t.Fatalf("LoadConsensusState on empty DB failed: %v", err)
	}
	if loaded != nil {
		t.Fatal("expected nil state when no state exists")
	}
}

func TestSaveLoadConsensusState_WithQC(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Use realistic QC data with large signers bitmap (100 validators)
	signers := make([]bool, 100)
	for i := range signers {
		signers[i] = i%3 != 2 // 67 out of 100 signed
	}

	state := &ConsensusState{
		View:                999,
		ConsecutiveTimeouts: 0,
		LockedQC: QuorumCertificate{
			View:               998,
			BlockHash:          types.Hash{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, 0x20},
			AggregateSignature: bytes.Repeat([]byte{0xAB}, 192), // BLS aggregate sig can be large
			Signers:            signers,
		},
		LastCommittedQC: QuorumCertificate{
			View:               997,
			BlockHash:          types.Hash{0xFF, 0xFE, 0xFD, 0xFC},
			AggregateSignature: bytes.Repeat([]byte{0xCD}, 192),
			Signers:            signers,
		},
	}

	// Save
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, state)
	})
	if err != nil {
		t.Fatalf("SaveConsensusState failed: %v", err)
	}

	// Load and verify
	var loaded *ConsensusState
	err = db.View(context.Background(), func(tx kv.Tx) error {
		var loadErr error
		loaded, loadErr = LoadConsensusState(tx)
		return loadErr
	})
	if err != nil {
		t.Fatalf("LoadConsensusState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state should not be nil")
	}

	if loaded.View != 999 {
		t.Fatalf("view mismatch: got %d", loaded.View)
	}
	if loaded.ConsecutiveTimeouts != 0 {
		t.Fatalf("consecutive timeouts should be 0, got %d", loaded.ConsecutiveTimeouts)
	}
	if loaded.LockedQC.View != 998 {
		t.Fatalf("locked QC view mismatch: got %d", loaded.LockedQC.View)
	}
	if loaded.LastCommittedQC.View != 997 {
		t.Fatalf("committed QC view mismatch: got %d", loaded.LastCommittedQC.View)
	}

	// Verify full block hash roundtrip
	expectedHash := types.Hash{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, 0x20}
	if loaded.LockedQC.BlockHash != expectedHash {
		t.Fatal("locked QC block hash mismatch after roundtrip")
	}

	// Verify signers bitmap roundtrip
	if len(loaded.LockedQC.Signers) != 100 {
		t.Fatalf("expected 100 signers, got %d", len(loaded.LockedQC.Signers))
	}
	for i, s := range signers {
		if loaded.LockedQC.Signers[i] != s {
			t.Fatalf("locked QC signer %d mismatch after roundtrip", i)
		}
	}
}

func TestSaveLoadConsensusState_Overwrite(t *testing.T) {
	db := memdb.NewTestDB(t)

	// Save initial state
	state1 := &ConsensusState{
		View:                10,
		ConsecutiveTimeouts: 1,
		LockedQC: QuorumCertificate{
			View:               9,
			BlockHash:          types.Hash{0x01},
			AggregateSignature: bytes.Repeat([]byte{0xAA}, 48),
			Signers:            []bool{true, true, false, true},
		},
		LastCommittedQC: QuorumCertificate{
			View:               8,
			BlockHash:          types.Hash{0x02},
			AggregateSignature: bytes.Repeat([]byte{0xBB}, 48),
			Signers:            []bool{true, false, true, true},
		},
	}

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, state1)
	})
	if err != nil {
		t.Fatalf("first SaveConsensusState failed: %v", err)
	}

	// Overwrite with new state
	state2 := &ConsensusState{
		View:                20,
		ConsecutiveTimeouts: 5,
		LockedQC: QuorumCertificate{
			View:               19,
			BlockHash:          types.Hash{0xDE, 0xAD},
			AggregateSignature: bytes.Repeat([]byte{0xCC}, 96),
			Signers:            []bool{true, true, true, false},
		},
		LastCommittedQC: QuorumCertificate{
			View:               18,
			BlockHash:          types.Hash{0xBE, 0xEF},
			AggregateSignature: bytes.Repeat([]byte{0xDD}, 96),
			Signers:            []bool{false, true, true, true},
		},
	}

	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, state2)
	})
	if err != nil {
		t.Fatalf("second SaveConsensusState failed: %v", err)
	}

	// Load and verify the overwritten state
	var loaded *ConsensusState
	err = db.View(context.Background(), func(tx kv.Tx) error {
		var loadErr error
		loaded, loadErr = LoadConsensusState(tx)
		return loadErr
	})
	if err != nil {
		t.Fatalf("LoadConsensusState after overwrite failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state should not be nil")
	}

	// Verify it reflects state2, not state1
	if loaded.View != 20 {
		t.Fatalf("view should be 20 (overwritten), got %d", loaded.View)
	}
	if loaded.ConsecutiveTimeouts != 5 {
		t.Fatalf("consecutive timeouts should be 5, got %d", loaded.ConsecutiveTimeouts)
	}
	if loaded.LockedQC.View != 19 {
		t.Fatalf("locked QC view should be 19, got %d", loaded.LockedQC.View)
	}
	if loaded.LockedQC.BlockHash != (types.Hash{0xDE, 0xAD}) {
		t.Fatal("locked QC block hash should be overwritten value")
	}
	if loaded.LastCommittedQC.View != 18 {
		t.Fatalf("committed QC view should be 18, got %d", loaded.LastCommittedQC.View)
	}
	if loaded.LastCommittedQC.BlockHash != (types.Hash{0xBE, 0xEF}) {
		t.Fatal("committed QC block hash should be overwritten value")
	}
	if !bytes.Equal(loaded.LockedQC.AggregateSignature, bytes.Repeat([]byte{0xCC}, 96)) {
		t.Fatal("locked QC aggregate signature should be overwritten value")
	}
	if !bytes.Equal(loaded.LastCommittedQC.AggregateSignature, bytes.Repeat([]byte{0xDD}, 96)) {
		t.Fatal("committed QC aggregate signature should be overwritten value")
	}

	// Verify signers from state2
	expectedLockedSigners := []bool{true, true, true, false}
	for i, s := range expectedLockedSigners {
		if loaded.LockedQC.Signers[i] != s {
			t.Fatalf("locked QC signer %d mismatch after overwrite", i)
		}
	}
	expectedCommittedSigners := []bool{false, true, true, true}
	for i, s := range expectedCommittedSigners {
		if loaded.LastCommittedQC.Signers[i] != s {
			t.Fatalf("committed QC signer %d mismatch after overwrite", i)
		}
	}
}

func TestSaveLoadConsensusState_GenesisQCs(t *testing.T) {
	db := memdb.NewTestDB(t)

	state := &ConsensusState{
		View:                1,
		ConsecutiveTimeouts: 0,
		LockedQC:            GenesisQC(),
		LastCommittedQC:     GenesisQC(),
	}

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, state)
	})
	if err != nil {
		t.Fatalf("SaveConsensusState with genesis QCs failed: %v", err)
	}

	var loaded *ConsensusState
	err = db.View(context.Background(), func(tx kv.Tx) error {
		var loadErr error
		loaded, loadErr = LoadConsensusState(tx)
		return loadErr
	})
	if err != nil {
		t.Fatalf("LoadConsensusState with genesis QCs failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state should not be nil")
	}

	if loaded.View != 1 {
		t.Fatalf("view mismatch: got %d", loaded.View)
	}
	if loaded.LockedQC.View != 0 {
		t.Fatalf("locked QC should be genesis (view 0), got %d", loaded.LockedQC.View)
	}
	if loaded.LastCommittedQC.View != 0 {
		t.Fatalf("committed QC should be genesis (view 0), got %d", loaded.LastCommittedQC.View)
	}
	if loaded.LockedQC.BlockHash != (types.Hash{}) {
		t.Fatal("locked QC block hash should be zero for genesis")
	}
}
