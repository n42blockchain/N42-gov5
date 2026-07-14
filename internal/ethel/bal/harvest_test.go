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

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/state"
)

// fakeView is a lightweight AccessView carrying hand-built read/write sets that
// use the real MVHashMap key encoding, so the harvester's decode path is exercised.
type fakeView struct {
	reads  []state.ReadRecord
	writes map[string][]byte
}

func (f *fakeView) ReadSet() []state.ReadRecord   { return f.reads }
func (f *fakeView) WriteSet() map[string][]byte   { return f.writes }

func storageWriteEntry(w map[string][]byte, a types.Address, s types.Hash, v *uint256.Int) {
	if v == nil || v.IsZero() {
		w[string(state.EncodeStorageKey(a, s))] = nil
		return
	}
	w[string(state.EncodeStorageKey(a, s))] = v.Bytes()
}

func accountWriteEntry(w map[string][]byte, a types.Address, nonce uint64, bal *uint256.Int) {
	acc := account.StateAccount{Nonce: nonce, Balance: *bal}
	w[string(state.EncodeAccountKey(a))] = acc.MarshalV2()
}

// TestHarvestBuildsBALFromViews harvests a single committed view and checks the
// storage post-values, balance/nonce, and read set decode into the right BAL.
func TestHarvestBuildsBALFromViews(t *testing.T) {
	writes := map[string][]byte{}
	storageWriteEntry(writes, addr(0x01), slot(0x02), uint256.NewInt(0x33))
	accountWriteEntry(writes, addr(0x01), 7, uint256.NewInt(0xdead))

	v := &fakeView{
		writes: writes,
		reads: []state.ReadRecord{
			{Key: state.EncodeStorageKey(addr(0x02), slot(0x09))},
			{Key: state.EncodeAccountKey(addr(0x02))}, // account read: ignored by BAL
		},
	}

	b := BuildBALFromViews([]AccessView{v}, 1)

	if len(b.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(b.Accounts))
	}
	a1 := b.Accounts[0]
	if a1.Address != addr(0x01) {
		t.Fatalf("account0 = %x, want addr01", a1.Address)
	}
	if len(a1.StorageChanges) != 1 || a1.StorageChanges[0].Slot != slot(0x02) {
		t.Fatalf("addr01 storage changes wrong: %+v", a1.StorageChanges)
	}
	w := a1.StorageChanges[0].Writes
	if len(w) != 1 || w[0].TxIndex != 1 || w[0].NewValue != val(0x33) {
		t.Fatalf("addr01 slot02 write wrong: %+v", w)
	}
	if len(a1.BalanceChanges) != 1 || a1.BalanceChanges[0].TxIndex != 1 || a1.BalanceChanges[0].PostBalance.Uint64() != 0xdead {
		t.Fatalf("addr01 balance change wrong: %+v", a1.BalanceChanges)
	}
	if len(a1.NonceChanges) != 1 || a1.NonceChanges[0].NewNonce != 7 {
		t.Fatalf("addr01 nonce change wrong: %+v", a1.NonceChanges)
	}
	a2 := b.Accounts[1]
	if a2.Address != addr(0x02) {
		t.Fatalf("account1 = %x, want addr02", a2.Address)
	}
	if len(a2.StorageReads) != 1 || a2.StorageReads[0] != slot(0x09) {
		t.Fatalf("addr02 reads wrong: %+v", a2.StorageReads)
	}
}

// TestHarvestCodeChange checks that a SetCode (account codeHash write + matching
// code-key write in the same view) is harvested as a CodeChange with the bytes.
func TestHarvestCodeChange(t *testing.T) {
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	codeHash := crypto.Keccak256Hash(code)

	acc := account.StateAccount{Nonce: 1, Balance: *uint256.NewInt(1), CodeHash: codeHash}
	writes := map[string][]byte{
		string(state.EncodeAccountKey(addr(0x01))): acc.MarshalV2(),
		string(state.EncodeCodeKey(codeHash)):      code,
	}
	b := BuildBALFromViews([]AccessView{&fakeView{writes: writes}}, 1)

	if len(b.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(b.Accounts))
	}
	cc := b.Accounts[0].CodeChanges
	if len(cc) != 1 || cc[0].TxIndex != 1 {
		t.Fatalf("code changes wrong: %+v", cc)
	}
	if !bytes.Equal(cc[0].NewCode, code) {
		t.Fatalf("code bytes = %x, want %x", cc[0].NewCode, code)
	}
}

// TestHarvestZeroStorageWrite checks a slot written to zero (nil value) is still
// captured with a zero post-value.
func TestHarvestZeroStorageWrite(t *testing.T) {
	writes := map[string][]byte{}
	storageWriteEntry(writes, addr(0x05), slot(0x06), nil) // written to zero

	v := &fakeView{writes: writes}
	b := BuildBALFromViews([]AccessView{v}, 3)

	if len(b.Accounts) != 1 || len(b.Accounts[0].StorageChanges) != 1 {
		t.Fatalf("expected one zero-write storage change, got %+v", b.Accounts)
	}
	w := b.Accounts[0].StorageChanges[0].Writes[0]
	if w.TxIndex != 3 || (w.NewValue != types.Hash{}) {
		t.Fatalf("zero write wrong: txIndex=%d value=%x", w.TxIndex, w.NewValue)
	}
}

// TestHarvestTxIndexingAcrossViews checks views map to ascending EIP-7928 indexes.
func TestHarvestTxIndexingAcrossViews(t *testing.T) {
	w0 := map[string][]byte{}
	storageWriteEntry(w0, addr(0x0a), slot(0x01), uint256.NewInt(1))
	w1 := map[string][]byte{}
	storageWriteEntry(w1, addr(0x0a), slot(0x01), uint256.NewInt(2))

	// two txs writing the same slot; baseTxIndex 1 => indexes 1 and 2
	b := BuildBALFromViews([]AccessView{&fakeView{writes: w0}, &fakeView{writes: w1}}, 1)
	if len(b.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(b.Accounts))
	}
	ws := b.Accounts[0].StorageChanges[0].Writes
	if len(ws) != 2 || ws[0].TxIndex != 1 || ws[1].TxIndex != 2 {
		t.Fatalf("tx indexing wrong: %+v", ws)
	}
	if ws[0].NewValue != val(1) || ws[1].NewValue != val(2) {
		t.Fatalf("post-values wrong: %+v", ws)
	}
}

// TestHarvestReadAlsoWrittenExcluded checks a slot both read and written is a
// write, not a read (BuildBAL reconciliation applied through the harvest path).
func TestHarvestReadAlsoWrittenExcluded(t *testing.T) {
	writes := map[string][]byte{}
	storageWriteEntry(writes, addr(0x01), slot(0x08), uint256.NewInt(9))
	v := &fakeView{
		writes: writes,
		reads: []state.ReadRecord{
			{Key: state.EncodeStorageKey(addr(0x01), slot(0x08))}, // same slot written
			{Key: state.EncodeStorageKey(addr(0x01), slot(0x07))}, // read-only
		},
	}
	b := BuildBALFromViews([]AccessView{v}, 1)
	a := b.Accounts[0]
	if len(a.StorageReads) != 1 || a.StorageReads[0] != slot(0x07) {
		t.Fatalf("reads should be only slot07: %x", a.StorageReads)
	}
	if len(a.StorageChanges) != 1 || a.StorageChanges[0].Slot != slot(0x08) {
		t.Fatalf("slot08 should be a write: %+v", a.StorageChanges)
	}
}
