// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// buildAggBatch makes an N-sub-tx aggregate batch from N distinct BLS senders,
// returns the batch + a resolver (sender address -> registered BLS pubkey).
func buildAggBatch(t *testing.T, n int) (*AggregateBatchTx, func(types.Address) (blscommon.PublicKey, bool)) {
	t.Helper()
	keys := make([]blscommon.SecretKey, n)
	reg := make(map[types.Address]blscommon.PublicKey, n)
	tx := &AggregateBatchTx{
		ChainID: uint256.NewInt(86),
		GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(2e10),
		Sender: make([]*types.Address, n), Nonce: make([]uint64, n),
		To: make([]*types.Address, n), Value: make([]*uint256.Int, n),
		Gas: make([]uint64, n), Data: make([][]byte, n),
	}
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = sk
		reg[BLSAddress(sk.PublicKey())] = sk.PublicKey()

		var to types.Address
		to[0], to[19] = byte(i), byte(i>>8)
		tx.To[i] = &to
		tx.Nonce[i] = uint64(i) // each sender's own nonce (independent)
		tx.Value[i] = uint256.NewInt(uint64(i + 1))
		tx.Gas[i] = 21000
	}
	if err := tx.AggregateBatchSign(keys); err != nil {
		t.Fatal(err)
	}
	resolve := func(a types.Address) (blscommon.PublicKey, bool) { pk, ok := reg[a]; return pk, ok }
	return tx, resolve
}

func TestAggBatchVerifyAndExpand(t *testing.T) {
	const n = 64
	tx, resolve := buildAggBatch(t, n)

	// One aggregate signature (96 bytes) authenticates all N distinct senders.
	if len(tx.AggSig) != 96 {
		t.Fatalf("AggSig = %d bytes, want 96", len(tx.AggSig))
	}
	ok, err := tx.Verify(resolve)
	if err != nil || !ok {
		t.Fatalf("Verify = %v (err %v), want true", ok, err)
	}

	// Encode -> decode -> verify again (sig + columns survive the codec).
	dec, err := DecodeAggregateBatch(tx.EncodeBatch())
	if err != nil {
		t.Fatal(err)
	}
	if dec.SubCount() != n {
		t.Fatalf("decoded SubCount = %d, want %d", dec.SubCount(), n)
	}
	ok, err = dec.Verify(resolve)
	if err != nil || !ok {
		t.Fatalf("decoded Verify = %v (err %v), want true", ok, err)
	}

	// Expand -> N DynamicFee txs, each its own sender + nonce.
	subs, err := dec.Expand()
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range subs {
		if s.Type() != DynamicFeeTxType {
			t.Fatalf("sub %d not DynamicFee", i)
		}
		if from := s.From(); from == nil || *from != *tx.Sender[i] {
			t.Fatalf("sub %d From mismatch", i)
		}
		if s.Nonce() != tx.Nonce[i] {
			t.Fatalf("sub %d nonce %d, want %d", i, s.Nonce(), tx.Nonce[i])
		}
	}
}

func TestAggBatchTamperFailsVerify(t *testing.T) {
	tx, resolve := buildAggBatch(t, 16)
	// Tamper one sub-tx value AFTER aggregation → aggregate verify must fail.
	tx.Value[5] = uint256.NewInt(123456789)
	ok, err := tx.Verify(resolve)
	if err == nil && ok {
		t.Fatal("tampered aggregate batch still verified")
	}
}

func TestAggBatchUnknownSenderRejected(t *testing.T) {
	tx, _ := buildAggBatch(t, 8)
	// A resolver that knows nobody → Verify must error (no registered key).
	empty := func(types.Address) (blscommon.PublicKey, bool) { return nil, false }
	if _, err := tx.Verify(empty); err == nil {
		t.Fatal("expected error for unregistered sender")
	}
}

func TestAggBatchCompression(t *testing.T) {
	const n = 10000
	tx, _ := buildAggBatch(t, n)
	enc := tx.EncodeBatch()
	t.Logf("aggregate batch of %d cross-account txs: %d bytes, %.2f bytes/tx (raw, pre-zstd); one 96B BLS sig",
		n, len(enc), float64(len(enc))/n)
}
