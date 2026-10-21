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
	"encoding/binary"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// refHashV2 is the v2 definition written out longhand.
func refHashV2(raw []byte, txs [][]byte) types.Hash {
	pre := append([]byte(DomainV2), raw[:HeaderSize]...)
	for _, tx := range txs {
		h := crypto.Keccak256Hash(tx)
		pre = append(pre, h[:]...)
	}
	return crypto.Keccak256Hash(pre)
}

// TestV2LeavesV1Alone: with v2 off the golden frame is unchanged;
// with v2 on the same frame's bytes differ only in the signature.
func TestV2LeavesV1Alone(t *testing.T) {
	defer SetHashV2ForTest(false)()
	TestV1Golden(t)
	v1 := goldenFrame(t)
	SetHashV2ForTest(true)
	v2 := goldenFrame(t)
	n := len(v1) - SigSize
	if len(v2) != len(v1) || !bytes.Equal(v1[:n], v2[:n]) {
		t.Fatal("v2 changed header or payload bytes")
	}
	if bytes.Equal(v1[n:], v2[n:]) {
		t.Fatal("v2 signed the same digest as v1")
	}
}

func TestV2RoundTrip(t *testing.T) {
	defer SetHashV2ForTest(true)()
	key, _ := crypto.GenerateKey()
	// Entries on both sides of the one-block limit and an odd count, so the
	// four-way batch, its tail and the long-entry path all run.
	txs := testTxs(37)
	txs[5] = bytes.Repeat([]byte{7}, 300)
	txs[6] = bytes.Repeat([]byte{8}, crypto.Keccak256OneBlockMax)
	txs[7] = bytes.Repeat([]byte{9}, crypto.Keccak256OneBlockMax-1)
	raw, hash, err := Encode(key, 9, TxTypeEthereum, txs)
	if err != nil {
		t.Fatal(err)
	}
	if want := refHashV2(raw, txs); hash != want {
		t.Fatalf("encode hash %x, want %x", hash, want)
	}
	f, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Verify()
	if err != nil || got != hash {
		t.Fatalf("verify: %x %v", got, err)
	}
	if len(f.TxHashes) != len(txs) {
		t.Fatalf("%d entry hashes", len(f.TxHashes))
	}
	for i, tx := range txs {
		if f.TxHashes[i] != crypto.Keccak256Hash(tx) {
			t.Fatalf("entry hash %d", i)
		}
	}
	if h, err := HashOf(raw); err != nil || h != hash {
		t.Fatalf("HashOf: %x %v", h, err)
	}
	// A frame verified under v1 does not verify under v2 and vice versa.
	SetHashV2ForTest(false)
	f, _ = Decode(raw)
	if h, err := f.Verify(); err == nil || h == hash {
		t.Fatal("v2 frame verified under v1")
	}
	// Empty frame.
	SetHashV2ForTest(true)
	raw, hash, err = Encode(key, 10, TxTypeEthereum, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ = Decode(raw)
	if h, err := f.Verify(); err != nil || h != hash || h != refHashV2(raw, nil) {
		t.Fatalf("empty frame: %v", err)
	}
}

func TestV2Tamper(t *testing.T) {
	defer SetHashV2ForTest(true)()
	key, _ := crypto.GenerateKey()
	txs := testTxs(9)
	raw, hash, err := Encode(key, 3, TxTypeEthereum, txs)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(name string, b []byte) {
		t.Helper()
		f, err := Decode(b)
		if err != nil {
			return // rejected outright
		}
		h, err := f.Verify()
		if err == nil {
			t.Fatalf("%s: verified", name)
		}
		if h == hash {
			t.Fatalf("%s: same hash", name)
		}
	}

	// Every transaction byte flipped, one at a time.
	off := HeaderSize
	for _, tx := range txs {
		off += txLenSize
		for j := range tx {
			b := append([]byte(nil), raw...)
			b[off+j] ^= 0x40
			verify("tx byte", b)
		}
		off += len(tx)
	}
	// Every header byte flipped (version, type, gateway, seq, count, length).
	for j := 0; j < HeaderSize; j++ {
		b := append([]byte(nil), raw...)
		b[j] ^= 1
		verify("header byte", b)
	}

	// Reordered transactions: a different hash, and the original signature
	// does not carry over.
	sw := append([][]byte(nil), txs...)
	sw[0], sw[1] = sw[1], sw[0]
	raw2, hash2, _ := Encode(key, 3, TxTypeEthereum, sw)
	if hash2 == hash {
		t.Fatal("reorder: same hash")
	}
	b := append([]byte(nil), raw2[:len(raw2)-SigSize]...)
	b = append(b, raw[len(raw)-SigSize:]...)
	verify("reorder", b)

	// Entry boundaries moved (two entries merged into one): count and
	// hashes change.
	merged := append([][]byte{append(append([]byte(nil), txs[0]...), txs[1]...)}, txs[2:]...)
	if _, h, _ := Encode(key, 3, TxTypeEthereum, merged); h == hash {
		t.Fatal("merge: same hash")
	}

	// Count mismatch: one transaction dropped with the header kept.
	_, hash3, _ := Encode(key, 3, TxTypeEthereum, txs[:8])
	if hash3 == hash {
		t.Fatal("dropped tx: same hash")
	}
	b = append([]byte(nil), raw...)
	binary.LittleEndian.PutUint16(b[30:32], 8)
	if _, err := Decode(b); !errors.Is(err, ErrPayloadLayout) {
		t.Fatalf("count mismatch decoded: %v", err)
	}

	// Trailing payload bytes covered by no entry: the payload length is
	// bumped and a byte inserted before the signature. Decode rejects it,
	// and so does the v2 hash walk on its own (a Frame built by hand).
	pl := len(raw) - SigSize
	b = append(append(append([]byte(nil), raw[:pl]...), 0xAA), raw[pl:]...)
	binary.LittleEndian.PutUint32(b[32:36], binary.LittleEndian.Uint32(raw[32:36])+1)
	if _, err := Decode(b); !errors.Is(err, ErrPayloadLayout) {
		t.Fatalf("trailing payload decoded: %v", err)
	}
	hdr, _ := ParseHeader(b)
	hand := &Frame{Header: hdr, Raw: b, Payload: b[HeaderSize : HeaderSize+int(hdr.PayloadLen)], Sig: b[HeaderSize+int(hdr.PayloadLen):]}
	if _, err := hand.Verify(); !errors.Is(err, ErrPayloadLayout) {
		t.Fatalf("trailing payload verified: %v", err)
	}
	if _, err := HashOf(b); err == nil {
		t.Fatal("HashOf hashed a frame with trailing payload")
	}
	// Bytes after the signature.
	if _, err := Decode(append(append([]byte(nil), raw...), 0)); !errors.Is(err, ErrTrailingBytes) {
		t.Fatalf("trailing: %v", err)
	}
}

func BenchmarkAdmitV1(b *testing.B) { benchAdmit(b, false) }
func BenchmarkAdmitV2(b *testing.B) { benchAdmit(b, true) }

// benchAdmit is one frame's admission: Decode and Verify.
func benchAdmit(b *testing.B, v2 bool) {
	defer SetHashV2ForTest(v2)()
	key, _ := crypto.GenerateKey()
	raw, _, _ := Encode(key, 1, TxTypeEthereum, testTxs(MaxTxs))
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
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
