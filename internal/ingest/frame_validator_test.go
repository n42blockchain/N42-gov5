// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ingest

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestFrameValidatorSigners: with the validator set wired, a frame signed
// by a current validator (a leader's local frame of pool transactions) is
// admitted over FRM1 and FRQ1 with no gateway allowlist at all; a frame
// signed by an unknown key is refused on both paths, and a frame that names
// a validator but carries another key's signature still fails the
// signature check.
func TestFrameValidatorSigners(t *testing.T) {
	val, _ := crypto.GenerateKey()
	stranger, _ := crypto.GenerateKey()
	valAddr := crypto.PubkeyToAddress(val.PublicKey)
	isValidator := func(a types.Address) bool { return a == valAddr }
	txs, _ := signedTxs(t, 3, 1)

	srv := startFrameServer(t, nil, FrameConfig{IsValidator: isValidator, Store: store.New(1 << 20)})
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	raw, h, _ := frame.Encode(val, 1, frame.TxTypeEthereum, txs)
	if status, got := sendFrame(t, conn, raw); status != FrameAccepted || got != h {
		t.Fatalf("validator frame: status %d", status)
	}
	raw, _, _ = frame.Encode(stranger, 1, frame.TxTypeEthereum, txs)
	if status, _ := sendFrame(t, conn, raw); status != FrameUnknownGateway {
		t.Fatalf("unknown key: status %d", status)
	}
	raw, _, _ = frame.Encode(stranger, 2, frame.TxTypeEthereum, txs)
	copy(raw[2:22], valAddr[:])
	if status, _ := sendFrame(t, conn, raw); status != FrameBadSignature {
		t.Fatalf("forged validator frame: status %d", status)
	}

	// FRQ1: the peer serves both; the puller admits only the validator's.
	valRaw, valHash, _ := frame.Encode(val, 7, frame.TxTypeEthereum, txs[:2])
	strRaw, strHash, _ := frame.Encode(stranger, 7, frame.TxTypeEthereum, txs[:2])
	peerStore := store.New(1 << 20)
	peerStore.Put(valHash, valRaw)
	peerStore.Put(strHash, strRaw)
	gwKey, _ := crypto.GenerateKey()
	peer := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{crypto.PubkeyToAddress(gwKey.PublicKey)}, Store: peerStore})
	local := store.New(1 << 20)
	me := startFrameServer(t, nil, FrameConfig{
		Gateways:    []types.Address{crypto.PubkeyToAddress(gwKey.PublicKey)},
		IsValidator: isValidator,
		Store:       local,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := me.PullFrames(ctx, []string{peer.Addr()}, []types.Hash{valHash, strHash})
	if len(got) != 1 || string(got[valHash]) != string(valRaw) || !local.Has(valHash) || local.Has(strHash) {
		t.Fatalf("pull admitted %d frames (validator %v, stranger %v)", len(got), local.Has(valHash), local.Has(strHash))
	}

	// Neither gateways nor a validator set: refused.
	if err := NewServer("127.0.0.1:0", &mockTxPool{}, 1<<20, 1<<20).EnableFrames(FrameConfig{}); err == nil {
		t.Fatal("frame ingest enabled with no signer source")
	}
}
