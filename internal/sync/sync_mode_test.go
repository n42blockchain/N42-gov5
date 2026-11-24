// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	gosync "sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

func newModeMachine() *syncModeMachine {
	m := &syncModeMachine{}
	m.initLocked()
	return m
}

// Live -> Range needs the gap above the live window at 2 consecutive
// commits or for 3 s; a single spike does nothing.
func TestSyncModeEntersRangeWithHysteresis(t *testing.T) {
	t0 := time.Now()
	m := newModeMachine()
	if mode, ch := m.observe(50, true, t0); mode != syncModeLive || ch {
		t.Fatal("one commit over the window entered Range")
	}
	if mode, _ := m.observe(5, true, t0.Add(100*time.Millisecond)); mode != syncModeLive {
		t.Fatal("left Live on a small gap")
	}
	m.observe(50, true, t0.Add(200*time.Millisecond))
	if mode, ch := m.observe(50, true, t0.Add(300*time.Millisecond)); mode != syncModeRange || !ch {
		t.Fatal("two consecutive commits over the window did not enter Range")
	}

	// Time rule: no commits (an observer), gap over the window for 3 s.
	m = newModeMachine()
	m.observe(20, false, t0)
	if mode, _ := m.observe(20, false, t0.Add(2900*time.Millisecond)); mode != syncModeLive {
		t.Fatal("entered Range before the dwell")
	}
	if mode, ch := m.observe(20, false, t0.Add(3*time.Second)); mode != syncModeRange || !ch {
		t.Fatal("dwell over the window did not enter Range")
	}
}

// A gap jittering around the thresholds never flaps the mode.
func TestSyncModeNoFlappingOnJitter(t *testing.T) {
	t0 := time.Now()
	m := newModeMachine()
	// Between exitGap and enterGap: Live stays Live forever.
	for i := 0; i < 200; i++ {
		gap := uint64(3 + i%6) // 3..8
		if mode, ch := m.observe(gap, i%2 == 0, t0.Add(time.Duration(i)*100*time.Millisecond)); mode != syncModeLive || ch {
			t.Fatalf("step %d (gap %d): left Live", i, gap)
		}
	}
	// Alternating spikes never give 2 consecutive commits or 3 s above.
	for i := 0; i < 200; i++ {
		gap := uint64(3)
		if i%2 == 0 {
			gap = 12
		}
		if mode, _ := m.observe(gap, true, t0.Add(time.Duration(i)*time.Second)); mode != syncModeLive {
			t.Fatalf("step %d: alternating spikes entered Range", i)
		}
	}
	// In Range, a gap jittering 1..5 never leaves it.
	m.observe(40, true, t0)
	m.observe(40, true, t0)
	if m.current() != syncModeRange {
		t.Fatal("setup: not in Range")
	}
	for i := 0; i < 200; i++ {
		gap := uint64(1)
		if i%2 == 1 {
			gap = 5
		}
		if mode, ch := m.observe(gap, true, t0.Add(time.Duration(i)*time.Second)); mode != syncModeRange || ch {
			t.Fatalf("step %d: jitter around exitGap left Range", i)
		}
	}
}

// Range -> Live: 3 consecutive commits within the exit gap after the minimum
// dwell, or the exit gap held for 2 s without commits (a stalled chain).
func TestSyncModeLeavesRange(t *testing.T) {
	t0 := time.Now()
	m := newModeMachine()
	m.observe(40, true, t0)
	m.observe(40, true, t0)
	for i := 1; i <= 3; i++ {
		mode, _ := m.observe(1, true, t0.Add(time.Duration(i)*100*time.Millisecond))
		if mode != syncModeRange {
			t.Fatal("left Range before the minimum dwell")
		}
	}
	if mode, ch := m.observe(0, true, t0.Add(2100*time.Millisecond)); mode != syncModeLive || !ch {
		t.Fatal("did not leave Range after the dwell and 4 small-gap commits")
	}

	m = newModeMachine()
	m.observe(40, true, t0)
	m.observe(40, true, t0)
	m.observe(2, false, t0.Add(1*time.Second))
	if mode, _ := m.observe(2, false, t0.Add(2900*time.Millisecond)); mode != syncModeRange {
		t.Fatal("left Range before the exit dwell")
	}
	if mode, ch := m.observe(2, false, t0.Add(3100*time.Millisecond)); mode != syncModeLive || !ch {
		t.Fatal("a stalled chain kept a caught-up node in Range")
	}
}

// forceRange puts svc in Range mode owning heights up to rangeEnd, with a
// dwell long enough that no observation leaves it during the test.
func forceRange(svc *Service, rangeEnd uint64) {
	m := &svc.syncModes
	m.mu.Lock()
	m.initLocked()
	m.p.minRangeDwell = time.Hour
	m.p.exitDwell = time.Hour
	m.enterLocked(syncModeRange, time.Now())
	m.mu.Unlock()
	svc.rangeEnd.Store(rangeEnd)
}

type countingNotifier struct {
	*fakeAnchorVerifier
	mu       gosync.Mutex
	imported map[comtypes.Hash]int
}

func (c *countingNotifier) NotifyBlockImported(h comtypes.Hash, _ comtypes.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.imported == nil {
		c.imported = map[comtypes.Hash]int{}
	}
	c.imported[h]++
}

func (c *countingNotifier) count(h comtypes.Hash) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.imported[h]
}

// Ownership exists only with the pipeline on, in Range, at or below the
// range end.
func TestSyncOwnsHeight(t *testing.T) {
	net := newHarnessNet(t, newHarnessChain(harnessBase(10)...))
	svc := net.svc
	defer setPipelineForTest(false)()
	forceRange(svc, 100)
	if svc.syncOwnsHeight(50) || svc.SyncProductionPaused() {
		t.Fatal("pipeline off: owned a height or paused production")
	}
	setPipelineForTest(true)
	if !svc.syncOwnsHeight(100) || svc.syncOwnsHeight(101) {
		t.Fatal("Range ownership boundary wrong")
	}
	if !svc.SyncProductionPaused() {
		t.Fatal("Range mode did not pause production")
	}
	svc.syncModes.mu.Lock()
	svc.syncModes.enterLocked(syncModeLive, time.Now())
	svc.syncModes.mu.Unlock()
	if svc.syncOwnsHeight(50) || svc.SyncProductionPaused() {
		t.Fatal("Live mode owned a height or paused production")
	}
}

// In Range a pushed block at an owned height is neither imported nor
// reported to the engine (no import-gated vote); above the range end, and
// with the pipeline off, the push path is unchanged.
func TestRangeModePushLeavesOwnedHeightsToSync(t *testing.T) {
	all := anchorChain(120, allVerified)
	net, fv := newAnchorNet(t, syncanchor.Observe, all[:101])
	cn := &countingNotifier{fakeAnchorVerifier: fv}
	net.svc.cfg.blockImportNotifier = cn
	t.Cleanup(setPipelineForTest(true))
	forceRange(net.svc, 110)

	net.svc.handlePushedBlock(all[101].(*block.Block), false)
	if net.chain.headNumber() != 100 || cn.count(all[101].Hash()) != 0 {
		t.Fatal("Range: the push path imported or reported an owned height")
	}
	setPipelineForTest(false)
	net.svc.handlePushedBlock(all[101].(*block.Block), false)
	if net.chain.headNumber() != 101 || cn.count(all[101].Hash()) != 1 {
		t.Fatal("pipeline off: the push path changed")
	}
}

// Handover: while the sync importer runs in Range, the same heights arrive
// by push concurrently. Every height executes exactly once and the engine
// hears about each block once.
func TestRangeModeNoDoubleImportWithLivePath(t *testing.T) {
	all := anchorChain(400, allVerified)
	net, fv := newAnchorNet(t, syncanchor.Observe, all[:101])
	cn := &countingNotifier{fakeAnchorVerifier: fv}
	net.svc.cfg.blockImportNotifier = cn
	net.chain.insertDelay = 200 * time.Microsecond
	t.Cleanup(setPipelineForTest(true))
	for i := 0; i < 4; i++ {
		net.addPeer(400, harnessHonest, all...)
	}
	forceRange(net.svc, 400)

	var wg gosync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 101; n <= 400; n++ {
			net.svc.handlePushedBlock(all[n].(*block.Block), false)
		}
	}()
	runPipeline(net, 400)
	wg.Wait()
	if got := net.chain.headNumber(); got != 400 {
		t.Fatalf("head = %d, want 400", got)
	}
	for n := 101; n <= 400; n++ {
		h := all[n].Hash()
		if e := net.chain.perHash[h]; e != 1 {
			t.Fatalf("height %d executed %d times", n, e)
		}
		if c := cn.count(h); c != 1 {
			t.Fatalf("height %d reported %d times", n, c)
		}
	}
	if skipped := metricValue(syncPipelineLiveSkipped.WithLabelValues("push")); skipped < 1 {
		t.Fatal("no push was left to the sync importer; the test exercises nothing")
	}
}
