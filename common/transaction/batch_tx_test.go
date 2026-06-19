// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// buildBatch makes an N-transfer batch (To set, Value=i+1, Gas=21000, no data)
// signed by one key, and returns the batch + the signer address.
func buildBatch(t *testing.T, n int) (*CompressedBatchTx, types.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := crypto.PubkeyToAddress(key.PublicKey)

	tx := &CompressedBatchTx{
		ChainID:   uint256.NewInt(86),
		BaseNonce: 100,
		GasTipCap: uint256.NewInt(1_000_000_000),
		GasFeeCap: uint256.NewInt(20_000_000_000),
		To:        make([]*types.Address, n),
		Value:     make([]*uint256.Int, n),
		Gas:       make([]uint64, n),
		Data:      make([][]byte, n),
	}
	for i := 0; i < n; i++ {
		var to types.Address
		to[0], to[19] = byte(i), byte(i) // arbitrary distinct recipient
		tx.To[i] = &to
		tx.Value[i] = uint256.NewInt(uint64(i + 1))
		tx.Gas[i] = 21000
		tx.Data[i] = nil
	}
	if err := tx.Sign(key); err != nil {
		t.Fatal(err)
	}
	return tx, signer
}

func TestBatchTxSignRecoverRoundtrip(t *testing.T) {
	tx, signer := buildBatch(t, 16)

	// Recover the single signer.
	got, err := tx.RecoverSender()
	if err != nil {
		t.Fatal(err)
	}
	if got != signer {
		t.Fatalf("RecoverSender = %x, want %x", got, signer)
	}

	// Encode → decode round-trip preserves every field + signature.
	enc := tx.EncodeBatch()
	dec, err := DecodeBatch(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec.BaseNonce != tx.BaseNonce || dec.SubCount() != tx.SubCount() {
		t.Fatalf("roundtrip header mismatch")
	}
	if dec.ChainID.Cmp(tx.ChainID) != 0 || dec.GasFeeCap.Cmp(tx.GasFeeCap) != 0 {
		t.Fatalf("roundtrip caps mismatch")
	}
	for i := 0; i < tx.SubCount(); i++ {
		if *dec.To[i] != *tx.To[i] || dec.Value[i].Cmp(tx.Value[i]) != 0 || dec.Gas[i] != tx.Gas[i] {
			t.Fatalf("roundtrip sub-tx %d mismatch", i)
		}
	}
	// Decoded batch recovers the same signer (signature survived the codec).
	got2, err := dec.RecoverSender()
	if err != nil || got2 != signer {
		t.Fatalf("decoded RecoverSender = %x (err %v), want %x", got2, err, signer)
	}
}

func TestBatchTxExpand(t *testing.T) {
	const n = 16
	tx, signer := buildBatch(t, n)

	subs, err := tx.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != n {
		t.Fatalf("Expand returned %d, want %d", len(subs), n)
	}
	for i, s := range subs {
		if s.Type() != DynamicFeeTxType {
			t.Fatalf("sub %d type = %d, want DynamicFee", i, s.Type())
		}
		if s.Nonce() != tx.BaseNonce+uint64(i) {
			t.Fatalf("sub %d nonce = %d, want %d", i, s.Nonce(), tx.BaseNonce+uint64(i))
		}
		from := s.From()
		if from == nil || *from != signer {
			t.Fatalf("sub %d From = %v, want %x", i, from, signer)
		}
		if s.To() == nil || *s.To() != *tx.To[i] {
			t.Fatalf("sub %d To mismatch", i)
		}
		if s.Value().Cmp(tx.Value[i]) != 0 || s.Gas() != tx.Gas[i] {
			t.Fatalf("sub %d value/gas mismatch", i)
		}
	}
}

func TestBatchTxTamperDetected(t *testing.T) {
	tx, signer := buildBatch(t, 8)
	// Tamper a value AFTER signing → recovered sender must differ (sig no longer
	// matches the bundle).
	tx.Value[3] = uint256.NewInt(999999)
	got, err := tx.RecoverSender()
	if err == nil && got == signer {
		t.Fatal("tampered batch recovered the original signer — signature does not bind the body")
	}
}

func TestBatchTxCompression(t *testing.T) {
	const n = 10000
	tx, _ := buildBatch(t, n)
	enc := tx.EncodeBatch()
	perTx := float64(len(enc)) / float64(n)
	t.Logf("batch of %d transfers: %d bytes total, %.2f bytes/tx (raw, pre-zstd)", n, len(enc), perTx)
	// A standalone signed tx is ~100+ bytes (65B sig + to + value + nonce + fees +
	// chainid + RLP overhead). The batch amortises the one signature, drops
	// per-tx from/nonce, and shares fees → must be far smaller per tx.
	if perTx > 40 {
		t.Fatalf("batch bytes/tx = %.2f, expected < 40 (transfer batch)", perTx)
	}
}
