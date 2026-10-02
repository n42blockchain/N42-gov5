// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TestNoopWriterDiscardsEverything exercises every NoopWriter method; all
// must return nil regardless of input.
func TestNoopWriterDiscardsEverything(t *testing.T) {
	w := NewNoopWriter()
	if w != NewNoopWriter() {
		t.Fatal("NewNoopWriter should return the shared singleton")
	}

	var addr types.Address
	addr[0] = 0x01
	orig := account.StateAccount{}
	acc := account.StateAccount{Nonce: 1}

	if err := w.UpdateAccountData(addr, &orig, &acc); err != nil {
		t.Fatalf("UpdateAccountData = %v, want nil", err)
	}
	if err := w.DeleteAccount(addr, &orig); err != nil {
		t.Fatalf("DeleteAccount = %v, want nil", err)
	}
	if err := w.UpdateAccountCode(addr, types.Hash{0x01}, []byte{0x60}); err != nil {
		t.Fatalf("UpdateAccountCode = %v, want nil", err)
	}
	if err := w.WriteAccountStorage(addr, types.Hash{0x02}, uint256.Int{}, *uint256.NewInt(1)); err != nil {
		t.Fatalf("WriteAccountStorage = %v, want nil", err)
	}
	if err := w.CreateContract(addr); err != nil {
		t.Fatalf("CreateContract = %v, want nil", err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatalf("WriteChangeSets = %v, want nil", err)
	}
	if err := w.WriteHistory(); err != nil {
		t.Fatalf("WriteHistory = %v, want nil", err)
	}
}
