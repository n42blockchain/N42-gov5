// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestCommitteeRegistrationRoundTrip: persisted hand-overs read back exactly.
func TestCommitteeRegistrationRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	pk7 := bytes.Repeat([]byte{0x07}, 48)
	pk42 := bytes.Repeat([]byte{0x42}, 48)
	if err := WriteCommitteeRegistration(tx, 7, pk7); err != nil {
		t.Fatalf("write slot 7: %v", err)
	}
	if err := WriteCommitteeRegistration(tx, 42, pk42); err != nil {
		t.Fatalf("write slot 42: %v", err)
	}

	regs, err := ReadCommitteeRegistrations(tx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("got %d registrations, want 2", len(regs))
	}
	if !bytes.Equal(regs[7], pk7) || !bytes.Equal(regs[42], pk42) {
		t.Fatalf("registrations round-trip mismatch: %x / %x", regs[7], regs[42])
	}

	// Wrong-length pubkey is rejected.
	if err := WriteCommitteeRegistration(tx, 1, []byte{0x01}); err == nil {
		t.Fatal("write accepted a non-48-byte pubkey")
	}
}
