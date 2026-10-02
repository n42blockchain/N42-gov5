package initialsync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/conf"
)

func TestNewBlocksFetcherDefaults(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := &initialSyncP2PStub{cfg: &conf.P2PConfig{MinSyncPeers: 1}}

	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain: chain,
		p2p:   p2pStub,
		mode:  modeStopOnFinalizedEpoch,
	})
	defer f.cancel()

	if f.blocksPerPeriod != 1024 {
		t.Fatalf("expected default blocksPerPeriod 1024, got %d", f.blocksPerPeriod)
	}
	if f.peerLocks == nil || f.fetchRequests == nil || f.fetchResponses == nil || f.quit == nil {
		t.Fatalf("expected initialized channels/maps")
	}
}

func TestNewBlocksFetcherCapacityWeightOverflowFallsBackToDefault(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	p2pStub := &initialSyncP2PStub{cfg: &conf.P2PConfig{MinSyncPeers: 1}}

	f := newBlocksFetcher(context.Background(), &blocksFetcherConfig{
		chain:                    chain,
		p2p:                      p2pStub,
		peerFilterCapacityWeight: 1.5,
	})
	defer f.cancel()

	if f.capacityWeight != peerFilterCapacityWeight {
		t.Fatalf("expected fallback capacityWeight, got %v", f.capacityWeight)
	}
}

func TestBlocksFetcherScheduleRequestContextDone(t *testing.T) {
	f := newTestBlocksFetcher()
	f.ctx = context.Background()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := f.scheduleRequest(ctx, uint256.NewInt(0), 1); err == nil {
		t.Fatalf("expected error for already-cancelled request context")
	}
}

func TestBlocksFetcherScheduleRequestFetcherDone(t *testing.T) {
	f := newTestBlocksFetcher()
	ctx, cancel := context.WithCancel(context.Background())
	f.ctx = ctx
	cancel() // fetcher's own context is done

	if err := f.scheduleRequest(context.Background(), uint256.NewInt(0), 1); err == nil {
		t.Fatalf("expected errFetcherCtxIsDone")
	}
}

func TestBlocksFetcherShouldSkipPeerWait(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	f := &blocksFetcher{
		chain: chain,
		p2p:   &initialSyncP2PStub{cfg: &conf.P2PConfig{MinSyncPeers: 0}},
	}
	if !f.shouldSkipPeerWait() {
		t.Fatalf("expected skip when MinSyncPeers is 0")
	}

	nonGenesisChain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(5)}}
	f2 := &blocksFetcher{
		chain: nonGenesisChain,
		p2p:   &initialSyncP2PStub{cfg: &conf.P2PConfig{MinSyncPeers: 1}},
	}
	if f2.shouldSkipPeerWait() {
		t.Fatalf("expected no skip for non-genesis block with required peers")
	}

	f3 := &blocksFetcher{
		chain: chain,
		p2p:   &initialSyncP2PStub{cfg: &conf.P2PConfig{MinSyncPeers: 1}},
	}
	if !f3.shouldSkipPeerWait() {
		t.Fatalf("expected skip for genesis block with no bootstrap/static peers configured")
	}
}
