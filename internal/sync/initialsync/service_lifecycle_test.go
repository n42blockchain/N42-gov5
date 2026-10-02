package initialsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/conf"
)

// TestServiceStartStopAlreadySynced drives NewService/Start/Stop end to end
// with MinSyncPeers=0 and a chain already at genesis with no connected
// peers: waitForMinimumPeers returns immediately, and
// syncToFinalizedBlockNr's "already synced" branch short-circuits before
// ever touching the blocksQueue, so Start() returns quickly and Stop()
// observes a clean shutdown.
func TestServiceStartStopAlreadySynced(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	svc := NewService(context.Background(), &Config{P2P: p2pStub, Chain: chain})
	if svc.Synced() {
		t.Fatalf("expected not synced before Start()")
	}
	if !svc.Syncing() {
		t.Fatalf("expected Syncing() true before Start()")
	}

	done := make(chan struct{})
	go func() {
		svc.Start()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Start() to return")
	}

	if !svc.Synced() {
		t.Fatalf("expected Synced() true after Start() returns")
	}
	if svc.Status() != nil {
		t.Fatalf("Status() = %v, want nil once synced", svc.Status())
	}

	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

// TestServiceResyncAlreadySynced exercises Resync()'s happy path the same
// way: current block already meets the (zero) target, so roundRobinSync
// returns nil without starting a queue.
func TestServiceResyncAlreadySynced(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	svc := NewService(context.Background(), &Config{P2P: p2pStub, Chain: chain})
	svc.markSynced()

	if err := svc.Resync(); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	if !svc.Synced() {
		t.Fatalf("expected Synced() true after Resync() completes")
	}
}

// TestServiceResyncContextDone covers Resync's early ctx.Err() guard.
func TestServiceResyncContextDone(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := NewService(ctx, &Config{P2P: p2pStub, Chain: chain})

	if err := svc.Resync(); err == nil {
		t.Fatalf("expected error for already-cancelled context")
	}
}

// TestServiceWaitForMinimumPeersSkipsStandalone covers the Service-level
// waitForMinimumPeers skip-wait path (MinSyncPeers==0).
func TestServiceWaitForMinimumPeersSkipsStandalone(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(7)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 0})

	svc := NewService(context.Background(), &Config{P2P: p2pStub, Chain: chain})
	got := svc.waitForMinimumPeers()
	if got == nil || got.Uint64() != 7 {
		t.Fatalf("waitForMinimumPeers() = %v, want current block 7", got)
	}
}

// TestServiceWaitForMinimumPeersContextDone exercises the ctx.Done() early
// return in the peer-waiting loop when MinSyncPeers requires peers but none
// show up and the service context is cancelled.
func TestServiceWaitForMinimumPeersContextDone(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(3)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 1})

	ctx, cancel := context.WithCancel(context.Background())
	svc := NewService(ctx, &Config{P2P: p2pStub, Chain: chain})

	resultCh := make(chan *uint256.Int, 1)
	go func() { resultCh <- svc.waitForMinimumPeers() }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case got := <-resultCh:
		if got == nil || got.Uint64() != 3 {
			t.Fatalf("waitForMinimumPeers() = %v, want current block 3 on ctx cancellation", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for waitForMinimumPeers to observe context cancellation")
	}
}
