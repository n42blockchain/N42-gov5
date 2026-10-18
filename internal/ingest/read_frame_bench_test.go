// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ingest

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// BenchmarkReadFrame is the admission cost of one accepted FRM1 frame of
// MaxTxs transfers on the connection goroutine, without the socket: read,
// layout check, gateway check, keccak + ecrecover, store admission with
// the gas estimate. Every frame is new to the store (the store is reset
// every len(pre) frames, off the clock). Reported per frame.
func BenchmarkReadFrame(b *testing.B) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(b, 50, 10)
	pre := make([][]byte, 512)
	for i := range pre {
		pre[i], _, _ = frame.Encode(gw, uint64(i+1), frame.TxTypeEthereum, txs[:frame.MaxTxs])
	}
	srv := NewServer("127.0.0.1:0", &mockTxPool{}, 1<<30, 1<<30)
	reset := func() {
		if err := srv.EnableFrames(FrameConfig{
			Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
			Store:    store.New(1 << 30),
		}); err != nil {
			b.Fatal(err)
		}
	}
	reset()
	var r bytes.Reader
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := i % len(pre)
		if k == 0 && i > 0 {
			b.StopTimer()
			reset()
			b.StartTimer()
		}
		r.Reset(pre[k])
		reply, err := srv.readFrame(&r)
		if err != nil || reply[0] != FrameAccepted {
			b.Fatal(err, reply[0])
		}
	}
}
