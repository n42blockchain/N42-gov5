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

func blockHash(b byte) types.Hash { var h types.Hash; h[0] = b; return h }

// TestDecodeBALRoundTrip checks a BAL survives encode -> decode -> re-encode
// byte-identically and keeps its hash — so a consumer that received a raw BAL out
// of band can rebuild it and verify against the header hash.
func TestDecodeBALRoundTrip(t *testing.T) {
	orig := BuildBAL([]TxAccess{
		{TxIndex: 1,
			StorageWrites:  []SlotWrite{{addr(0x01), slot(0x02), val(0x33)}},
			BalanceChanges: []AccountBalance{{addr(0x01), *uint256.NewInt(0xdead)}},
			NonceChanges:   []AccountNonce{{addr(0x01), 7}},
			CodeChanges:    []AccountCode{{addr(0x01), []byte{0x60, 0x00}}},
		},
		{TxIndex: 2, StorageReads: []SlotRead{{addr(0x02), slot(0x09)}}},
	})
	raw, err := orig.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := DecodeBAL(raw)
	if err != nil {
		t.Fatalf("DecodeBAL: %v", err)
	}
	raw2, err := dec.EncodeRLP()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, raw2) {
		t.Fatalf("re-encode differs:\n %x\n %x", raw, raw2)
	}
	h1, _ := orig.Hash()
	h2, _ := dec.Hash()
	if h1 != h2 {
		t.Fatalf("hash changed through decode: %s vs %s", h1.Hex(), h2.Hex())
	}
	// Spot-check decoded content.
	a := dec.Accounts[0]
	if a.BalanceChanges[0].PostBalance.Uint64() != 0xdead || a.NonceChanges[0].NewNonce != 7 {
		t.Fatalf("decoded account0 wrong: %+v", a)
	}
}

func TestDecodeBALRejectsBadLengths(t *testing.T) {
	// A wire BAL with a 31-byte slot must be rejected (fixed 32 expected).
	w := wireBAL{Accounts: []wireAccount{{
		Address:        addr(0x01).Bytes(),
		StorageChanges: []wireSlot{{Slot: make([]byte, 31), Writes: []wireStorageWrite{{TxIndex: 1, NewValue: make([]byte, 32)}}}},
	}}}
	raw, err := rlp.EncodeToBytes(w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBAL(raw); err == nil {
		t.Fatal("DecodeBAL accepted a 31-byte slot, want error")
	}
}

// TestBALServiceRoundTrip checks the eth/71-style request/response codecs and the
// serve path (present + absent BALs).
func TestBALServiceRoundTrip(t *testing.T) {
	h1, h2, h3 := blockHash(0x11), blockHash(0x22), blockHash(0x33)
	req := &GetBlockAccessLists{Hashes: []types.Hash{h1, h2, h3}}

	enc, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	req2, err := DecodeGetBlockAccessLists(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(req2.Hashes) != 3 || req2.Hashes[1] != h2 {
		t.Fatalf("request round-trip wrong: %+v", req2.Hashes)
	}

	// h1 and h3 have BALs stored; h2 does not.
	store := map[types.Hash][]byte{h1: {0xaa, 0xbb}, h3: {0xcc}}
	resp := ServeGetBlockAccessLists(req2, func(h types.Hash) []byte { return store[h] })

	renc, err := resp.Encode()
	if err != nil {
		t.Fatal(err)
	}
	resp2, err := DecodeBlockAccessLists(renc)
	if err != nil {
		t.Fatal(err)
	}
	if raw, ok := resp2.Get(h1); !ok || !bytes.Equal(raw, []byte{0xaa, 0xbb}) {
		t.Fatalf("h1 BAL wrong: %x ok=%v", raw, ok)
	}
	if _, ok := resp2.Get(h2); ok {
		t.Fatal("h2 should be absent (empty entry)")
	}
	if raw, ok := resp2.Get(h3); !ok || !bytes.Equal(raw, []byte{0xcc}) {
		t.Fatalf("h3 BAL wrong: %x ok=%v", raw, ok)
	}
}

func TestBALServiceRequestCap(t *testing.T) {
	hashes := make([]types.Hash, MaxBALRequest+50)
	for i := range hashes {
		hashes[i] = blockHash(byte(i))
	}
	resp := ServeGetBlockAccessLists(&GetBlockAccessLists{Hashes: hashes}, func(types.Hash) []byte { return nil })
	if len(resp.Entries) != MaxBALRequest {
		t.Fatalf("response not capped: %d entries, want %d", len(resp.Entries), MaxBALRequest)
	}
}
