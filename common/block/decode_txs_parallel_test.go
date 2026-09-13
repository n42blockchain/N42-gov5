// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"

	"github.com/holiman/uint256"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/rlp"
)

// The parallel decode yields the same transactions as the serial loop, and
// on corrupt input the error of the lowest failing index.
func TestDecodeBlockTxsParallelMatchesSerial(t *testing.T) {
	txs := benchTxs(parallelTxDecodeMin + 1000)
	data := make([][]byte, len(txs))
	for i, tx := range txs {
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		data[i] = enc
	}
	got, err := decodeBlockTxs(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range txs {
		if got[i].Hash() != txs[i].Hash() {
			t.Fatalf("tx %d: hash %x != %x", i, got[i].Hash(), txs[i].Hash())
		}
	}
	// Corrupt two entries; the reported error must be the lower index's.
	bad := make([][]byte, len(data))
	copy(bad, data)
	bad[2500] = []byte{0xc0}
	bad[100] = []byte{0xff, 0x01}
	_, perr := decodeBlockTxs(bad)
	_, serr := transaction.DecodeEthereumTransaction(bad[100])
	if perr == nil || serr == nil || perr.Error() != serr.Error() {
		t.Fatalf("parallel error %v, want the index-100 error %v", perr, serr)
	}
}

// A block encoded with the cached transaction encodings round-trips to the
// same header hash and transaction hashes.
func TestBlockRLPRoundTripWithCachedEncodings(t *testing.T) {
	txs := benchTxs(parallelTxDecodeMin + 17)
	for _, tx := range txs[:10] {
		if _, err := tx.EthEncoded(); err != nil { // warm half the caches
			t.Fatal(err)
		}
	}
	h := &Header{Number: benchNumber(7), GasLimit: 1 << 40}
	b := NewBlock(h, txs).(*Block)
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, b); err != nil {
		t.Fatal(err)
	}
	var dec Block
	if err := rlp.DecodeBytes(buf.Bytes(), &dec); err != nil {
		t.Fatal(err)
	}
	if dec.Hash() != b.Hash() {
		t.Fatalf("header hash %x != %x", dec.Hash(), b.Hash())
	}
	dtx := dec.Transactions()
	if len(dtx) != len(txs) {
		t.Fatalf("decoded %d txs, want %d", len(dtx), len(txs))
	}
	for i := range txs {
		if dtx[i].Hash() != txs[i].Hash() {
			t.Fatalf("tx %d hash mismatch after round trip", i)
		}
	}
}

func benchNumber(n uint64) *uint256.Int { return uint256.NewInt(n) }
