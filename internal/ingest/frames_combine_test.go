// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ingest

import (
	"bytes"
	"crypto/ecdsa"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// gwScript is one gateway's input: frame seqs in send order, with gaps
// (a seq two ahead) and resends of earlier seqs.
func gwScript(n int, rng *rand.Rand) []uint64 {
	var out []uint64
	seq := uint64(1)
	for len(out) < n {
		switch rng.IntN(8) {
		case 0:
			out = append(out, seq+2)
		case 1:
			if seq > 1 {
				out = append(out, 1+rng.Uint64N(seq-1))
			}
		default:
			out = append(out, seq)
			seq++
		}
	}
	return out
}

func newFrameSrv(t testing.TB, gws []*ecdsa.PrivateKey, st *store.Store) *Server {
	t.Helper()
	addrs := make([]types.Address, len(gws))
	for i, k := range gws {
		addrs[i] = crypto.PubkeyToAddress(k.PublicKey)
	}
	srv := NewServer("127.0.0.1:0", &mockTxPool{}, 1<<30, 1<<30)
	if err := srv.EnableFrames(FrameConfig{Gateways: addrs, Store: st}); err != nil {
		t.Fatal(err)
	}
	return srv
}

func replyOf(t testing.TB, srv *Server, raw []byte) byte {
	t.Helper()
	reply, err := srv.readFrame(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return reply[0]
}

// TestFrameIngestCombinedReplies: with N42_FRAME_ADMIT_COMBINE each
// gateway, sent concurrently with the others, gets exactly the reply
// sequence (accepted / busy for a gap / duplicate) the serial path gives
// the same input, and the counters match.
func TestFrameIngestCombinedReplies(t *testing.T) {
	const gwN, steps = 6, 120
	txs, _ := signedTxs(t, 2, 2)
	rng := rand.New(rand.NewPCG(7, 8))
	gws := make([]*ecdsa.PrivateKey, gwN)
	scripts := make([][]uint64, gwN)
	frames := make([]map[uint64][]byte, gwN)
	for g := range gws {
		gws[g], _ = crypto.GenerateKey()
		scripts[g] = gwScript(steps, rng)
		frames[g] = map[uint64][]byte{}
		for _, seq := range scripts[g] {
			if frames[g][seq] == nil {
				raw, _, err := frame.Encode(gws[g], seq, frame.TxTypeEthereum, txs)
				if err != nil {
					t.Fatal(err)
				}
				frames[g][seq] = raw
			}
		}
	}
	run := func(combine, parallel bool) ([][]byte, FrameStats) {
		defer store.SetAdmitCombineForTest(combine)()
		srv := newFrameSrv(t, gws, store.New(1<<30))
		out := make([][]byte, gwN)
		var wg sync.WaitGroup
		for g := range gws {
			one := func() {
				for _, seq := range scripts[g] {
					out[g] = append(out[g], replyOf(t, srv, frames[g][seq]))
				}
			}
			if parallel {
				wg.Add(1)
				go func() { defer wg.Done(); one() }()
			} else {
				one()
			}
		}
		wg.Wait()
		return out, srv.FrameStats()
	}
	want, ws := run(false, false)
	for _, parallel := range []bool{false, true} {
		got, gs := run(true, parallel)
		for g := range want {
			if !slices.Equal(got[g], want[g]) {
				t.Fatalf("parallel=%v gateway %d: replies\n got %v\nwant %v", parallel, g, got[g], want[g])
			}
		}
		if gs.Accepted != ws.Accepted || gs.Duplicate != ws.Duplicate || gs.OutOfOrder != ws.OutOfOrder ||
			gs.Busy != ws.Busy || gs.Store.Frames != ws.Store.Frames {
			t.Fatalf("parallel=%v: stats\n got %+v\nwant %+v", parallel, gs, ws)
		}
	}
	if ws.OutOfOrder == 0 || ws.Duplicate == 0 {
		t.Fatalf("script did not exercise gaps and resends: %+v", ws)
	}
}

// TestFrameIngestCombinedBusy: a full store answers Busy through the
// combined path as through the serial one, and a committed frame's resend
// is a duplicate.
func TestFrameIngestCombinedBusy(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(t, 2, 2)
	var raws [][]byte
	for seq := uint64(1); seq <= 4; seq++ {
		raw, _, _ := frame.Encode(gw, seq, frame.TxTypeEthereum, txs)
		raws = append(raws, raw)
	}
	run := func(combine bool) []byte {
		defer store.SetAdmitCombineForTest(combine)()
		budget := int64(len(raws[0]) + len(raws[1]) + len(raws[2]) + 2)
		st := store.NewWithKeep(budget, 0)
		srv := newFrameSrv(t, []*ecdsa.PrivateKey{gw}, st)
		var out []byte
		for i := 0; i < 4; i++ {
			out = append(out, replyOf(t, srv, raws[i]))
		}
		var first types.Hash
		st.Scan(func(h types.Hash, _ store.Entry) bool { first = h; return false })
		st.Include([]types.Hash{first}, 5)
		st.Commit(5, []types.Hash{first})
		out = append(out, replyOf(t, srv, raws[3]), replyOf(t, srv, raws[0]))
		return out
	}
	want := run(false)
	if got := run(true); !slices.Equal(got, want) {
		t.Fatalf("replies\n got %v\nwant %v", got, want)
	}
	if want[3] != FrameBusy || want[4] != FrameAccepted || want[5] != FrameDuplicate {
		t.Fatalf("serial replies %v", want)
	}
}

// BenchmarkFrameAdmitMix reproduces the bench's store mix on one store:
// 14 FRM1 admitters (readFrame: decode, keccak + ecrecover, gas estimate,
// admission) on 1 or 4 gateways, 32 admission-recovery workers (3 of 7
// frames recovered here, the rest left to attestations), attestation
// batches, and a block every 50 ms (builder scan, decode senders, resolve,
// claim, include, commit with keep 16). It reports admitted frames/s with
// the r93 switches off and on.
//
//	GOMAXPROCS=16 go test -tags nosqlite,noboltdb ./internal/ingest/ -run '^$' -bench FrameAdmitMix -benchtime 30000x
func BenchmarkFrameAdmitMix(b *testing.B) {
	modes := []struct {
		name                 string
		combine, batch, scan bool
	}{
		{"off", false, false, false},
		{"combine", true, false, false},
		{"batch", false, true, false},
		{"scan", false, false, true},
		{"all", true, true, true},
	}
	txs, _ := signedTxs(b, 10, 10)
	for _, gwN := range []int{1, 4} {
		for _, m := range modes {
			b.Run(fmt.Sprintf("gw%d/%s", gwN, m.name), func(b *testing.B) {
				defer store.SetAdmitCombineForTest(m.combine)()
				defer store.SetStoreBatchForTest(m.batch)()
				defer store.SetScanFastForTest(m.scan)()
				runAdmitMix(b, gwN, txs)
			})
		}
	}
}

type mixRecoverer struct {
	jobs    chan types.Hash
	pending atomic.Int64
}

func (r *mixRecoverer) Submit(h types.Hash, _ []byte) {
	r.pending.Add(1)
	select {
	case r.jobs <- h:
	default:
		r.pending.Add(-1)
	}
}
func (r *mixRecoverer) Pending() int64    { return r.pending.Load() }
func (r *mixRecoverer) Recovered() uint64 { return 0 }

func spin(d time.Duration) {
	for t0 := time.Now(); time.Since(t0) < d; {
	}
}

func runAdmitMix(b *testing.B, gwN int, txs [][]byte) {
	const (
		admitters    = 14
		recoverers   = 32
		recoverWork  = 20 * time.Microsecond
		blockEvery   = 50 * time.Millisecond
		blockFrames  = 4000
		attestBatch  = 64
		attestFlush  = 20 * time.Millisecond
		decodeWorker = 8
	)
	batch := store.StoreBatchOn()
	gws := make([]*ecdsa.PrivateKey, gwN)
	for i := range gws {
		gws[i], _ = crypto.GenerateKey()
	}
	st := store.NewWithKeep(1<<30, 16)
	rec := &mixRecoverer{jobs: make(chan types.Hash, 1<<16)}
	st.SetRecoverer(rec)
	srv := newFrameSrv(b, gws, st)
	// Each gateway's first frame sets its last seq high; every later seq is
	// lower (a resend as far as the in-order check goes), so all checks run
	// and none refuses.
	for _, k := range gws {
		raw, _, _ := frame.Encode(k, 1<<62, frame.TxTypeEthereum, txs)
		if r := replyOf(b, srv, raw); r != FrameAccepted {
			b.Fatalf("warmup reply %d", r)
		}
	}
	done := make(chan struct{})
	var bg sync.WaitGroup
	attestQ := make(chan types.Hash, 1<<16)
	// Admission recovery (recoverOne's store calls).
	for w := 0; w < recoverers; w++ {
		bg.Add(1)
		go func() {
			defer bg.Done()
			for {
				var h types.Hash
				select {
				case <-done:
					return
				case h = <-rec.jobs:
				}
				committee := h[0]%7 < 3
				if batch && !committee {
					attestQ <- h
					rec.pending.Add(-1)
					continue
				}
				var t store.RecoverTicket
				if batch {
					t = st.BeginRecoverCombined(h, true)
				} else {
					t = st.BeginRecover(h, true)
				}
				if t.Own {
					var ss *store.Senders
					if committee {
						spin(recoverWork)
						ss = &store.Senders{}
						st.RecoverInfoOf(h) // attestation stamps
					} else {
						attestQ <- h
					}
					if batch {
						st.FinishRecoverAsync(t, ss)
					} else {
						st.FinishRecover(t, ss)
					}
				}
				rec.pending.Add(-1)
			}
		}()
	}
	// Attestation batches (pool.Add's store calls).
	bg.Add(1)
	go func() {
		defer bg.Done()
		tick := time.NewTicker(attestFlush)
		defer tick.Stop()
		var hs []types.Hash
		flush := func() {
			if len(hs) == 0 {
				return
			}
			if batch {
				infos := make([]store.RecoverInfo, len(hs))
				st.RecoverInfoBatch(hs, infos)
				ss := make([]*store.Senders, len(hs))
				for i := range ss {
					ss[i] = &store.Senders{}
				}
				st.SetSendersAsync(hs, ss)
			} else {
				for _, h := range hs {
					st.RecoverInfoOf(h)
				}
				for _, h := range hs {
					st.SetSenders(h, &store.Senders{})
				}
			}
			hs = hs[:0]
		}
		for {
			select {
			case <-done:
				return
			case h := <-attestQ:
				if hs = append(hs, h); len(hs) >= attestBatch {
					flush()
				}
			case <-tick.C:
				flush()
			}
		}
	}()
	// Blocks: builder scan, decode senders, resolve, claim, include, commit.
	var blocks atomic.Int64
	bg.Add(1)
	go func() {
		defer bg.Done()
		tick := time.NewTicker(blockEvery)
		defer tick.Stop()
		var n uint64 = 1
		var prev []types.Hash
		perFrame := func(hs []types.Hash, fn func(types.Hash)) {
			var wg sync.WaitGroup
			for w := 0; w < decodeWorker; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := w; i < len(hs); i += decodeWorker {
						fn(hs[i])
					}
				}()
			}
			wg.Wait()
		}
		for {
			select {
			case <-done:
				return
			case <-tick.C:
			}
			var pick []types.Hash
			var held []types.Address
			st.ScanFillableReady(time.Second, func(h types.Hash, e store.Entry, ready bool) bool {
				gw, _ := frame.GatewayOf(e.Raw)
				if slices.Contains(held, gw) {
					return true
				}
				if !ready {
					held = append(held, gw)
					return true
				}
				pick = append(pick, h)
				return len(pick) < blockFrames
			})
			if batch {
				ss := make([]*store.Senders, len(pick))
				st.SendersBatch(pick, ss) // build decode
				raws := make([][]byte, len(pick))
				st.GetBatch(pick, raws) // resolve
				st.SendersBatch(pick, ss)
			} else {
				perFrame(pick, func(h types.Hash) { st.Senders(h) })
				perFrame(pick, func(h types.Hash) { st.Get(h) })
				perFrame(pick, func(h types.Hash) { st.Senders(h) })
			}
			st.Claim(pick)
			st.Include(pick, n)
			st.Commit(n-1, prev)
			prev = pick
			n++
			blocks.Add(1)
		}
	}()
	// Admitters: each signs its next frame (the generator's share) and
	// admits it; a Busy is retried after a short pause.
	var next atomic.Int64
	var busy atomic.Int64
	acc0 := srv.FrameStats().Accepted
	b.ResetTimer()
	start := time.Now()
	var aw sync.WaitGroup
	for a := 0; a < admitters; a++ {
		aw.Add(1)
		go func() {
			defer aw.Done()
			k := gws[a%gwN]
			for {
				i := next.Add(1)
				if i > int64(b.N) {
					return
				}
				raw, _, err := frame.Encode(k, uint64(a)<<40|uint64(i), frame.TxTypeEthereum, txs)
				if err != nil {
					b.Error(err)
					return
				}
				for {
					r, err := srv.readFrame(bytes.NewReader(raw))
					if err != nil {
						b.Error(err)
						return
					}
					if r[0] != FrameBusy {
						break
					}
					busy.Add(1)
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	aw.Wait()
	el := time.Since(start)
	b.StopTimer()
	close(done)
	bg.Wait()
	acc := srv.FrameStats().Accepted - acc0
	b.ReportMetric(float64(acc)/el.Seconds(), "frames/s")
	b.ReportMetric(float64(busy.Load()), "busy")
	b.ReportMetric(float64(blocks.Load()), "blocks")
	seqMean, seqMax, _ := srv.frames.seqWait.Take()
	stMean, stMax, _ := st.TakeAdmitLockWait()
	b.ReportMetric(float64(seqMean.Microseconds()), "seqWaitMeanUs")
	b.ReportMetric(float64(seqMax.Microseconds()), "seqWaitMaxUs")
	b.ReportMetric(float64(stMean.Microseconds()), "storeWaitMeanUs")
	b.ReportMetric(float64(stMax.Microseconds()), "storeWaitMaxUs")
}
