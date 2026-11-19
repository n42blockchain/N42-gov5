// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// measureTransferShape counts senders, chains, recipients and the overlap.
func TestMeasureTransferShape(t *testing.T) {
	a, b, c, d, e := types.Address{1}, types.Address{2}, types.Address{3}, types.Address{4}, types.Address{5}
	mk := func(from *types.Address, to *types.Address, data []byte) *transaction.Transaction {
		tx := transaction.NewTx(&transaction.LegacyTx{To: to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1), Data: data})
		if from != nil {
			tx.SetFrom(*from)
		}
		return tx
	}
	txs := []*transaction.Transaction{
		mk(&a, &b, nil),           // a -> b
		mk(&a, &c, nil),           // a -> c (a sends twice)
		mk(&b, &d, nil),           // b sends and receives
		mk(&c, &e, []byte{0x01}),  // c sends and receives; call data
		mk(&e, nil, []byte{0x60}), // e: create, e also receives
		mk(nil, &a, nil),          // no cached sender; a receives
	}
	s := measureTransferShape(nil, txs)
	want := blockTransferShape{txs: 6, noSender: 1, plainShape: 4, senders: 4, multiSenders: 1, maxChain: 2,
		recipients: 5, sharedSenders: 4, sharedTxs: 5}
	if s != want {
		t.Fatalf("shape = %+v, want %+v", s, want)
	}
	if f := s.fields(); len(f) != 18 {
		t.Fatalf("fields: %d values", len(f))
	}
	if z := measureTransferShape(nil, nil); z != (blockTransferShape{}) {
		t.Fatalf("empty block: %+v", z)
	}
}
