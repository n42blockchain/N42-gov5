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

package frame

import (
	"bytes"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func testTxs(n int) [][]byte {
	txs := make([][]byte, n)
	for i := range txs {
		tx := make([]byte, 100+i%17)
		for j := range tx {
			tx[j] = byte(i + j)
		}
		txs[i] = tx
	}
	return txs
}

func TestRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	txs := testTxs(MaxTxs)
	raw, hash, err := Encode(key, 42, TxTypeEthereum, txs)
	if err != nil {
		t.Fatal(err)
	}
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Seq != 42 || int(f.TxCount) != MaxTxs || f.Gateway != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatalf("header mismatch: %+v", f.Header)
	}
	if f.Hash() != hash {
		t.Fatal("hash mismatch")
	}
	got, err := f.Verify()
	if err != nil || got != hash {
		t.Fatalf("verify: %v", err)
	}
	out := f.Txs()
	if len(out) != len(txs) {
		t.Fatalf("tx count %d", len(out))
	}
	for i := range txs {
		if !bytes.Equal(out[i], txs[i]) {
			t.Fatalf("tx %d differs", i)
		}
	}
}

func TestSignatureReject(t *testing.T) {
	key, _ := crypto.GenerateKey()
	other, _ := crypto.GenerateKey()
	raw, _, err := Encode(key, 1, TxTypeEthereum, testTxs(3))
	if err != nil {
		t.Fatal(err)
	}

	// A payload byte flipped: the hash changes, the recovered key does not
	// match the gateway.
	bad := append([]byte(nil), raw...)
	bad[HeaderSize+5] ^= 1
	f, err := Decode(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Verify(); err == nil {
		t.Fatal("tampered payload verified")
	}

	// Gateway field rewritten to another address.
	bad = append([]byte(nil), raw...)
	a := crypto.PubkeyToAddress(other.PublicKey)
	copy(bad[2:22], a[:])
	f, _ = Decode(bad)
	if _, err := f.Verify(); !errors.Is(err, ErrWrongGateway) && !errors.Is(err, ErrBadSignature) {
		t.Fatalf("rewritten gateway: %v", err)
	}

	// Garbage signature.
	bad = append([]byte(nil), raw...)
	for i := len(bad) - SigSize; i < len(bad); i++ {
		bad[i] = 0xFF
	}
	f, _ = Decode(bad)
	if _, err := f.Verify(); err == nil {
		t.Fatal("garbage signature verified")
	}
}

func TestBounds(t *testing.T) {
	key, _ := crypto.GenerateKey()
	if _, _, err := Encode(key, 0, 0, testTxs(MaxTxs+1)); !errors.Is(err, ErrTooManyTxs) {
		t.Fatalf("too many: %v", err)
	}
	big := [][]byte{make([]byte, 60000)}
	many := make([][]byte, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, big[0])
	}
	if _, _, err := Encode(key, 0, 0, many); !errors.Is(err, ErrPayloadTooBig) {
		t.Fatalf("too big: %v", err)
	}
	if _, _, err := Encode(key, 0, 0, [][]byte{{}}); !errors.Is(err, ErrEmptyTx) {
		t.Fatalf("empty: %v", err)
	}
	raw, _, _ := Encode(key, 0, 0, testTxs(4))
	for n := 0; n < len(raw); n++ {
		if _, err := Decode(raw[:n]); err == nil {
			t.Fatalf("truncated at %d decoded", n)
		}
	}
	if _, err := Decode(append(raw, 0)); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("trailing: %v", err)
	}
	bad := append([]byte(nil), raw...)
	bad[0] = 9
	if _, err := Decode(bad); !errors.Is(err, ErrVersion) {
		t.Fatalf("version: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	key, _ := crypto.GenerateKey()
	raw, _, _ := Encode(key, 7, 0, testTxs(3))
	f.Add(raw)
	f.Add(raw[:HeaderSize])
	f.Add([]byte{1})
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := Decode(b)
		if err != nil {
			return
		}
		txs := fr.Txs()
		if len(txs) != int(fr.TxCount) {
			t.Fatalf("decoded %d txs, header says %d", len(txs), fr.TxCount)
		}
		_, _ = fr.Verify()
	})
}

func BenchmarkVerify(b *testing.B) {
	key, _ := crypto.GenerateKey()
	raw, _, _ := Encode(key, 1, 0, testTxs(MaxTxs))
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := Decode(raw)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := f.Verify(); err != nil {
			b.Fatal(err)
		}
	}
}
