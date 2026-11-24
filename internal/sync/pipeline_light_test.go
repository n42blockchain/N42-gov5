// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	gosync "sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// lightHarnessChain models an E=1 light chain: InsertChain only queues the
// blocks for the lag executor and reports them all as taken (e1LightQueue);
// a stepper goroutine applies the queued block on the head when apply(n)
// allows it.
type lightHarnessChain struct {
	*harnessChain
	qmu   gosync.Mutex
	queue map[comtypes.Hash]block.IBlock
}

func (c *lightHarnessChain) InsertChain(blocks []block.IBlock) (int, error) {
	c.qmu.Lock()
	defer c.qmu.Unlock()
	for _, b := range blocks {
		c.queue[b.Hash()] = b
	}
	return len(blocks), nil
}

func (c *lightHarnessChain) InsertChainAuthorized(blocks []block.IBlock) (int, error) {
	return c.InsertChain(blocks)
}

// step applies the queued child of the head when apply allows its height.
func (c *lightHarnessChain) step(apply func(uint64) bool) {
	head := c.CurrentBlock()
	c.qmu.Lock()
	var next block.IBlock
	for h, b := range c.queue {
		if c.HasAppliedBlock(h, b.Number64().Uint64()) {
			delete(c.queue, h)
			continue
		}
		if b.ParentHash() == head.Hash() && apply(b.Number64().Uint64()) {
			next = b
			delete(c.queue, h)
			break
		}
	}
	c.qmu.Unlock()
	if next != nil {
		_, _ = c.harnessChain.insert([]block.IBlock{next}, false)
	}
}

// newLightPipelineNet is a pipeline harness on a light chain in Range mode
// whose stepper runs until the test ends.
func newLightPipelineNet(t *testing.T, local []block.IBlock, apply func(*Service, uint64) bool) *harnessNet {
	t.Helper()
	net, _ := newPipelineNet(t, syncanchor.Off, local)
	ctx, cancel := context.WithCancel(context.Background())
	net.svc.ctx = ctx
	lc := &lightHarnessChain{harnessChain: net.chain, queue: map[comtypes.Hash]block.IBlock{}}
	net.svc.cfg.chain = lc
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			lc.step(func(n uint64) bool { return apply(net.svc, n) })
			time.Sleep(200 * time.Microsecond)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return net
}

// runPipelineWithin runs the catch-up and reports whether it returned
// before the deadline.
func runPipelineWithin(net *harnessNet, target uint64, d time.Duration) bool {
	ret := make(chan struct{})
	go func() {
		defer close(ret)
		runPipeline(net, target)
	}()
	select {
	case <-ret:
		return true
	case <-time.After(d):
		return false
	}
}

// A restarted light node in Range mode: the pipeline's inserts only queue
// blocks, and the light lag executor applies them (it is the only importer,
// so Range ownership does not hold it back). The head reaches the target.
func TestPipelineLightChainReachesTarget(t *testing.T) {
	all := anchorChain(400, allVerified)
	net := newLightPipelineNet(t, all[:101], func(*Service, uint64) bool { return true })
	forceRange(net.svc, 400)
	for i := 0; i < 4; i++ {
		net.addPeer(400, harnessHonest, all...)
	}
	if !runPipelineWithin(net, 400, 20*time.Second) {
		t.Fatalf("pipeline did not return; head = %d", net.chain.headNumber())
	}
	if got := net.chain.headNumber(); got != 400 {
		t.Fatalf("head = %d, want 400", got)
	}
}

// Progress counts applied blocks only: when the light executor applies
// nothing (it leaves every Range-owned height to the sync importer, the
// pre-fix rule), the inserts that merely queue blocks are not progress, so
// the stall detector ends the run instead of re-queuing the same range
// forever (bench run 98 livelock).
func TestPipelineQueuedOnlyInsertsStallOut(t *testing.T) {
	savedStall := pipeStallTimeout
	pipeStallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pipeStallTimeout = savedStall })
	all := anchorChain(400, allVerified)
	net := newLightPipelineNet(t, all[:101], func(s *Service, n uint64) bool { return !s.syncOwnsHeight(n) })
	forceRange(net.svc, 400)
	for i := 0; i < 4; i++ {
		net.addPeer(400, harnessHonest, all...)
	}
	before := metricValue(syncPipelineImported)
	if !runPipelineWithin(net, 400, 10*time.Second) {
		t.Fatalf("pipeline never ended: queued-only inserts fed its progress (head = %d)", net.chain.headNumber())
	}
	if got := net.chain.headNumber(); got != 100 {
		t.Fatalf("head = %d, want 100", got)
	}
	if d := metricValue(syncPipelineImported) - before; d != 0 {
		t.Fatalf("pipeline counted %v imports with nothing applied", d)
	}
}
