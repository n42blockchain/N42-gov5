// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// slowRecoverer stands in for a recoverer whose frames are attested late:
// nothing recovers on admission or on the ordinary priority lane, and a
// frame handed to the local lane gets its senders delay later.
type slowRecoverer struct {
	noopRecoverer
	st    *store.Store
	delay time.Duration
	n     int
	mu    sync.Mutex
	local []types.Hash
	wg    sync.WaitGroup
}

func (r *slowRecoverer) Hurry(types.Hash, []byte) bool { return true }

func (r *slowRecoverer) HurryLocal(h types.Hash, _ []byte) bool {
	r.mu.Lock()
	r.local = append(r.local, h)
	r.mu.Unlock()
	if r.delay >= time.Hour {
		return true // never recovered
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		time.Sleep(r.delay)
		r.st.SetSenders(h, &store.Senders{Addrs: make([]types.Address, r.n)})
	}()
	return true
}

// heldStore: gateways A (one sender, so its nonces run across its frames)
// and B, four frames each, arriving a0 b0 a1 b1 ...; a1 and a3 have no
// senders, every other frame is ready.
func heldStore(t *testing.T, delay time.Duration) (*store.Store, *slowRecoverer, []types.Hash, map[types.Hash]int, [][]types.Hash) {
	keysA := frametest.Keys(t, 9, 51)
	keysB := frametest.Keys(t, 9, 52)
	ra, ha, _ := frametest.NewBuilder(keysA[0], big.NewInt(94), keysA[1:2]).Frames(t, 4, 5)
	rb, hb, _ := frametest.NewBuilder(keysB[0], big.NewInt(94), keysB[1:]).Frames(t, 4, 5)
	st := store.New(1 << 30)
	rec := &slowRecoverer{st: st, delay: delay, n: 5}
	st.SetRecoverer(rec)
	gwOf := map[types.Hash]int{}
	var arrival []types.Hash
	for i := 0; i < 4; i++ {
		for g, fr := range []struct {
			raw []byte
			h   types.Hash
		}{{ra[i], ha[i]}, {rb[i], hb[i]}} {
			f, err := frame.Decode(fr.raw)
			if err != nil {
				t.Fatal(err)
			}
			if !st.PutEntry(fr.h, store.Entry{Raw: fr.raw, Gas: frame.GasEstimate(f), Txs: int(f.TxCount)}) {
				t.Fatal("put")
			}
			gwOf[fr.h] = g
			arrival = append(arrival, fr.h)
		}
	}
	for _, h := range []types.Hash{ha[0], ha[2], hb[0], hb[1], hb[2], hb[3]} {
		if !st.SetSenders(h, &store.Senders{Addrs: make([]types.Address, 5)}) {
			t.Fatal("set senders")
		}
	}
	return st, rec, arrival, gwOf, [][]types.Hash{ha, hb}
}

// checkPickRule asserts the switch-off pick rule on p: per gateway an
// arrival-order prefix, the whole pick in arrival order, and every
// sender's nonces consecutive in the flattened transactions.
func checkPickRule(t *testing.T, st *store.Store, p framePick, arrival []types.Hash, gwOf map[types.Hash]int, perGw [][]types.Hash) {
	t.Helper()
	pos := map[types.Hash]int{}
	for i, h := range arrival {
		pos[h] = i
	}
	next := map[int]int{}
	for i, h := range p.hashes {
		if i > 0 && pos[p.hashes[i-1]] >= pos[h] {
			t.Fatalf("pick %d out of arrival order", i)
		}
		g := gwOf[h]
		if perGw[g][next[g]] != h {
			t.Fatalf("gateway %d frame taken out of its arrival order", g)
		}
		next[g]++
	}
	signer := transaction.LatestSignerForChainID(big.NewInt(94))
	last := map[types.Address]uint64{}
	for _, raw := range p.raws {
		txs, _, err := frame.DecodeTxs(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, tx := range txs {
			from, err := transaction.Sender(signer, tx)
			if err != nil {
				t.Fatal(err)
			}
			if n, ok := last[from]; ok && tx.Nonce() != n+1 {
				t.Fatalf("sender %s nonce %d after %d", from, tx.Nonce(), n)
			}
			last[from] = tx.Nonce()
		}
	}
	_ = st
}

// TestPickHeldWaitFillsShortBuild (N42_FRAME_HELD_WAIT_MS): gateway A's
// frames a1 and a3 are recovered slowly. Off, the build stops at a1 and
// takes 5 of 8 frames. On, the held frames go to local recovery, the build
// waits for a1 and picks again until nothing is held: all 8 frames, in
// arrival order, each gateway a prefix and each sender's nonces
// consecutive. The block is exactly the pick a switch-off leader makes
// from the same store once those frames are ready, so a switch-off node
// validates it as any other.
func TestPickHeldWaitFillsShortBuild(t *testing.T) {
	defer func(prev time.Duration) { frameReadyGrace = prev }(frameReadyGrace)
	frameReadyGrace = time.Hour
	defer func(prev bool) { frameReadyPick = prev }(frameReadyPick)
	frameReadyPick = true
	defer func(prev bool) { frameFillHeld = prev }(frameFillHeld)
	frameFillHeld = false

	st, rec, arrival, gwOf, perGw := heldStore(t, 2*time.Millisecond)
	defer rec.wg.Wait()
	pick := func() framePick { return pickFrames(st, nil, types.Address{}, 1<<62, 1024) }

	defer SetFrameHeldWaitForTest(0)()
	off := pickHeldWait(st, pick)
	if len(off.hashes) != 5 || off.held != 1 || off.capped || off.wait.waited != 0 || len(rec.local) != 0 {
		t.Fatalf("off: %d frames held %d waited %v local %d", len(off.hashes), off.held, off.wait.waited, len(rec.local))
	}
	if off.wait.headAge <= 0 {
		t.Fatalf("off: head age %v not stamped", off.wait.headAge)
	}
	checkPickRule(t, st, off, arrival, gwOf, perGw)

	SetFrameHeldWaitForTest(5 * time.Second)
	on := pickHeldWait(st, pick)
	if len(on.hashes) != len(arrival) || on.held != 0 {
		t.Fatalf("on: %d frames held %d (wait %+v)", len(on.hashes), on.held, on.wait)
	}
	for i := range arrival {
		if on.hashes[i] != arrival[i] {
			t.Fatalf("on: pick %d out of arrival order", i)
		}
	}
	checkPickRule(t, st, on, arrival, gwOf, perGw)
	if on.wait.repicks < 1 || on.wait.gained != 3 || on.wait.local < 2 || on.wait.expired || on.wait.waited <= 0 {
		t.Fatalf("on: wait %+v", on.wait)
	}
	// The same store, switch off: the identical block.
	SetFrameHeldWaitForTest(0)
	same := pickHeldWait(st, pick)
	if len(same.hashes) != len(on.hashes) {
		t.Fatalf("switch-off pick after readiness: %d frames", len(same.hashes))
	}
	for i := range same.hashes {
		if same.hashes[i] != on.hashes[i] {
			t.Fatalf("switch-off pick differs at %d", i)
		}
	}
}

// TestPickHeldWaitBounded: a head frame slower than the wait leaves the
// pick as it was, after about the configured wait; a capped pick and a
// pick with nothing held never wait.
func TestPickHeldWaitBounded(t *testing.T) {
	defer func(prev time.Duration) { frameReadyGrace = prev }(frameReadyGrace)
	frameReadyGrace = time.Hour
	defer func(prev bool) { frameReadyPick = prev }(frameReadyPick)
	frameReadyPick = true

	st, rec, arrival, gwOf, perGw := heldStore(t, time.Hour)
	_ = rec
	pick := func() framePick { return pickFrames(st, nil, types.Address{}, 1<<62, 1024) }
	defer SetFrameHeldWaitForTest(20 * time.Millisecond)()
	t0 := time.Now()
	p := pickHeldWait(st, pick)
	if d := time.Since(t0); d < 20*time.Millisecond || d > 2*time.Second {
		t.Fatalf("wait took %v", d)
	}
	if len(p.hashes) != 5 || !p.wait.expired || p.wait.repicks != 0 || p.wait.gained != 0 {
		t.Fatalf("expired wait: %d frames, wait %+v", len(p.hashes), p.wait)
	}
	checkPickRule(t, st, p, arrival, gwOf, perGw)

	// Capped at 3 frames: a filled build does not wait.
	capped := func() framePick { return pickFrames(st, nil, types.Address{}, 1<<62, 3) }
	t0 = time.Now()
	if p = pickHeldWait(st, capped); !p.capped || p.wait.waited != 0 || time.Since(t0) > time.Second {
		t.Fatalf("capped pick waited %v", p.wait.waited)
	}
	// Nothing held: no wait.
	for _, h := range arrival {
		st.SetSenders(h, &store.Senders{Addrs: make([]types.Address, 5)})
	}
	if p = pickHeldWait(st, pick); p.held != 0 || p.wait.waited != 0 || len(p.hashes) != len(arrival) {
		t.Fatalf("ready store: held %d waited %v", p.held, p.wait.waited)
	}
}
