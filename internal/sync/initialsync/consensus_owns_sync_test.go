package initialsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/conf"
)

// On a HotStuff chain the downloader never runs: Start reports synced at
// once even though peers are required and none are connected (the normal
// path would wait for them, then chase their Status claims), and Resync
// neither fetches nor flips the node back to syncing.
func TestConsensusOwnsSyncSkipsDownloader(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(5)}}
	p2pStub := newInitialsyncFakeP2P(t, &conf.P2PConfig{MinSyncPeers: 3})
	svc := NewService(context.Background(), &Config{P2P: p2pStub, Chain: chain, ConsensusOwnsSync: true})

	done := make(chan struct{})
	go func() { svc.Start(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start waited for peers although consensus owns sync")
	}
	if !svc.Synced() || svc.Syncing() || svc.Status() != nil {
		t.Fatal("node not reported synced")
	}
	if err := svc.Resync(); err != nil {
		t.Fatalf("Resync: %v", err)
	}
	if svc.Syncing() {
		t.Fatal("Resync flipped the node back to syncing")
	}
	if err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
}
