// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

func frameStoreOf(t testing.TB, nFrames, perFrame int) (*store.Store, []types.Hash) {
	keys := frametest.Keys(t, 65, 9)
	fb := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, hashes, _ := fb.Frames(t, nFrames, perFrame)
	st := store.New(1 << 30)
	for i, raw := range raws {
		f, err := frame.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !st.PutEntry(hashes[i], store.Entry{Raw: raw, Gas: frame.GasEstimate(f), Txs: int(f.TxCount)}) {
			t.Fatal("put")
		}
	}
	return st, hashes
}

// TestPickFramesOrderAndGasStop: 20 frames x 500 transfers (10.5M gas each);
// a 70M budget takes the six oldest in arrival order and stops at the
// seventh; in-flight frames are passed over; the frame cap holds.
func TestPickFramesOrderAndGasStop(t *testing.T) {
	st, hashes := frameStoreOf(t, 20, 500)
	const perFrameGas = 500 * 21000

	p := pickFrames(st, nil, types.Address{}, 70_000_000, 1024)
	if len(p.hashes) != 6 || p.gas != 6*perFrameGas || p.txs != 3000 {
		t.Fatalf("picked %d frames gas %d txs %d", len(p.hashes), p.gas, p.txs)
	}
	for i, h := range p.hashes {
		if h != hashes[i] {
			t.Fatalf("frame %d out of arrival order", i)
		}
	}

	inFlight := map[types.Hash]bool{hashes[0]: true, hashes[2]: true}
	p = pickFrames(st, func(h types.Hash) bool { return inFlight[h] }, types.Address{}, 3*perFrameGas, 1024)
	if len(p.hashes) != 3 || p.hashes[0] != hashes[1] || p.hashes[1] != hashes[3] || p.hashes[2] != hashes[4] {
		t.Fatalf("in-flight skip: %v", p.hashes)
	}

	if p = pickFrames(st, nil, types.Address{}, 1<<62, 5); len(p.hashes) != 5 {
		t.Fatalf("max frames: %d", len(p.hashes))
	}
	if p = pickFrames(st, nil, types.Address{}, perFrameGas-1, 1024); len(p.hashes) != 0 {
		t.Fatal("frame above the budget was taken")
	}

	// Frames a written block holds leave the store; the next pick starts
	// after them.
	st.Remove(hashes[:6])
	if p = pickFrames(st, nil, types.Address{}, perFrameGas, 1024); len(p.hashes) != 1 || p.hashes[0] != hashes[6] {
		t.Fatal("pick after remove")
	}
}

// TestPickFramesRate reports the pick rate over a store of 4,000 frames
// (in-process number for the P1b report).
func TestPickFramesRate(t *testing.T) {
	if testing.Short() {
		t.Skip("rate measurement")
	}
	st, _ := frameStoreOf(t, 4000, 5)
	inFlight := func(types.Hash) bool { return false }
	const rounds = 50
	t0 := time.Now()
	n := 0
	for i := 0; i < rounds; i++ {
		n += len(pickFrames(st, inFlight, types.Address{}, 1<<62, 1024).hashes)
	}
	el := time.Since(t0)
	t.Logf("pickFrames: %d frames in %v = %.0f frames/s", n, el, float64(n)/el.Seconds())
}
