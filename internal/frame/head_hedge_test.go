// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// hedgeRig is a node off every committee (attestSource) with n frames of
// one gateway admitted and their admission recovery deferred to the
// attestations, and a hedge on a fake clock.
type hedgeRig struct {
	st     *store.Store
	src    *attestSource
	rec    *frame.SenderRecoverer
	hedge  *frame.HeadHedge
	signer transaction.Signer
	raws   [][]byte
	hashes []types.Hash
	now    time.Time
}

func newHedgeRig(t *testing.T, n, k int, grace time.Duration, workers int) *hedgeRig {
	t.Helper()
	keys := frametest.Keys(t, 5, 23)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, n, 8)
	r := &hedgeRig{st: store.New(1 << 24), src: &attestSource{}, raws: raws, hashes: hashes,
		signer: transaction.LatestSignerForChainID(recoverChainID)}
	r.rec = frame.NewSenderRecoverer(r.st, func() transaction.Signer { return r.signer }, 1)
	r.rec.SetSource(r.src)
	r.rec.AttachForTest()
	r.hedge = frame.NewHeadHedge(r.rec, k, grace, workers)
	r.hedge.SetNowForTest(func() time.Time { return r.now })
	for i := range raws {
		if r.st.Admit(hashes[i], store.Entry{Raw: raws[i]}, false) != store.Stored {
			t.Fatalf("frame %d not stored", i)
		}
	}
	r.now = time.Now() // every frame is admitted by now
	for {
		if _, ok := r.rec.StepForTest(); !ok {
			break
		}
	}
	if d := r.rec.Stats().Deferred; d != uint64(n) {
		t.Fatalf("admission deferred %d frames, want %d", d, n)
	}
	return r
}

// jobs drains the dispatched jobs.
func (r *hedgeRig) jobs() []frame.HeadFrameJob {
	var out []frame.HeadFrameJob
	for {
		j, ok := r.hedge.NextJobForTest()
		if !ok {
			return out
		}
		out = append(out, j)
	}
}

// TestHeadHedgeRecoversLateHead: a head frame whose attestation never
// arrives is left alone during the grace and recovered here after it; its
// senders are the ones a recovery yields and the local recovery is
// reported to the source (a fallback, or a member's attestation).
func TestHeadHedgeRecoversLateHead(t *testing.T) {
	const grace = 10 * time.Millisecond
	r := newHedgeRig(t, 1, 4, grace, 2)
	if wait := r.hedge.Evaluate(); wait <= 0 || wait > grace {
		t.Fatalf("before the grace: wait %v, want (0, %v]", wait, grace)
	}
	if js := r.jobs(); len(js) != 0 {
		t.Fatalf("dispatched %d jobs inside the grace", len(js))
	}
	r.now = r.now.Add(grace)
	if wait := r.hedge.Evaluate(); wait != 0 {
		t.Fatalf("nothing pending, wait %v", wait)
	}
	js := r.jobs()
	if len(js) != 1 || js[0].H != r.hashes[0] {
		t.Fatalf("dispatched %v, want the head frame", js)
	}
	if o := r.hedge.Run(js[0]); o != frame.HedgeWonLocal {
		t.Fatalf("outcome %d, want a local win", o)
	}
	ss := r.st.Senders(r.hashes[0])
	want := mustRecover(t, r.signer, r.raws[0])
	if ss == nil || !equalAddrs(ss.Addrs, want) {
		t.Fatal("senders not cached or wrong")
	}
	if a, l := readyCounts(r.st); a != 0 || l != 1 {
		t.Fatalf("ready counts attested %d local %d", a, l)
	}
	if len(r.src.recovered) != 1 || r.src.recovered[0] != r.hashes[0] {
		t.Fatalf("source told %x", r.src.recovered)
	}
	s := r.hedge.Stats()
	if s.Dispatched != 1 || s.Hedged != 1 || s.WonLocal != 1 || s.WonAttested != 0 || s.WastedUs != 0 {
		t.Fatalf("stats %+v", s)
	}
	// Ready now: nothing more to do.
	r.hedge.Evaluate()
	if js := r.jobs(); len(js) != 0 {
		t.Fatalf("dispatched %d jobs for a ready head", len(js))
	}
}

// TestHeadHedgeOnlyFirstK: only the first K unready frames are hedged,
// even with idle workers; once they are ready the next K are.
func TestHeadHedgeOnlyFirstK(t *testing.T) {
	r := newHedgeRig(t, 6, 2, 0, 8)
	r.now = r.now.Add(time.Second)
	r.hedge.Evaluate()
	js := r.jobs()
	if len(js) != 2 || js[0].H != r.hashes[0] || js[1].H != r.hashes[1] {
		t.Fatalf("dispatched %d jobs, want frames 0 and 1", len(js))
	}
	// Dispatched but not run yet: evaluating again hands out nothing new.
	r.hedge.Evaluate()
	if more := r.jobs(); len(more) != 0 {
		t.Fatalf("re-dispatched %d jobs", len(more))
	}
	for _, j := range js {
		r.hedge.Run(j)
	}
	// Frame 2's attestation arrived meanwhile: the head is now 3 and 4.
	r.src.attest(r.st, r.signer, r.hashes[2], mustRecover(t, r.signer, r.raws[2]))
	r.hedge.Evaluate()
	js = r.jobs()
	if len(js) != 2 || js[0].H != r.hashes[3] || js[1].H != r.hashes[4] {
		t.Fatalf("second round dispatched %d jobs, want frames 3 and 4", len(js))
	}
}

// TestHeadHedgeWorkerBound: no more jobs than workers are out at a time.
func TestHeadHedgeWorkerBound(t *testing.T) {
	r := newHedgeRig(t, 4, 4, 0, 1)
	r.now = r.now.Add(time.Second)
	r.hedge.Evaluate()
	js := r.jobs()
	if len(js) != 1 || js[0].H != r.hashes[0] {
		t.Fatalf("dispatched %d jobs with one worker", len(js))
	}
	r.hedge.Run(js[0])
	r.hedge.Evaluate()
	if js = r.jobs(); len(js) != 1 || js[0].H != r.hashes[1] {
		t.Fatalf("next job %v, want frame 1", js)
	}
}

// TestHeadHedgeAttestationWins: an attestation that arrives before a
// dispatched job starts, or while its recovery runs, makes the frame ready
// at once with the attested senders; the local result is discarded and
// readiness is counted once.
func TestHeadHedgeAttestationWins(t *testing.T) {
	r := newHedgeRig(t, 2, 4, 0, 4)
	r.now = r.now.Add(time.Second)
	r.hedge.Evaluate()
	js := r.jobs()
	if len(js) != 2 {
		t.Fatalf("dispatched %d", len(js))
	}
	att0 := mustRecover(t, r.signer, r.raws[0])
	att1 := mustRecover(t, r.signer, r.raws[1])
	// Frame 0: attested before its job starts.
	r.src.attest(r.st, r.signer, r.hashes[0], att0)
	if o := r.hedge.Run(js[0]); o != frame.HedgeWonAttested {
		t.Fatalf("frame 0 outcome %d", o)
	}
	// Frame 1: attested while the local recovery runs; ready at once.
	var readyDuring bool
	r.hedge.SetAfterRecoverForTest(func(h types.Hash) {
		if h == r.hashes[1] {
			r.src.attest(r.st, r.signer, h, att1)
			readyDuring = r.st.HasSenders(h)
		}
	})
	if o := r.hedge.Run(js[1]); o != frame.HedgeWonAttested {
		t.Fatalf("frame 1 outcome %d", o)
	}
	if !readyDuring {
		t.Fatal("the attestation did not make the frame ready while the recovery ran")
	}
	for i, want := range [][]types.Address{att0, att1} {
		ss := r.st.Senders(r.hashes[i])
		if ss == nil || !equalAddrs(ss.Addrs, want) {
			t.Fatalf("frame %d senders differ from the attested ones", i)
		}
		if info := r.st.RecoverInfoOf(r.hashes[i]); info.Running {
			t.Fatalf("frame %d still running", i)
		}
	}
	if a, l := readyCounts(r.st); a != 2 || l != 0 {
		t.Fatalf("ready counts attested %d local %d, want 2 and 0", a, l)
	}
	if st := r.st.Stats(); st.WithSenders != 2 {
		t.Fatalf("with senders %d", st.WithSenders)
	}
	s := r.hedge.Stats()
	if s.Hedged != 1 || s.WonLocal != 0 || s.WonAttested != 2 {
		t.Fatalf("stats %+v", s)
	}
	if len(r.src.recovered) != 0 {
		t.Fatal("a discarded recovery was reported to the source")
	}
}

// TestHeadHedgeNoWorkWhenReady: with the head ready (attested), the hedge
// does nothing.
func TestHeadHedgeNoWorkWhenReady(t *testing.T) {
	r := newHedgeRig(t, 3, 4, 0, 4)
	for i := range r.hashes {
		r.src.attest(r.st, r.signer, r.hashes[i], mustRecover(t, r.signer, r.raws[i]))
	}
	r.now = r.now.Add(time.Second)
	if wait := r.hedge.Evaluate(); wait != 0 {
		t.Fatalf("wait %v", wait)
	}
	if js := r.jobs(); len(js) != 0 {
		t.Fatalf("dispatched %d jobs for a ready head", len(js))
	}
	if s := r.hedge.Stats(); s.Dispatched != 0 || s.Hedged != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// TestHeadHedgeSkipsInFlight: frames a builder does not pick (held by an
// unwritten block) are not the head; the hedge counts K past them.
func TestHeadHedgeSkipsInFlight(t *testing.T) {
	r := newHedgeRig(t, 3, 1, 0, 4)
	r.hedge.SetSkip(func(h types.Hash) bool { return h == r.hashes[0] })
	r.now = r.now.Add(time.Second)
	r.hedge.Evaluate()
	if js := r.jobs(); len(js) != 1 || js[0].H != r.hashes[1] {
		t.Fatalf("dispatched %v, want frame 1", js)
	}
}

// TestHeadHedgePickUnchanged: frames the hedge made ready are scanned in
// the same order, all ready, with the same senders as when attestations
// made them ready (the switch-off path).
func TestHeadHedgePickUnchanged(t *testing.T) {
	hedged := newHedgeRig(t, 4, 4, 0, 4)
	hedged.now = hedged.now.Add(time.Second)
	hedged.hedge.Evaluate()
	for _, j := range hedged.jobs() {
		hedged.hedge.Run(j)
	}
	attested := newHedgeRig(t, 4, 4, 0, 4)
	for i := range attested.hashes {
		attested.src.attest(attested.st, attested.signer, attested.hashes[i], mustRecover(t, attested.signer, attested.raws[i]))
	}
	o1, r1 := fillableOrder(hedged.st)
	o2, r2 := fillableOrder(attested.st)
	if len(o1) != 4 || len(o1) != len(o2) {
		t.Fatalf("scanned %d and %d frames", len(o1), len(o2))
	}
	for i := range o1 {
		if o1[i] != o2[i] || !r1[i] || !r2[i] {
			t.Fatalf("position %d: %x ready %v vs %x ready %v", i, o1[i][:4], r1[i], o2[i][:4], r2[i])
		}
		a, b := hedged.st.Senders(o1[i]), attested.st.Senders(o2[i])
		if !equalAddrs(a.Addrs, b.Addrs) {
			t.Fatalf("frame %d senders differ", i)
		}
	}
}

// TestHeadHedgeEventDriven: started, the hedge reacts to admissions and
// the grace timer alone (no periodic scan): a frame whose attestation
// never comes is ready shortly after the grace.
func TestHeadHedgeEventDriven(t *testing.T) {
	keys := frametest.Keys(t, 5, 29)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 3, 8)
	signer := transaction.LatestSignerForChainID(recoverChainID)
	st := store.New(1 << 24)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 2)
	rec.SetSource(&attestSource{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec.Start(ctx)
	defer rec.Stop()
	hedge := frame.NewHeadHedge(rec, 2, 5*time.Millisecond, 2)
	hedge.Start(ctx)
	defer hedge.Stop()
	for i := range raws {
		st.Admit(hashes[i], store.Entry{Raw: raws[i]}, false)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		all := true
		for _, h := range hashes {
			all = all && st.HasSenders(h)
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready: stats %+v", hedge.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	if s := hedge.Stats(); s.WonLocal != 3 {
		t.Fatalf("stats %+v", s)
	}
}

func equalAddrs(a, b []types.Address) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
