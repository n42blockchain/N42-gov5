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

package bal

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

func addr(b byte) types.Address { var a types.Address; a[19] = b; return a }
func slot(b byte) types.Hash    { var h types.Hash; h[31] = b; return h }
func val(b byte) types.Hash     { var h types.Hash; h[31] = b; return h }

// TestBuildBALCanonicalOrder feeds unsorted accounts/slots/tx-indexes and checks
// the builder emits them in EIP-7928 canonical order.
func TestBuildBALCanonicalOrder(t *testing.T) {
	txs := []TxAccess{
		{TxIndex: 2, StorageWrites: []SlotWrite{{addr(0x02), slot(0x05), val(0x22)}, {addr(0x01), slot(0x09), val(0x91)}}},
		{TxIndex: 1, StorageWrites: []SlotWrite{{addr(0x02), slot(0x01), val(0x11)}, {addr(0x01), slot(0x09), val(0x90)}}},
	}
	b := BuildBAL(txs)

	if len(b.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(b.Accounts))
	}
	if b.Accounts[0].Address != addr(0x01) || b.Accounts[1].Address != addr(0x02) {
		t.Fatalf("accounts not sorted by address: %x, %x", b.Accounts[0].Address, b.Accounts[1].Address)
	}
	// addr01 slot09 written by tx1 then tx2 -> writes sorted by tx index.
	a1 := b.Accounts[0]
	if len(a1.StorageChanges) != 1 || a1.StorageChanges[0].Slot != slot(0x09) {
		t.Fatalf("addr01 storage changes wrong: %+v", a1.StorageChanges)
	}
	ws := a1.StorageChanges[0].Writes
	if len(ws) != 2 || ws[0].TxIndex != 1 || ws[1].TxIndex != 2 {
		t.Fatalf("addr01 slot09 writes not sorted by tx index: %+v", ws)
	}
	if ws[0].NewValue != val(0x90) || ws[1].NewValue != val(0x91) {
		t.Fatalf("addr01 slot09 post-values wrong: %+v", ws)
	}
	// addr02 slots 0x01 then 0x05 -> sorted by slot.
	a2 := b.Accounts[1]
	if len(a2.StorageChanges) != 2 || a2.StorageChanges[0].Slot != slot(0x01) || a2.StorageChanges[1].Slot != slot(0x05) {
		t.Fatalf("addr02 slots not sorted: %+v", a2.StorageChanges)
	}
}

// TestBuildBALReadsExcludeWrites checks a slot that is both read and written is
// classified as a write, not a read.
func TestBuildBALReadsExcludeWrites(t *testing.T) {
	txs := []TxAccess{
		{TxIndex: 1,
			StorageReads:  []SlotRead{{addr(0x01), slot(0x07)}, {addr(0x01), slot(0x08)}},
			StorageWrites: []SlotWrite{{addr(0x01), slot(0x08), val(0x01)}},
		},
	}
	b := BuildBAL(txs)
	if len(b.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(b.Accounts))
	}
	a := b.Accounts[0]
	if len(a.StorageReads) != 1 || a.StorageReads[0] != slot(0x07) {
		t.Fatalf("reads should be just slot07 (slot08 written): %x", a.StorageReads)
	}
	if len(a.StorageChanges) != 1 || a.StorageChanges[0].Slot != slot(0x08) {
		t.Fatalf("slot08 should be a write: %+v", a.StorageChanges)
	}
}

// TestBuildBALDeterministic checks two different feed orderings of the same
// logical changes yield byte-identical RLP and hash.
func TestBuildBALDeterministic(t *testing.T) {
	mk := func(order int) []TxAccess {
		w := []SlotWrite{
			{addr(0x03), slot(0x02), val(0x30)},
			{addr(0x01), slot(0x04), val(0x10)},
			{addr(0x02), slot(0x06), val(0x20)},
		}
		if order == 1 {
			w[0], w[2] = w[2], w[0]
		}
		return []TxAccess{
			{TxIndex: 1, StorageWrites: w,
				BalanceChanges: []AccountBalance{{addr(0x01), *uint256.NewInt(100)}}},
		}
	}
	b0 := BuildBAL(mk(0))
	b1 := BuildBAL(mk(1))

	e0, err := b0.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	e1, err := b1.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e0, e1) {
		t.Fatalf("encoding not deterministic across feed order:\n %x\n %x", e0, e1)
	}
	h0, _ := b0.Hash()
	h1, _ := b1.Hash()
	if h0 != h1 {
		t.Fatalf("hash not deterministic: %x vs %x", h0, h1)
	}
	if (h0 == types.Hash{}) {
		t.Fatal("non-empty BAL hashed to zero")
	}
}

// TestBuildBALEmpty checks an empty block produces an empty, well-formed BAL.
func TestBuildBALEmpty(t *testing.T) {
	b := BuildBAL(nil)
	if len(b.Accounts) != 0 {
		t.Fatalf("empty BAL has %d accounts", len(b.Accounts))
	}
	enc, err := b.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	var w wireBAL
	if err := rlp.DecodeBytes(enc, &w); err != nil {
		t.Fatalf("empty BAL RLP does not round-trip: %v", err)
	}
	if len(w.Accounts) != 0 {
		t.Fatalf("decoded empty BAL has %d accounts", len(w.Accounts))
	}
}

// TestBALRLPRoundTrip checks the encoding decodes into the wire structure with
// the expected shape (addresses, slots, post-values, balances preserved).
func TestBALRLPRoundTrip(t *testing.T) {
	txs := []TxAccess{
		{TxIndex: 1,
			StorageWrites:  []SlotWrite{{addr(0x01), slot(0x02), val(0x33)}},
			BalanceChanges: []AccountBalance{{addr(0x01), *uint256.NewInt(0xdead)}},
			NonceChanges:   []AccountNonce{{addr(0x01), 7}},
			CodeChanges:    []AccountCode{{addr(0x01), []byte{0x60, 0x00}}},
		},
		{TxIndex: 2, StorageReads: []SlotRead{{addr(0x02), slot(0x09)}}},
	}
	b := BuildBAL(txs)
	enc, err := b.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	var w wireBAL
	if err := rlp.DecodeBytes(enc, &w); err != nil {
		t.Fatalf("round-trip decode failed: %v", err)
	}
	if len(w.Accounts) != 2 {
		t.Fatalf("decoded accounts = %d, want 2", len(w.Accounts))
	}
	a1 := w.Accounts[0]
	if !bytes.Equal(a1.Address, addr(0x01).Bytes()) {
		t.Fatalf("account0 address = %x", a1.Address)
	}
	if len(a1.StorageChanges) != 1 || a1.StorageChanges[0].Writes[0].TxIndex != 1 {
		t.Fatalf("account0 storage change wrong: %+v", a1.StorageChanges)
	}
	if len(a1.BalanceChanges) != 1 || !bytes.Equal(a1.BalanceChanges[0].PostBalance, uint256.NewInt(0xdead).Bytes()) {
		t.Fatalf("account0 balance change wrong: %+v", a1.BalanceChanges)
	}
	if len(a1.NonceChanges) != 1 || a1.NonceChanges[0].NewNonce != 7 {
		t.Fatalf("account0 nonce change wrong: %+v", a1.NonceChanges)
	}
	if len(a1.CodeChanges) != 1 || !bytes.Equal(a1.CodeChanges[0].NewCode, []byte{0x60, 0x00}) {
		t.Fatalf("account0 code change wrong: %+v", a1.CodeChanges)
	}
	a2 := w.Accounts[1]
	if len(a2.StorageReads) != 1 || !bytes.Equal(a2.StorageReads[0], slot(0x09).Bytes()) {
		t.Fatalf("account1 read wrong: %+v", a2.StorageReads)
	}
}
