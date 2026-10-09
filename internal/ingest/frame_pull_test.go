// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestFramePull: a node missing frames pulls them over FRQ1 from a peer
// that holds them in its store or only in its durable lookup; unknown
// hashes come back missing, and a frame from a gateway the puller does not
// allow is refused.
func TestFramePull(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	rogue, _ := crypto.GenerateKey()
	gwAddr := crypto.PubkeyToAddress(gw.PublicKey)
	txs, _ := signedTxs(t, 4, 5)
	raws := packFrames(t, gw, txs)
	if len(raws) != 1 {
		t.Fatalf("frames %d", len(raws))
	}
	extra, extraHash, err := frame.Encode(gw, 99, frame.TxTypeEthereum, txs[:3])
	if err != nil {
		t.Fatal(err)
	}
	bad, badHash, err := frame.Encode(rogue, 1, frame.TxTypeEthereum, txs[:2])
	if err != nil {
		t.Fatal(err)
	}
	h0, _ := frame.HashOf(raws[0])

	durable := map[types.Hash][]byte{extraHash: extra}
	peerStore := store.New(1 << 20)
	peerStore.Put(h0, raws[0])
	peerStore.Put(badHash, bad) // the peer holds it; the puller must not admit it
	peer := startFrameServer(t, nil, FrameConfig{
		Gateways: []types.Address{gwAddr, crypto.PubkeyToAddress(rogue.PublicKey)},
		Store:    peerStore,
		Lookup:   func(h types.Hash) ([]byte, bool) { b, ok := durable[h]; return b, ok },
	})

	local := store.New(1 << 20)
	me := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{gwAddr}, Store: local})

	unknown := types.Hash{0x42}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := me.PullFrames(ctx, []string{"127.0.0.1:1", peer.Addr()}, []types.Hash{h0, extraHash, unknown, badHash})
	if len(got) != 2 || string(got[h0]) != string(raws[0]) || string(got[extraHash]) != string(extra) {
		t.Fatalf("pulled %d frames", len(got))
	}
	if !local.Has(h0) || !local.Has(extraHash) || local.Has(badHash) {
		t.Fatal("pulled frames not admitted to the local store as expected")
	}
	if st := me.FrameStats(); st.Pulled != 2 {
		t.Fatalf("pulled counter %d", st.Pulled)
	}
	if st := peer.FrameStats(); st.PullServed != 3 {
		t.Fatalf("served counter %d", st.PullServed)
	}

	// A pull that cannot complete returns within its deadline.
	short, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	t0 := time.Now()
	if got := me.PullFrames(short, []string{peer.Addr()}, []types.Hash{unknown}); len(got) != 0 {
		t.Fatal("unknown frame returned")
	}
	if time.Since(t0) > 2*time.Second {
		t.Fatal("pull outlived its deadline")
	}
}
