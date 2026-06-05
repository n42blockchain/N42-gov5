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

package parallel

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// stubBaseReader is a minimal StateReader whose storage map represents the
// pre-block ("base") state — the data that SHOULD be shadowed after a wipe.
type stubBaseReader struct {
	storage map[types.Hash][]byte
}

func (s *stubBaseReader) ReadAccountData(types.Address) (*account.StateAccount, error) {
	return nil, nil
}
func (s *stubBaseReader) ReadAccountStorage(_ types.Address, key *types.Hash) ([]byte, error) {
	return s.storage[*key], nil
}
func (s *stubBaseReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) {
	return nil, nil
}
func (s *stubBaseReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) {
	return 0, nil
}

func readSlotAt(base *stubBaseReader, mvs *MVS, addr types.Address, slot types.Hash, txIndex int) ([]byte, *ReadWriteSet) {
	rw := NewReadWriteSet(txIndex)
	r := NewParallelStateReader(base, mvs, rw, txIndex)
	v, _ := r.ReadAccountStorage(addr, &slot)
	return v, rw
}

// TestStorageWipeShadowsStaleBaseSlot reproduces the SELFDESTRUCT stale-read
// bug: a tx ordered after an address's wipe must see zero for a slot that only
// exists in pre-wipe base state — not the destroyed contract's old value.
func TestStorageWipeShadowsStaleBaseSlot(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	slot := types.HexToHash("0x01")
	stale := []byte{0xde, 0xad}

	base := &stubBaseReader{storage: map[types.Hash][]byte{slot: stale}}

	// Control: no wipe → reader at tx8 sees the base value (this is the pre-fix
	// behaviour and is correct when nothing wiped the address).
	{
		mvs := NewMVS()
		got, _ := readSlotAt(base, mvs, addr, slot, 8)
		if !bytes.Equal(got, stale) {
			t.Fatalf("no-wipe: expected base value %x, got %x", stale, got)
		}
	}

	// Wipe at tx5 → reader at tx8 must see zero (shadowed), NOT the stale base
	// value. This is the bug the fix closes.
	{
		mvs := NewMVS()
		mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 5, 0, []byte{1})
		got, rw := readSlotAt(base, mvs, addr, slot, 8)
		if len(got) != 0 {
			t.Fatalf("post-wipe: expected zero slot, got %x", got)
		}
		// The wipe read must be recorded so validation can re-run the tx if a
		// later commit changes who wiped the address.
		var sawWipeRead bool
		for _, rd := range rw.Reads {
			if rd.Key.Field == FieldStorageWipe {
				sawWipeRead = true
			}
		}
		if !sawWipeRead {
			t.Fatal("post-wipe: wipe marker read was not recorded for validation")
		}
	}
}

// TestStorageWipeSameTxRewriteWins: a tx that wipes AND re-writes a slot in the
// same step (recreate-then-SSTORE) must expose the new value, not zero — the
// explicit slot write at the same txIdx wins over the wipe.
func TestStorageWipeSameTxRewriteWins(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	slot := types.HexToHash("0x02")
	base := &stubBaseReader{storage: map[types.Hash][]byte{slot: {0xff}}}

	mvs := NewMVS()
	fresh := []byte{0x09}
	mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 5, 0, []byte{1})
	mvs.Write(LocationKey{Address: addr, Field: FieldStorage, Slot: slot}, 5, 0, fresh)

	got, _ := readSlotAt(base, mvs, addr, slot, 8)
	if !bytes.Equal(got, fresh) {
		t.Fatalf("same-tx rewrite: expected fresh value %x, got %x", fresh, got)
	}
}

// TestStorageWipeShadowsOlderMVSWrite: a slot written by an EARLIER tx (tx3),
// then wiped by a LATER tx (tx5), must read as zero for tx8 — the wipe is newer
// than the slot's writer.
func TestStorageWipeShadowsOlderMVSWrite(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000cc")
	slot := types.HexToHash("0x03")
	base := &stubBaseReader{storage: map[types.Hash][]byte{}}

	mvs := NewMVS()
	mvs.Write(LocationKey{Address: addr, Field: FieldStorage, Slot: slot}, 3, 0, []byte{0x77})
	mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 5, 0, []byte{1})

	got, _ := readSlotAt(base, mvs, addr, slot, 8)
	if len(got) != 0 {
		t.Fatalf("older-write wipe: expected zero slot, got %x", got)
	}
}

// TestStorageWipeNotVisibleToEarlierTx: a reader ordered BEFORE the wipe (tx4 <
// wipe tx5) must NOT be shadowed — it legitimately precedes the SELFDESTRUCT.
func TestStorageWipeNotVisibleToEarlierTx(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000dd")
	slot := types.HexToHash("0x04")
	stale := []byte{0x42}
	base := &stubBaseReader{storage: map[types.Hash][]byte{slot: stale}}

	mvs := NewMVS()
	mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 5, 0, []byte{1})

	got, _ := readSlotAt(base, mvs, addr, slot, 4) // tx4 precedes the wipe
	if !bytes.Equal(got, stale) {
		t.Fatalf("pre-wipe reader: expected base value %x, got %x", stale, got)
	}
}

// TestStorageWipeReadInvalidatedByNewerWiper: the recorded wipe read must fail
// validation if a newer preceding tx becomes the wiper after execution (the
// Block-STM re-execution trigger).
func TestStorageWipeReadInvalidatedByNewerWiper(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000ee")
	slot := types.HexToHash("0x05")
	base := &stubBaseReader{storage: map[types.Hash][]byte{slot: {0x01}}}

	mvs := NewMVS()
	mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 5, 0, []byte{1})

	_, rw := readSlotAt(base, mvs, addr, slot, 8)
	if !Validate(mvs, rw) {
		t.Fatal("read set should be valid before any change")
	}

	// A newer preceding tx (tx7) now also wipes the address → the wipe read's
	// writer changed from tx5 to tx7 → the tx must re-execute.
	mvs.Write(LocationKey{Address: addr, Field: FieldStorageWipe}, 7, 0, []byte{1})
	if Validate(mvs, rw) {
		t.Fatal("read set should be invalid after a newer wiper appears")
	}
}
