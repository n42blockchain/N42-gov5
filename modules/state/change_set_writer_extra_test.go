// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestChangeSetWriterChangedCountsNil(t *testing.T) {
	var w *ChangeSetWriter
	accounts, storage := w.ChangedCounts()
	if accounts != 0 || storage != 0 {
		t.Fatalf("ChangedCounts(nil) = %d, %d, want 0, 0", accounts, storage)
	}
}

func TestChangeSetWriterChangedCountsAndPrint(t *testing.T) {
	w := NewChangeSetWriter()

	var addr types.Address
	addr[0] = 0xA1
	orig := account.StateAccount{Initialised: true, Nonce: 1}
	acc := account.StateAccount{Initialised: true, Nonce: 2}
	if err := w.UpdateAccountData(addr, &orig, &acc); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}
	if err := w.WriteAccountStorage(addr, types.Hash{0x01}, uint256.Int{}, *uint256.NewInt(5)); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}

	accounts, storage := w.ChangedCounts()
	if accounts != 1 || storage != 1 {
		t.Fatalf("ChangedCounts = %d, %d, want 1, 1", accounts, storage)
	}

	// UpdateAccountCode on ChangeSetWriter is a documented no-op (code
	// changes aren't tracked in the account changeset).
	if err := w.UpdateAccountCode(addr, types.Hash{0x02}, []byte{0x60}); err != nil {
		t.Fatalf("UpdateAccountCode = %v, want nil", err)
	}

	// Exercise the diagnostic print path; it must not panic.
	w.PrintChangedAccounts()
}

func TestChangeSetWriterHistoryAggregatorRouting(t *testing.T) {
	w := NewChangeSetWriter()
	agg := NewHistoryAggregator()
	w.SetHistoryAggregator(agg)

	var addr types.Address
	addr[0] = 0xB2
	orig := account.StateAccount{Initialised: true, Nonce: 1}
	acc := account.StateAccount{Initialised: true, Nonce: 2}
	if err := w.UpdateAccountData(addr, &orig, &acc); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}

	// With an aggregator installed, WriteHistory must succeed without a
	// kv.RwTx (it routes through the aggregator instead of w.db).
	if err := w.WriteHistory(); err != nil {
		t.Fatalf("WriteHistory (aggregator routed) = %v, want nil", err)
	}
}
