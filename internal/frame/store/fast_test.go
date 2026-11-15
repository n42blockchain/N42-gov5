// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// flushCombiner returns once every op queued before it is applied.
func (s *Store) flushCombiner() {
	o := getWop(opBegin)
	o.h = types.Hash{0xff, 0xfe, 0xfd} // never stored: a no-op claim
	s.combineSync(o)
	putWop(o)
}

// serialAdmit is the historical FRM1 admission (ingest readFrame before
// r93): Has, then the in-order check against lastSeq, then Admit.
type serialAdmit struct {
	st      *Store
	mu      sync.Mutex
	lastSeq map[types.Address]uint64
}

func (a *serialAdmit) admit(h types.Hash, e Entry, gw types.Address, seq uint64) InOrderResult {
	if a.st.Has(h) {
		return InOrderResult{Result: Duplicate, Known: true}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if last, seen := a.lastSeq[gw]; seen && seq > last+1 {
		return InOrderResult{Result: Busy, OutOfOrder: true}
	}
	r := a.st.Admit(h, e, false)
	if r == Stored {
		if last, seen := a.lastSeq[gw]; !seen || seq > last {
			a.lastSeq[gw] = seq
		}
	}
	return InOrderResult{Result: r}
}

type admitStep struct {
	h    types.Hash
	gw   types.Address
	seq  uint64
	size int
}

func gwAddr(i int) types.Address { return types.Address{0xa0, byte(i)} }

// script builds a per-gateway input with in-order frames, gaps, resends of
// stored frames and resends of committed (tombstoned) ones.
func script(gw types.Address, gi, n int, rng *rand.Rand) []admitStep {
	var out []admitStep
	h := func(seq uint64) types.Hash { return types.Hash{0xc0, byte(gi), byte(seq >> 8), byte(seq)} }
	seq := uint64(1)
	for len(out) < n {
		switch rng.IntN(8) {
		case 0: // gap
			out = append(out, admitStep{h(seq + 2), gw, seq + 2, 100})
		case 1: // resend of an earlier frame
			if seq > 1 {
				p := 1 + rng.Uint64N(seq-1)
				out = append(out, admitStep{h(p), gw, p, 100})
			}
		default:
			out = append(out, admitStep{h(seq), gw, seq, 100})
			seq++
		}
	}
	return out
}

func runSerial(st *Store, steps []admitStep) []InOrderResult {
	a := &serialAdmit{st: st, lastSeq: map[types.Address]uint64{}}
	out := make([]InOrderResult, len(steps))
	for i, s := range steps {
		out[i] = a.admit(s.h, Entry{Raw: make([]byte, s.size)}, s.gw, s.seq)
	}
	return out
}

// TestAdmitInOrderMatchesSerial: one input order, the same replies and the
// same store counters, with commits (tombstones) and a full budget in it.
func TestAdmitInOrderMatchesSerial(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var steps []admitStep
	for g := 0; g < 3; g++ {
		steps = append(steps, script(gwAddr(g), g, 300, rng)...)
	}
	rng.Shuffle(len(steps), func(i, j int) { steps[i], steps[j] = steps[j], steps[i] })
	// Budget for 20 frames of 100 bytes: Busy happens, commits free room.
	mk := func() *Store { return NewWithKeep(20*100, 1) }
	a, b := mk(), mk()
	ser := &serialAdmit{st: a, lastSeq: map[types.Address]uint64{}}
	for i, s := range steps {
		e := Entry{Raw: make([]byte, s.size)}
		want := ser.admit(s.h, e, s.gw, s.seq)
		got := b.AdmitInOrder(s.h, e, s.gw, s.seq)
		if got != want {
			t.Fatalf("step %d %+v: got %+v want %+v", i, s, got, want)
		}
		if i%97 == 96 {
			// Commit everything stored so far, two blocks apart (keep 1).
			n := uint64(i)
			var hs []types.Hash
			a.Scan(func(h types.Hash, _ Entry) bool { hs = append(hs, h); return true })
			a.Commit(n, hs)
			b.Commit(n, hs)
			a.Commit(n+2, nil)
			b.Commit(n+2, nil)
		}
	}
	sa, sb := a.Stats(), b.Stats()
	if sa != sb {
		t.Fatalf("stats differ:\n serial %+v\n combined %+v", sa, sb)
	}
	if sa.Busy == 0 || sa.Duplicates == 0 || sa.Evicted == 0 {
		t.Fatalf("script did not exercise busy/dup/evict: %+v", sa)
	}
}

// TestAdmitInOrderConcurrentPerGateway: gateways admitted concurrently, each
// by one goroutine in its own order, with async recovery finishes and
// attestations interleaved, get exactly the replies of the serial path.
func TestAdmitInOrderConcurrentPerGateway(t *testing.T) {
	const gws = 8
	rng := rand.New(rand.NewPCG(3, 4))
	scripts := make([][]admitStep, gws)
	want := make([][]InOrderResult, gws)
	ref := New(1 << 30)
	for g := range scripts {
		scripts[g] = script(gwAddr(g), g, 400, rng)
		want[g] = runSerial(ref, scripts[g])
	}
	st := New(1 << 30)
	got := make([][]InOrderResult, gws)
	var wg sync.WaitGroup
	for g := range scripts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, s := range scripts[g] {
				r := st.AdmitInOrder(s.h, Entry{Raw: make([]byte, s.size)}, s.gw, s.seq)
				got[g] = append(got[g], r)
				if r.Result == Stored {
					if s.seq%2 == 0 {
						st.SetSendersAsync([]types.Hash{s.h}, []*Senders{{}})
					} else if tk := st.BeginRecoverCombined(s.h, true); tk.Own {
						st.FinishRecoverAsync(tk, &Senders{})
					}
				}
			}
		}()
	}
	wg.Wait()
	st.flushCombiner()
	for g := range scripts {
		if !slices.Equal(got[g], want[g]) {
			t.Fatalf("gateway %d: replies differ", g)
		}
	}
	s := st.Stats()
	if s.Frames != ref.Len() || s.WithSenders != s.Frames {
		t.Fatalf("frames %d (ref %d), withSenders %d", s.Frames, ref.Len(), s.WithSenders)
	}
	var ready uint64
	for i := range s.ReadyAttested {
		ready += s.ReadyAttested[i] + s.ReadyLocal[i]
	}
	if ready != uint64(s.Frames) {
		t.Fatalf("readiness counted %d times for %d frames", ready, s.Frames)
	}
}

// TestBatchedSendersCountedOnce: the same frames cached by many async
// attestations and recoveries at once count their readiness exactly once,
// and every recovery waiter is woken.
func TestBatchedSendersCountedOnce(t *testing.T) {
	st := New(1 << 30)
	const n = 500
	hs := make([]types.Hash, n)
	for i := range hs {
		hs[i] = hashOf(i)
		if st.Admit(hs[i], Entry{Raw: []byte{1}}, false) != Stored {
			t.Fatal("admit")
		}
	}
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < n; i += 3 {
				if w%2 == 0 {
					st.SetSendersAsync(hs[i:i+1], []*Senders{{}})
					continue
				}
				tk := st.BeginRecoverCombined(hs[i], false)
				switch {
				case tk.Own:
					st.FinishRecoverAsync(tk, &Senders{})
				case tk.Wait != nil:
					select {
					case <-tk.Wait:
					case <-time.After(10 * time.Second):
						t.Error("waiter never woken")
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	st.flushCombiner()
	s := st.Stats()
	var ready uint64
	for i := range s.ReadyAttested {
		ready += s.ReadyAttested[i] + s.ReadyLocal[i]
	}
	if s.WithSenders != n || ready != n {
		t.Fatalf("withSenders %d, readiness counted %d, want %d", s.WithSenders, ready, n)
	}
}

func TestBatchReadsMatchSingle(t *testing.T) {
	st := New(1 << 30)
	var hs []types.Hash
	for i := 0; i < 50; i++ {
		h := hashOf(i)
		hs = append(hs, h)
		if i%5 == 4 {
			continue // not stored
		}
		st.Admit(h, Entry{Raw: []byte{byte(i), 1}}, false)
		if i%2 == 0 {
			st.SetSenders(h, &Senders{Addrs: []types.Address{{byte(i)}}})
		}
	}
	raws := make([][]byte, len(hs))
	ss := make([]*Senders, len(hs))
	infos := make([]RecoverInfo, len(hs))
	if miss := st.GetBatch(hs, raws); miss != 10 {
		t.Fatalf("missing %d", miss)
	}
	st.SendersBatch(hs, ss)
	st.RecoverInfoBatch(hs, infos)
	for i, h := range hs {
		raw, _ := st.Get(h)
		ri := st.RecoverInfoOf(h)
		if !slices.Equal(raw, raws[i]) || ss[i] != st.Senders(h) ||
			ri.Stored != infos[i].Stored || ri.Ready != infos[i].Ready || ri.Running != infos[i].Running || ri.Claimed != infos[i].Claimed {
			t.Fatalf("frame %d differs", i)
		}
	}
}

type scanRec struct {
	h             types.Hash
	ready, pinned bool
}

func collectScans(st *Store) (fill, unc []scanRec) {
	st.ScanFillableReady(0, func(h types.Hash, _ Entry, ready bool) bool {
		fill = append(fill, scanRec{h, ready, false})
		return true
	})
	st.ScanUncommittedReady(0, func(h types.Hash, _ Entry, ready, pinned bool) bool {
		unc = append(unc, scanRec{h, ready, pinned})
		return true
	})
	return fill, unc
}

// TestScanFastIdentical: the cursor and the snapshot yield the same frames
// in the same order, with the same readiness, through commits, pins,
// forks, removals and ring compaction.
func TestScanFastIdentical(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, keep := range []uint64{0, 2, 16} {
		t.Run(fmt.Sprint("keep", keep), func(t *testing.T) {
			st := NewWithKeep(1<<30, keep)
			st.SetRecoverer(nopRecoverer{})
			next := 0
			var height uint64
			for round := 0; round < 400; round++ {
				for k := rng.IntN(40); k > 0; k-- {
					h := hashOf(next)
					next++
					st.Admit(h, Entry{Raw: []byte{1}}, false)
					if rng.IntN(3) > 0 {
						st.SetSenders(h, &Senders{})
					}
				}
				var fill []types.Hash
				st.ScanFillable(func(h types.Hash, _ Entry) bool {
					fill = append(fill, h)
					return len(fill) < 30
				})
				// Mostly the oldest frames, sometimes skipping one.
				var take []types.Hash
				for _, h := range fill {
					if rng.IntN(10) > 0 {
						take = append(take, h)
					}
				}
				height++
				st.Include(take, height)
				switch rng.IntN(6) {
				case 0: // abandoned fork: commit the height without its frames
					st.Commit(height, nil)
				case 1:
					st.Remove(fill[:min(len(fill), 2)])
					st.Commit(height, take)
				default:
					st.Commit(height, take)
				}
				func() {
					defer SetScanFastForTest(false)()
					f0, u0 := collectScans(st)
					restore := SetScanFastForTest(true)
					f1, u1 := collectScans(st)
					restore()
					if !slices.Equal(f0, f1) || !slices.Equal(u0, u1) {
						t.Fatalf("round %d: scans differ (fill %d/%d, uncommitted %d/%d)", round, len(f0), len(f1), len(u0), len(u1))
					}
				}()
				// Keep the cursor maintained, as with the switch on.
				if round%2 == 0 {
					restore := SetScanFastForTest(true)
					st.Commit(height, nil)
					restore()
				}
			}
		})
	}
}

type nopRecoverer struct{}

func (nopRecoverer) Submit(types.Hash, []byte) {}
func (nopRecoverer) Pending() int64            { return 0 }
func (nopRecoverer) Recovered() uint64         { return 0 }
