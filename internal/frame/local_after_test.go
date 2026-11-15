// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// attestSource is a node off every committee whose attestations a test
// delivers: Senders answers the frames attested so far; onRecovered (when
// set) runs as a local recovery reports its frame, before the store takes it.
type attestSource struct {
	mu          sync.Mutex
	attested    map[types.Hash][]types.Address
	recovered   []types.Hash
	onRecovered func(h types.Hash, ss *store.Senders)
}

func (s *attestSource) Senders(h types.Hash, _ int) []types.Address {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attested[h]
}
func (*attestSource) Pending(types.Hash) bool { return true }
func (s *attestSource) Recovered(h types.Hash, ss *store.Senders) {
	s.mu.Lock()
	s.recovered = append(s.recovered, h)
	fn := s.onRecovered
	s.mu.Unlock()
	if fn != nil {
		fn(h, ss)
	}
}

// attest delivers frame h's attested senders as the pool does: the source
// answers them and the store caches them.
func (s *attestSource) attest(st *store.Store, signer transaction.Signer, h types.Hash, addrs []types.Address) {
	s.mu.Lock()
	if s.attested == nil {
		s.attested = make(map[types.Hash][]types.Address)
	}
	s.attested[h] = addrs
	s.mu.Unlock()
	st.SetSenders(h, &store.Senders{Signer: signer, Addrs: addrs})
}

func readyCounts(st *store.Store) (attested, local uint64) {
	s := st.Stats()
	for _, n := range s.ReadyAttested {
		attested += n
	}
	for _, n := range s.ReadyLocal {
		local += n
	}
	return
}

func fillableOrder(st *store.Store) (order []types.Hash, ready []bool) {
	st.ScanFillableReady(time.Hour, func(h types.Hash, _ store.Entry, r bool) bool {
		order = append(order, h)
		ready = append(ready, r)
		return true
	})
	return
}

// TestLocalAger (N42_FRAME_LOCAL_AFTER_MS): frames whose senders are
// expected from attestations are deferred by admission recovery. Before
// they are N ms old the ager takes none; after, it recovers them here,
// oldest first. A frame whose attestation arrived first is not recovered
// again; one whose attestation arrives while the local recovery runs is
// counted ready once. The order the builder scans the frames in is the
// same before and after.
func TestLocalAger(t *testing.T) {
	const after = 50 * time.Millisecond
	keys := frametest.Keys(t, 5, 19)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 4, 12)
	signer := transaction.LatestSignerForChainID(recoverChainID)

	st := store.New(1 << 24)
	src := &attestSource{}
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 1)
	rec.SetSource(src)
	rec.AttachForTest()
	ager := frame.NewLocalAger(rec, after, 1)

	t0 := time.Now()
	for i := range raws {
		if st.Admit(hashes[i], store.Entry{Raw: raws[i]}, false) != store.Stored {
			t.Fatalf("frame %d not stored", i)
		}
	}
	t1 := time.Now()
	for {
		if _, ok := rec.StepForTest(); !ok {
			break
		}
	}
	if rec.Stats().Deferred != 4 {
		t.Fatalf("admission deferred %d frames, want 4", rec.Stats().Deferred)
	}
	order0, ready0 := fillableOrder(st)
	for i, r := range ready0 {
		if r {
			t.Fatalf("frame %d ready before any attestation or local recovery", i)
		}
	}

	// Not yet N ms old (the latest instant that is before every admission
	// plus N): nothing is queued.
	if n := ager.Tick(t0.Add(after - time.Millisecond)); n != 0 {
		t.Fatalf("ager queued %d frames younger than %v", n, after)
	}
	if _, _, ok := ager.StepForTest(); ok {
		t.Fatal("a job was queued")
	}

	// Frame 0's attestation arrives before the frames are N ms old: it is
	// ready and never recovered here.
	want0 := mustRecover(t, signer, raws[0])
	src.attest(st, signer, hashes[0], want0)
	// Frame 2's attestation arrives while its local recovery runs.
	want2 := mustRecover(t, signer, raws[2])
	src.onRecovered = func(h types.Hash, _ *store.Senders) {
		if h == hashes[2] {
			src.attest(st, signer, h, want2)
		}
	}

	// Every frame is N ms old: the three without senders are queued, oldest
	// first, and a second tick queues none of them again.
	if n := ager.Tick(t1.Add(after)); n != 3 {
		t.Fatalf("ager queued %d frames, want 3", n)
	}
	if n := ager.Tick(t1.Add(after)); n != 0 {
		t.Fatalf("ager re-queued %d frames", n)
	}
	var got []types.Hash
	for {
		h, o, ok := ager.StepForTest()
		if !ok {
			break
		}
		if o != frame.OutcomeLocal {
			t.Fatalf("frame %x: outcome %d, want a local recovery", h[:4], o)
		}
		got = append(got, h)
	}
	if len(got) != 3 || got[0] != hashes[1] || got[1] != hashes[2] || got[2] != hashes[3] {
		t.Fatalf("recovery order %x, want frames 1, 2, 3", got)
	}
	for i := range hashes {
		if !st.HasSenders(hashes[i]) {
			t.Fatalf("frame %d not ready", i)
		}
		info := st.RecoverInfoOf(hashes[i])
		if info.Running {
			t.Fatalf("frame %d still running", i)
		}
	}
	if ss := st.Senders(hashes[2]); len(ss.Addrs) != len(want2) {
		t.Fatalf("frame 2 senders %d", len(ss.Addrs))
	}
	// Readiness counted once per frame: 0 and 2 by attestation, 1 and 3
	// locally.
	if a, l := readyCounts(st); a != 2 || l != 2 {
		t.Fatalf("ready counts attested %d local %d, want 2 and 2", a, l)
	}
	if s := st.Stats(); s.WithSenders != 4 {
		t.Fatalf("frames with senders %d, want 4", s.WithSenders)
	}
	q, r, a, sk := ager.Stats()
	if q != 3 || r != 3 || a != 0 || sk != 0 {
		t.Fatalf("ager stats queued %d recovered %d attested %d skipped %d", q, r, a, sk)
	}
	if qh, rh := rec.HurriedLocal(); qh != 0 || rh != 0 {
		t.Fatalf("aged recoveries counted as held-wait ones: %d %d", qh, rh)
	}
	// Nothing left to recover: no frame is queued again.
	if n := ager.Tick(time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("ager queued %d ready frames", n)
	}
	// The builder's scan order is unchanged; every frame is ready.
	order1, ready1 := fillableOrder(st)
	if len(order1) != len(order0) {
		t.Fatalf("scan saw %d frames, before %d", len(order1), len(order0))
	}
	for i := range order0 {
		if order0[i] != order1[i] || !ready1[i] {
			t.Fatalf("scan position %d: %x ready %v, before %x", i, order1[i][:4], ready1[i], order0[i][:4])
		}
	}
}

// TestLocalAgerAttestedAtTurn: a frame queued by age whose attestation was
// accepted by its turn takes the attested senders (no local recovery).
func TestLocalAgerAttestedAtTurn(t *testing.T) {
	const after = 20 * time.Millisecond
	keys := frametest.Keys(t, 5, 19)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 1, 8)
	signer := transaction.LatestSignerForChainID(recoverChainID)
	st := store.New(1 << 24)
	src := &attestSource{}
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 1)
	rec.SetSource(src)
	rec.AttachForTest()
	ager := frame.NewLocalAger(rec, after, 1)
	if st.Admit(hashes[0], store.Entry{Raw: raws[0]}, false) != store.Stored {
		t.Fatal("not stored")
	}
	t1 := time.Now()
	for {
		if _, ok := rec.StepForTest(); !ok {
			break
		}
	}
	if n := ager.Tick(t1.Add(after)); n != 1 {
		t.Fatalf("queued %d", n)
	}
	// The pool accepted the attestation (the source answers it) but the
	// store has not been told yet.
	src.mu.Lock()
	src.attested = map[types.Hash][]types.Address{hashes[0]: mustRecover(t, signer, raws[0])}
	src.mu.Unlock()
	if _, o, ok := ager.StepForTest(); !ok || o != frame.OutcomeAttested {
		t.Fatalf("outcome %d (%v), want attested", o, ok)
	}
	if !st.HasSenders(hashes[0]) {
		t.Fatal("not ready")
	}
	if a, l := readyCounts(st); a+l != 1 {
		t.Fatalf("ready counted %d times", a+l)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.recovered) != 0 {
		t.Fatal("the frame was recovered locally")
	}
}

func mustRecover(t *testing.T, signer transaction.Signer, raw []byte) []types.Address {
	t.Helper()
	ss, failed, err := frame.RecoverSenders(signer, raw)
	if err != nil || failed != 0 {
		t.Fatalf("recover: %v (%d failed)", err, failed)
	}
	return ss.Addrs
}
