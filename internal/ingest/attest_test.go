// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ingest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/attest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestAttestationOverIngest (P8b): attestation batches pushed over the
// ingest listener reach the handler in order, on a connection that keeps
// carrying frames.
func TestAttestationOverIngest(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	srv := startFrameServer(t, nil, FrameConfig{Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)}, Store: store.New(1 << 20)})
	var (
		mu  sync.Mutex
		got []*attest.Batch
	)
	srv.SetAttestHandler(func(body []byte) {
		b, err := attest.DecodeBody(body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		got = append(got, b)
		mu.Unlock()
	})
	p := attest.NewPusher(attest.PeersFromEnv([]string{srv.Addr(), "127.0.0.1:1"}, "0.0.0.0:1"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()
	for i := 0; i < 3; i++ {
		p.Send(attest.EncodeWire(&attest.Batch{Attester: types.Address{byte(i + 1)}, Sig: []byte{9},
			Entries: []attest.Entry{{Frame: types.Hash{byte(i)}, Senders: []types.Address{{1}}}}}))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of 3 batches arrived", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i, b := range got {
		if b.Attester != (types.Address{byte(i + 1)}) || len(b.Entries) != 1 || b.Entries[0].Frame != (types.Hash{byte(i)}) {
			t.Fatalf("batch %d: %+v", i, b)
		}
	}
	if peers := attest.PeersFromEnv([]string{"127.0.0.1:9100", "127.0.0.1:9101"}, "0.0.0.0:9101"); len(peers) != 1 || peers[0] != "127.0.0.1:9100" {
		t.Fatalf("self not dropped: %v", peers)
	}
}
