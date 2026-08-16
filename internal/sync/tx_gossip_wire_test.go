// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/proto/types_pb"
)

// TestTransactionGossipWireRoundTrip walks a transaction through the exact
// framing the gossip mesh uses -- EncodeGossip over the raw carrier, then
// DecodeGossip back out -- and asserts the transaction that comes off the wire
// is the same one that went on.
//
// The unit round trip in common/transaction covers the encoding; this covers
// the wiring around it, which is where the switch away from SSZ-over-protobuf
// actually happens. It also records what the change buys, since the old format
// is still constructible from the same transaction.
func TestTransactionGossipWireRoundTrip(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := uint256.NewInt(94)
	to := types.Address{0x11, 0x22, 0x33}
	signer := transaction.LatestSignerForChainID(chainID.ToBig())

	tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     42,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1e10),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1e18),
		Data:      bytes.Repeat([]byte{0xcd}, 68),
	})
	if err != nil {
		t.Fatal(err)
	}

	rlpBytes, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Publisher side: exactly what BroadcastTransaction does.
	var wire bytes.Buffer
	enc := encoder.SszNetworkEncoder{}
	if _, err := enc.EncodeGossip(&wire, &rawSSZBytes{data: rlpBytes}); err != nil {
		t.Fatalf("EncodeGossip: %v", err)
	}

	// Subscriber side: exactly what decodePubsubMessage + txSubscriber do.
	raw := &rawSSZBytes{}
	if err := enc.DecodeGossip(wire.Bytes(), raw); err != nil {
		t.Fatalf("DecodeGossip: %v", err)
	}
	got, err := transaction.DecodeEthereumTransaction(raw.data)
	if err != nil {
		t.Fatalf("DecodeEthereumTransaction: %v", err)
	}

	if got.Hash() != tx.Hash() {
		t.Fatalf("hash changed across the wire: %s -> %s", tx.Hash(), got.Hash())
	}
	want, err := transaction.Sender(signer, tx)
	if err != nil {
		t.Fatal(err)
	}
	gotFrom, err := transaction.Sender(signer, got)
	if err != nil {
		t.Fatalf("sender not recoverable from the wire form: %v", err)
	}
	if gotFrom != want {
		t.Fatalf("sender changed across the wire: %s -> %s", want, gotFrom)
	}

	// What the old format would have cost for the same transaction.
	var oldWire bytes.Buffer
	pbTx := tx.ToProtoMessage().(*types_pb.Transaction)
	if _, err := enc.EncodeGossip(&oldWire, pbTx); err != nil {
		t.Fatalf("EncodeGossip(proto): %v", err)
	}
	if oldWire.Len() <= wire.Len() {
		t.Fatalf("RLP framing (%d B) is not smaller than SSZ-over-protobuf (%d B)", wire.Len(), oldWire.Len())
	}
	t.Logf("on the wire: RLP %d B vs SSZ-over-protobuf %d B (%.1f%% smaller, snappy-framed)",
		wire.Len(), oldWire.Len(), float64(oldWire.Len()-wire.Len())*100/float64(oldWire.Len()))
}
