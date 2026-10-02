package initialsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/utils"
)

// TestBlocksFetcherStartStopHandlesRequest drives start/loop/handleRequest end
// to end with MinSyncPeers=0 (so waitForMinimumPeers returns immediately) and
// no connected peers, which deterministically yields errNoPeersAvailable.
func TestBlocksFetcherStartStopHandlesRequest(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: chain,
		p2p:   p2pStub,
	})
	t.Cleanup(f.stop)

	if err := f.start(); err != nil {
		t.Fatalf("start() error = %v", err)
	}

	if err := f.scheduleRequest(context.Background(), uint256.NewInt(0), 10); err != nil {
		t.Fatalf("scheduleRequest() error = %v", err)
	}

	select {
	case resp := <-f.requestResponses():
		if resp.err != errNoPeersAvailable {
			t.Fatalf("expected errNoPeersAvailable, got %v", resp.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fetch response")
	}
}

// TestBlocksFetcherStartAfterCancelErrors exercises the early-return branch
// of start() when the fetcher's context is already done.
func TestBlocksFetcherStartAfterCancelErrors(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: chain,
		p2p:   p2pStub,
	})
	f.cancel()
	close(f.quit) // loop never started, satisfy stop()'s <-f.quit
	t.Cleanup(func() {
		if f.rateLimiter != nil {
			f.rateLimiter.Free()
		}
	})

	if err := f.start(); err != errFetcherCtxIsDone {
		t.Fatalf("start() error = %v, want errFetcherCtxIsDone", err)
	}
}

// TestBlocksFetcherHandleRequestContextDone covers the ctx.Err() early
// return inside handleRequest.
func TestBlocksFetcherHandleRequestContextDone(t *testing.T) {
	f := newTestBlocksFetcher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp := f.handleRequest(ctx, uint256.NewInt(0), 1)
	if resp.err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", resp.err)
	}
}

// TestBlocksFetcherFetchBlocksFromPeerAllFail drives fetchBlocksFromPeer
// through requestBlocks against a peer whose Send always errors, verifying
// the loop over peers exhausts and returns errNoPeersAvailable.
func TestBlocksFetcherFetchBlocksFromPeerAllFail(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})
	p2pStub.sendErr = errNoPeersAvailable

	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: chain,
		p2p:   p2pStub,
	})
	defer f.cancel()
	defer f.rateLimiter.Free()

	pid := p2pStub.self
	blocks, gotPid, err := f.fetchBlocksFromPeer(context.Background(), uint256.NewInt(0), 1, []peer.ID{pid})
	if err != errNoPeersAvailable {
		t.Fatalf("expected errNoPeersAvailable, got %v", err)
	}
	if blocks != nil || gotPid != "" {
		t.Fatalf("expected zero-value results on failure, got blocks=%v pid=%v", blocks, gotPid)
	}
}

// TestBlocksFetcherRequestBlocksContextDone covers the ctx.Err() guard in
// requestBlocks.
func TestBlocksFetcherRequestBlocksContextDone(t *testing.T) {
	f := newTestBlocksFetcher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := &sync_pb.BodiesByRangeRequest{StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(0)), Count: 1, Step: 1}
	if _, err := f.requestBlocks(ctx, req, "peer-x"); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestBlocksFetcherWaitForBandwidthReturnsEarly exercises the fast path where
// remaining capacity already satisfies the requested count.
func TestBlocksFetcherWaitForBandwidthReturnsEarly(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})
	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{chain: chain, p2p: p2pStub})
	defer f.cancel()
	defer f.rateLimiter.Free()

	if err := f.waitForBandwidth("peer-x", 1); err != nil {
		t.Fatalf("waitForBandwidth() error = %v, want nil (ample capacity)", err)
	}
}

// TestBlocksFetcherWaitForBandwidthCtxDone exercises the fetcher-context-done
// branch of waitForBandwidth by draining the rate limiter below the
// requested count and cancelling the fetcher's context beforehand.
func TestBlocksFetcherWaitForBandwidthCtxDone(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})
	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{chain: chain, p2p: p2pStub})
	defer f.rateLimiter.Free()

	// Drain to a small, comfortably-positive remainder (not flush to zero):
	// LeakyBucket.Count() rounds up with math.Ceil, which can transiently
	// overshoot by one unit and make Remaining() negative right at the
	// boundary. A small margin keeps this test deterministic; see the
	// defect note in the final report regarding waitForBandwidth's
	// uint64(rem) >= count fast-path check.
	var pid peer.ID = "peer-x"
	// waitForBandwidth looks up the rate limiter bucket by pid.String(),
	// which is not the same as the raw peer.ID bytes for libp2p peer IDs;
	// use the same key here so the drained bucket is the one it reads.
	f.rateLimiter.Add(pid.String(), int64(f.rateLimiter.Capacity())-10)

	// Ask for a modest multiple of capacity so the computed wait is a few
	// seconds out (NOTE: a much larger ask, e.g. capacity*1_000_000,
	// overflows timeToWait's `int64(timeTillEmpty) * blocksNeeded` product
	// and yields a bogus near-zero/negative wait — see the defect note in
	// the final report). waitForBandwidth is started first so its select
	// is already parked on the (far from due) timer; cancelling afterwards
	// makes ctx.Done() the only channel that becomes ready, avoiding a
	// select race between two simultaneously-ready channels.
	errCh := make(chan error, 1)
	go func() {
		errCh <- f.waitForBandwidth(pid, int64ToUint64(f.rateLimiter.Capacity())*2)
	}()

	time.Sleep(50 * time.Millisecond)
	f.cancel()

	select {
	case err := <-errCh:
		if err != errFetcherCtxIsDone {
			t.Fatalf("waitForBandwidth() error = %v, want errFetcherCtxIsDone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for waitForBandwidth to observe context cancellation")
	}
}

func int64ToUint64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
