package bodyf2

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func addr(b byte) types.Address {
	var a types.Address
	for i := range a {
		a[i] = b
	}
	return a
}

func TestAddrDictInternSaveLoad(t *testing.T) {
	d := NewAddrDict()
	a0, a1 := addr(1), addr(2)
	if d.Intern(a0) != 0 || d.Intern(a1) != 1 || d.Intern(a0) != 0 {
		t.Fatal("intern IDs/dedup wrong")
	}
	if d.Len() != 2 {
		t.Fatalf("len %d", d.Len())
	}
	p := filepath.Join(t.TempDir(), "addr.dict")
	if err := d.Save(p); err != nil {
		t.Fatal(err)
	}
	d2, err := LoadAddrDict(p)
	if err != nil {
		t.Fatal(err)
	}
	if d2.Len() != 2 {
		t.Fatalf("loaded len %d", d2.Len())
	}
	if got, _ := d2.Addr(1); got != a1 {
		t.Errorf("addr(1) = %x", got)
	}
	if id, _ := d2.ID(a0); id != 0 {
		t.Errorf("id(a0) = %d", id)
	}
}

func TestSegmentRoundTrip(t *testing.T) {
	to := addr(9)
	u := uint256.NewInt
	roundVal := new(uint256.Int).Mul(uint256.NewInt(15), new(uint256.Int).Exp(u(10), u(17))) // 1.5 ETH
	oddVal := uint256.NewInt(1234567890123)

	blocks := []F2Block{
		{
			Txs: []F2Tx{
				// legacy create, zero value
				{Type: 0, From: addr(1), To: nil, Nonce: 0, Gas: 21000, Value: u(0), GasFeeCap: u(20_000_000_000)},
				// legacy transfer, round value
				{Type: 0, From: addr(2), To: &to, Nonce: 7, Gas: 21000, Value: roundVal, GasFeeCap: u(1)},
			},
			Withdrawals: []F2Withdrawal{{Index: 100, Validator: 5, Address: addr(7), Amount: 32_000_000_000}},
		},
		{
			Txs: []F2Tx{
				// dynamic-fee + access list + odd value + calldata
				{
					Type: 2, From: addr(2), To: &to, Nonce: 8, Gas: 50000,
					Value: oddVal, GasFeeCap: u(30_000_000_000), GasTipCap: u(2_000_000_000),
					Data: []byte{0xde, 0xad, 0xbe, 0xef},
					Access: []F2AccessTuple{
						{Address: addr(3), StorageKeys: [][32]byte{{1}, {2, 3}}},
					},
				},
				// blob tx (type 3): blobFeeCap + versioned hashes
				{
					Type: 3, From: addr(4), To: &to, Nonce: 1, Gas: 21000,
					Value: u(0), GasFeeCap: u(50), GasTipCap: u(3),
					BlobFeeCap: u(7), BlobHashes: [][32]byte{{0x01, 0x11}, {0x01, 0x22}},
				},
				// set-code tx (type 4): auth list
				{
					Type: 4, From: addr(5), To: &to, Nonce: 2, Gas: 60000,
					Value: u(0), GasFeeCap: u(40), GasTipCap: u(4),
					AuthList: []F2Auth{
						{ChainID: u(1), Address: addr(6), Nonce: 9, V: u(1), R: u(123456), S: u(654321)},
					},
				},
			},
		},
	}

	dict := NewAddrDict()
	raw := EncodeSegment(blocks, dict)
	got, err := DecodeSegment(raw, dict)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(blocks) {
		t.Fatalf("block count %d != %d", len(got), len(blocks))
	}
	for bi := range blocks {
		if len(got[bi].Txs) != len(blocks[bi].Txs) {
			t.Fatalf("block %d tx count", bi)
		}
		if len(got[bi].Withdrawals) != len(blocks[bi].Withdrawals) {
			t.Fatalf("block %d wd count", bi)
		}
		for ti := range blocks[bi].Txs {
			w, g := blocks[bi].Txs[ti], got[bi].Txs[ti]
			if g.Type != w.Type || g.From != w.From || g.Nonce != w.Nonce || g.Gas != w.Gas {
				t.Errorf("b%d t%d scalar mismatch: %+v vs %+v", bi, ti, g, w)
			}
			if (g.To == nil) != (w.To == nil) || (w.To != nil && *g.To != *w.To) {
				t.Errorf("b%d t%d To mismatch", bi, ti)
			}
			if g.Value.Cmp(w.Value) != 0 || g.GasFeeCap.Cmp(w.GasFeeCap) != 0 {
				t.Errorf("b%d t%d value/cap mismatch: val %s vs %s", bi, ti, g.Value, w.Value)
			}
			if w.Type >= 2 && g.GasTipCap.Cmp(w.GasTipCap) != 0 {
				t.Errorf("b%d t%d tip mismatch", bi, ti)
			}
			if !bytes.Equal(g.Data, w.Data) {
				t.Errorf("b%d t%d data mismatch", bi, ti)
			}
			if len(g.Access) != len(w.Access) {
				t.Errorf("b%d t%d access len", bi, ti)
			} else {
				for ai := range w.Access {
					if g.Access[ai].Address != w.Access[ai].Address || len(g.Access[ai].StorageKeys) != len(w.Access[ai].StorageKeys) {
						t.Errorf("b%d t%d access %d mismatch", bi, ti, ai)
					}
				}
			}
			if w.Type == 3 {
				if g.BlobFeeCap == nil || g.BlobFeeCap.Cmp(w.BlobFeeCap) != 0 || len(g.BlobHashes) != len(w.BlobHashes) {
					t.Errorf("b%d t%d blob mismatch: feecap=%v hashes=%d", bi, ti, g.BlobFeeCap, len(g.BlobHashes))
				} else if len(w.BlobHashes) > 0 && g.BlobHashes[0] != w.BlobHashes[0] {
					t.Errorf("b%d t%d blob hash[0] mismatch", bi, ti)
				}
			}
			if w.Type == 4 {
				if len(g.AuthList) != len(w.AuthList) {
					t.Errorf("b%d t%d authlist len %d != %d", bi, ti, len(g.AuthList), len(w.AuthList))
				} else if len(w.AuthList) > 0 {
					ga, wa := g.AuthList[0], w.AuthList[0]
					if ga.Address != wa.Address || ga.Nonce != wa.Nonce || ga.R.Cmp(wa.R) != 0 || ga.S.Cmp(wa.S) != 0 {
						t.Errorf("b%d t%d auth mismatch", bi, ti)
					}
				}
			}
		}
		for wi := range blocks[bi].Withdrawals {
			if got[bi].Withdrawals[wi] != blocks[bi].Withdrawals[wi] {
				t.Errorf("b%d wd %d mismatch", bi, wi)
			}
		}
	}
}

func TestValueSciRoundTrip(t *testing.T) {
	u := uint256.NewInt
	cases := []*uint256.Int{
		u(0), u(1), u(10), u(1000000),
		new(uint256.Int).Mul(u(15), new(uint256.Int).Exp(u(10), u(17))), // 1.5 ETH
		u(1234567890123),                   // odd
		new(uint256.Int).Exp(u(2), u(200)), // large
	}
	for _, v := range cases {
		b := encValueSci(nil, v)
		r := &reader{b: b}
		got, err := r.valueSci()
		if err != nil {
			t.Fatalf("decode %s: %v", v, err)
		}
		if got.Cmp(v) != 0 {
			t.Errorf("sci roundtrip: got %s want %s", got, v)
		}
	}
}
