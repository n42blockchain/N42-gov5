// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// signedRawTxs builds n signed transactions and returns their standard
// encodings plus their hashes.
func signedRawTxs(t *testing.T, n int) ([][]byte, []types.Hash) {
	t.Helper()
	chainID := uint256.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID.ToBig())
	raws := make([][]byte, n)
	hashes := make([]types.Hash, n)
	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		to := types.Address{byte(i), 0x22}
		tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1e10),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(uint64(i) + 1),
			Data:      bytes.Repeat([]byte{0xab}, i%40),
		})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		raws[i] = raw
		hashes[i] = tx.Hash()
	}
	return raws, hashes
}

// TestTxBatchRoundTrip: encode a batch, decode it, and get the same
// transactions back in order.
func TestTxBatchRoundTrip(t *testing.T) {
	raws, hashes := signedRawTxs(t, 17)
	payload, err := encodeTxBatch(raws)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if payload[0] != txBatchMarker {
		t.Fatalf("payload starts with %#x, want marker %#x", payload[0], txBatchMarker)
	}
	txs, err := decodeTxBatch(payload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(txs) != len(raws) {
		t.Fatalf("decoded %d txs, want %d", len(txs), len(raws))
	}
	for i, tx := range txs {
		if tx.Hash() != hashes[i] {
			t.Fatalf("tx %d: hash mismatch after round trip", i)
		}
	}
}

// TestTxBatchMarkerDistinguishesSingles: every single-transaction encoding
// must be recognised as NOT a batch, so the subscriber's fallback path fires.
func TestTxBatchMarkerDistinguishesSingles(t *testing.T) {
	raws, _ := signedRawTxs(t, 3)
	for i, raw := range raws {
		if raw[0] == txBatchMarker {
			t.Fatalf("tx %d: single encoding starts with the batch marker", i)
		}
		if _, err := decodeTxBatch(raw); !errors.Is(err, errNotABatch) {
			t.Fatalf("tx %d: single encoding decoded as a batch (err=%v)", i, err)
		}
	}
	// A legacy transaction's first byte is >= 0xc0 by RLP construction; typed
	// envelopes in use start 0x01-0x04. Document the invariant the marker
	// relies on.
	if txBatchMarker >= 0xc0 || (txBatchMarker >= 0x01 && txBatchMarker <= 0x04) {
		t.Fatalf("marker %#x collides with a transaction encoding", txBatchMarker)
	}
}

// TestTxBatchRejectsMalformed: garbage after the marker is an error, not a
// silent fallback — nothing legitimate starts with the marker byte.
func TestTxBatchRejectsMalformed(t *testing.T) {
	if _, err := decodeTxBatch([]byte{txBatchMarker, 0xff, 0x00}); err == nil || errors.Is(err, errNotABatch) {
		t.Fatalf("malformed batch: got %v, want a decode error", err)
	}
	if _, err := decodeTxBatch(nil); !errors.Is(err, errNotABatch) {
		t.Fatalf("nil payload: got %v, want errNotABatch", err)
	}
	// An empty list is refused: a publisher never sends one.
	empty, err := encodeTxBatch(nil)
	if err != nil {
		t.Fatalf("encode empty: %v", err)
	}
	if _, err := decodeTxBatch(empty); err == nil {
		t.Fatal("empty batch decoded")
	}
}

// TestTxBatchCap: a batch above txBatchMaxTxs is refused on decode, bounding
// what one gossip message can make the pool chew.
func TestTxBatchCap(t *testing.T) {
	raws, _ := signedRawTxs(t, 2)
	over := make([][]byte, txBatchMaxTxs+1)
	for i := range over {
		over[i] = raws[i%2]
	}
	payload, err := encodeTxBatch(over)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := decodeTxBatch(payload); err == nil {
		t.Fatal("oversized batch decoded")
	}
}
