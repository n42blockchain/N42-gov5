// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	gosync "sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// Catch-up pipeline (S2) on the in-process harness.

func newPipelineNet(t *testing.T, mode syncanchor.Mode, local []block.IBlock) (*harnessNet, *fakeAnchorVerifier) {
	t.Helper()
	t.Cleanup(setPipelineForTest(true))
	net, fv := newAnchorNet(t, mode, local)
	if mode == syncanchor.Off {
		net.svc.cfg.blockImportNotifier = nil
	}
	return net, fv
}

// runPipeline drives enqueueCatchUpNow the way CatchUpTo does and returns
// when it is done.
func runPipeline(net *harnessNet, target uint64) {
	net.svc.enqueueCatchUpNow(target)
}

func assertNoScores(t *testing.T, net *harnessNet, peers ...*harnessPeer) {
	t.Helper()
	for _, p := range peers {
		if got := net.badResponses(p); got != 0 {
			t.Fatalf("peer %s scored %d", shortPeer(p.id), got)
		}
	}
}

// Slots spread over peers of different speeds; the importer reaches the
// target in order; no honest peer is scored; every height executes once.
func TestPipelineParallelPeersDifferentSpeeds(t *testing.T) {
	for _, mode := range []syncanchor.Mode{syncanchor.Off, syncanchor.Observe, syncanchor.Enforce} {
		t.Run(mode.String(), func(t *testing.T) {
			all := anchorChain(700, allVerified)
			net, _ := newPipelineNet(t, mode, all[:101])
			var ps []*harnessPeer
			for i := 0; i < 6; i++ {
				p := net.addPeer(700, harnessHonest, all...)
				p.rtt = time.Duration(1+i) * time.Millisecond
				p.perBlock = time.Duration(50*(i+1)) * time.Microsecond
				ps = append(ps, p)
			}
			runPipeline(net, 700)
			want := uint64(700)
			if mode == syncanchor.Enforce {
				want = 698 // 699 and 700 wait for an anchor above them
			}
			if got := net.chain.headNumber(); got != want {
				t.Fatalf("head = %d, want %d", got, want)
			}
			asked := 0
			for _, p := range ps {
				if p.askedCount() > 0 {
					asked++
				}
			}
			if asked < 3 {
				t.Fatalf("only %d peers served slots", asked)
			}
			assertNoScores(t, net, ps...)
			for h, n := range net.chain.perHash {
				if n != 1 {
					t.Fatalf("block %s executed %d times", h.Hex()[:10], n)
				}
			}
		})
	}
}

func gaugeValue(g interface{ Write(*dto.Metric) error }) float64 {
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

func withPipelineConfig(t *testing.T, c pipelineConfig) {
	t.Helper()
	saved := pipelineCfg
	pipelineCfg = c
	t.Cleanup(func() { pipelineCfg = saved })
}

// Short (prefix) replies, busy refusals, empty answers and a peer that hangs
// are tolerated: the gap closes from the others, nobody is scored for them,
// and the refusing peers are only backed off.
func TestPipelinePrefixRepliesAndRefusalsNotScored(t *testing.T) {
	all := anchorChain(500, allVerified)
	net, _ := newPipelineNet(t, syncanchor.Observe, all[:101])
	short1 := net.addPeer(500, harnessHonest, all...)
	short1.maxServe = 7
	short2 := net.addPeer(500, harnessHonest, all...)
	short2.maxServe = 11
	busy := net.addPeer(500, harnessRefuse)
	empty := net.addPeer(500, harnessEmpty)
	hang := net.addPeer(500, harnessHang)
	hang.hangFor = 300 * time.Millisecond
	honest := net.addPeer(500, harnessHonest, all...)
	before := metricValue(syncPipelineImported)
	runPipeline(net, 500)
	if got := net.chain.headNumber(); got != 500 {
		t.Fatalf("head = %d, want 500", got)
	}
	if d := metricValue(syncPipelineImported) - before; d != 400 {
		t.Fatalf("pipeline imported %v blocks, want 400 (did the pipeline run?)", d)
	}
	assertNoScores(t, net, short1, short2, busy, empty, hang, honest)
	if busy.askedCount() == 0 || empty.askedCount() == 0 {
		t.Fatal("refusing peers never asked; the test exercises nothing")
	}
	if short1.askedCount()+short2.askedCount() == 0 {
		t.Fatal("prefix-serving peers never asked")
	}
}

// A slow peer keeps at most a single slot in flight and loses its share to
// faster peers; a malformed reply is a provable fault and its slot is
// re-assigned.
func TestPipelineSlowAndFailedPeersReassigned(t *testing.T) {
	all := anchorChain(800, allVerified)
	net, _ := newPipelineNet(t, syncanchor.Observe, all[:101])
	slow := net.addPeer(800, harnessHonest, all...)
	slow.perBlock = 15 * time.Millisecond
	bad := net.addPeer(800, harnessMalformed)
	var fast []*harnessPeer
	for i := 0; i < 3; i++ {
		p := net.addPeer(800, harnessHonest, all...)
		p.perBlock = 50 * time.Microsecond
		fast = append(fast, p)
	}
	runPipeline(net, 800)
	if got := net.chain.headNumber(); got != 800 {
		t.Fatalf("head = %d, want 800", got)
	}
	if n := slow.askedCount(); n > 3 {
		t.Fatalf("slow peer asked %d times, want <= 3", n)
	}
	fastAsked := 0
	for _, p := range fast {
		fastAsked += p.askedCount()
	}
	if fastAsked < 8 {
		t.Fatalf("fast peers asked %d times in total", fastAsked)
	}
	if bad.askedCount() > 0 && net.badResponses(bad) == 0 {
		t.Fatal("malformed reply not scored")
	}
	assertNoScores(t, net, append(fast, slow)...)
}

// Slots answered with very different latencies arrive out of order; the
// importer still executes strictly in order and never falls back.
func TestPipelineOutOfOrderArrivalImportsInOrder(t *testing.T) {
	all := anchorChain(600, allVerified)
	net, _ := newPipelineNet(t, syncanchor.Enforce, all[:101])
	for i := 0; i < 5; i++ {
		p := net.addPeer(600, harnessHonest, all...)
		p.rtt = time.Duration(60-12*i) * time.Millisecond
	}
	fb := metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackFork)) +
		metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackInsert)) +
		metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackEvicted))
	runPipeline(net, 600)
	if got := net.chain.headNumber(); got != 598 {
		t.Fatalf("head = %d, want 598", got)
	}
	fb2 := metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackFork)) +
		metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackInsert)) +
		metricValue(syncPipelineFallbacks.WithLabelValues(pipeFallbackEvicted))
	if fb2 != fb {
		t.Fatalf("the pipeline fell back %v times", fb2-fb)
	}
	if net.chain.switches != 0 {
		t.Fatal("the pipeline used branch-switch authority")
	}
}

// The reorder buffer stays within its block bound (plus the cursor block)
// while a slow importer back-pressures fast peers.
func TestPipelineBufferBoundedUnderBackPressure(t *testing.T) {
	withPipelineConfig(t, pipelineConfig{slotBlocks: 8, slotBytes: 64 << 20, bufferBlocks: 32, bufferBytes: 2 << 30, peerInflight: 2})
	all := anchorChain(400, allVerified)
	net, _ := newPipelineNet(t, syncanchor.Observe, all[:101])
	net.chain.insertDelay = time.Millisecond
	for i := 0; i < 4; i++ {
		net.addPeer(400, harnessHonest, all...)
	}
	stop := make(chan struct{})
	maxSeen := 0.0
	var wg gosync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if v := gaugeValue(syncPipelineBufferBlocks); v > maxSeen {
				maxSeen = v
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	runPipeline(net, 400)
	close(stop)
	wg.Wait()
	if got := net.chain.headNumber(); got != 400 {
		t.Fatalf("head = %d, want 400", got)
	}
	if maxSeen > 33 {
		t.Fatalf("buffer held %v blocks, bound 32 (+1 cursor block)", maxSeen)
	}
	if maxSeen < 2 {
		t.Fatal("the buffer never filled; the test exercises nothing")
	}
}

// Enforce: a lying peer's forged slots never execute while honest slots
// proceed; forged headers with invalid QCs are scored, a forged chain
// without QCs is only dropped.
func TestPipelineEnforceForgedSlotRejected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      func(uint64) byte
		wantScore bool
	}{
		{"invalid QCs", func(uint64) byte { return fakeQCBad }, true},
		{"no QCs", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedStall := pipeStallTimeout
			pipeStallTimeout = 300 * time.Millisecond
			t.Cleanup(func() { pipeStallTimeout = savedStall })
			all := anchorChain(400, allVerified)
			net, _ := newPipelineNet(t, syncanchor.Enforce, all[:101])
			// The liar connects first and is alone for one round, so its
			// forged slots certainly reach the importer.
			forged := anchorExtend(all[:101], 1000, 0xf0, tc.kind)
			liar := net.addPeer(1_000_000_000, harnessHonest, append(append([]block.IBlock{}, all[:101]...), forged...)...)
			runPipeline(net, 400)
			if got := net.chain.headNumber(); got != 100 {
				t.Fatalf("head after the liar's round = %d, want 100", got)
			}
			var honest []*harnessPeer
			for i := 0; i < 6; i++ {
				honest = append(honest, net.addPeer(400, harnessHonest, all...))
			}
			for round := 0; round < 5 && net.chain.headNumber() < 398; round++ {
				runPipeline(net, 400)
			}
			if got := net.chain.headNumber(); got != 398 {
				t.Fatalf("head = %d, want 398", got)
			}
			net.chain.mu.Lock()
			for _, f := range forged {
				if net.chain.appliedLocked(f.Hash()) || net.chain.perHash[f.Hash()] > 0 {
					net.chain.mu.Unlock()
					t.Fatalf("forged block %d executed", f.Number64().Uint64())
				}
			}
			net.chain.mu.Unlock()
			if liar.askedCount() == 0 {
				t.Fatal("the liar was never asked; the test exercises nothing")
			}
			if got := net.badResponses(liar); (got > 0) != tc.wantScore {
				t.Fatalf("liar score = %d, want scored=%v", got, tc.wantScore)
			}
			assertNoScores(t, net, honest...)
		})
	}
}
