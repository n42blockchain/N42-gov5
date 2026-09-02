// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func sampleTxs(t *testing.T) []*Transaction {
	t.Helper()
	to := types.HexToAddress("0x2000000000000000000000000000000000000001")
	v, r, s := uint256.NewInt(27), uint256.NewInt(0x1234), uint256.NewInt(0x5678)
	mk := func(inner TxData) *Transaction {
		tx := NewTx(inner)
		tx.inner.setSignatureValues(tx.inner.chainID(), v, r, s)
		return tx
	}
	return []*Transaction{
		mk(&LegacyTx{Nonce: 7, GasPrice: uint256.NewInt(1_000_000_007), Gas: 21000,
			To: &to, Value: uint256.NewInt(1)}),
		mk(&LegacyTx{Nonce: 0, GasPrice: uint256.NewInt(1), Gas: 90000,
			To: nil, Value: uint256.NewInt(0), Data: []byte{0x60, 0x00}}),
		mk(&AccessListTx{ChainID: uint256.NewInt(94), Nonce: 3,
			GasPrice: uint256.NewInt(7), Gas: 21000, To: &to, Value: uint256.NewInt(2)}),
		mk(&DynamicFeeTx{ChainID: uint256.NewInt(94), Nonce: 11,
			GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(9),
			Gas: 21000, To: &to, Value: uint256.NewInt(3)}),
	}
}

// TestEthEncodedCacheMatchesEncoder is the invariant the transactions root now
// rests on: the bytes a decoded transaction carries are byte-identical to what
// EncodeEthereumTransaction would produce for it. Before the cache, the root
// re-derived them; now it loads them, and a difference between the two would
// change a block's transactions root -- a consensus divergence, not a
// performance bug.
func TestEthEncodedCacheMatchesEncoder(t *testing.T) {
	for i, tx := range sampleTxs(t) {
		want, err := EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatalf("tx %d: encode: %v", i, err)
		}
		decoded, err := DecodeEthereumTransaction(want)
		if err != nil {
			t.Fatalf("tx %d: decode: %v", i, err)
		}
		// The decoder cached its input; check it against a fresh encode of the
		// decoded transaction, which is what the old root computation used.
		fresh, err := EncodeEthereumTransaction(decoded)
		if err != nil {
			t.Fatalf("tx %d: re-encode: %v", i, err)
		}
		cached, err := decoded.EthEncoded()
		if err != nil {
			t.Fatalf("tx %d: EthEncoded: %v", i, err)
		}
		if !bytes.Equal(cached, fresh) {
			t.Fatalf("tx %d: cached %x != re-encoded %x", i, cached, fresh)
		}
		if !bytes.Equal(cached, want) {
			t.Fatalf("tx %d: cached %x != original %x", i, cached, want)
		}
		if n, err := decoded.EncodedSize(); err != nil || n != len(want) {
			t.Fatalf("tx %d: EncodedSize %d/%v, want %d", i, n, err, len(want))
		}
	}
}

// TestDecodeRejectsNonCanonicalRLP is the other half. The cache is only safe
// because a non-canonical encoding cannot be decoded: if one could, the cache
// would carry the received bytes while the old code carried their canonical
// re-encoding, and the two roots would differ.
func TestDecodeRejectsNonCanonicalRLP(t *testing.T) {
	tx := sampleTxs(t)[0]
	canonical, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	// A leading zero byte on the nonce is the classic non-canonical integer.
	// Rather than hand-craft the RLP, mutate the payload in the ways a peer
	// could and require every one of them to be refused or to round-trip
	// unchanged -- never to decode into something that re-encodes differently.
	for i := 0; i < len(canonical); i++ {
		for _, delta := range []byte{0x01, 0x80} {
			bad := append([]byte(nil), canonical...)
			bad[i] += delta
			decoded, err := DecodeEthereumTransaction(bad)
			if err != nil {
				continue // refused, which is fine
			}
			fresh, err := EncodeEthereumTransaction(decoded)
			if err != nil {
				continue
			}
			if !bytes.Equal(fresh, bad) {
				t.Fatalf("byte %d +%#x decoded but re-encodes differently:\n  in  %x\n  out %x",
					i, delta, bad, fresh)
			}
		}
	}
}

// TestEthEncodedCacheIsNotAliasedToCallerBuffer pins that the decoder copies:
// a caller that reuses its read buffer must not be able to corrupt a
// transaction's cached encoding, which would corrupt a block's root.
func TestEthEncodedCacheIsNotAliasedToCallerBuffer(t *testing.T) {
	tx := sampleTxs(t)[0]
	enc, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	buf := append([]byte(nil), enc...)
	decoded, err := DecodeEthereumTransaction(buf)
	if err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		buf[i] = 0xAA
	}
	cached, err := decoded.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cached, enc) {
		t.Fatalf("cached encoding follows the caller's buffer: %x", cached)
	}
}
