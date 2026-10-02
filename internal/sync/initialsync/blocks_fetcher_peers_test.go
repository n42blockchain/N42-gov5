package initialsync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	n42rand "github.com/n42blockchain/N42/crypto/rand"
)

func newTestBlocksFetcher() *blocksFetcher {
	return &blocksFetcher{
		rand:      n42rand.NewGenerator(),
		peerLocks: make(map[peer.ID]*peerLock),
	}
}

func TestBlocksFetcherPeerLock(t *testing.T) {
	f := newTestBlocksFetcher()
	pid := peer.ID("peer-1")

	lock1 := f.peerLock(pid)
	if lock1 == nil {
		t.Fatalf("expected non-nil lock")
	}
	lock2 := f.peerLock(pid)
	if lock1 != lock2 {
		t.Fatalf("expected same lock instance for repeated calls")
	}
	if len(f.peerLocks) != 1 {
		t.Fatalf("expected 1 peer lock, got %d", len(f.peerLocks))
	}
}

func TestBlocksFetcherRemoveStalePeerLocks(t *testing.T) {
	f := newTestBlocksFetcher()
	f.peerLocks["stale"] = &peerLock{accessed: time.Now().Add(-2 * time.Hour)}
	f.peerLocks["fresh"] = &peerLock{accessed: time.Now()}

	f.removeStalePeerLocks(time.Hour)

	if _, ok := f.peerLocks["stale"]; ok {
		t.Fatalf("expected stale lock to be removed")
	}
	if _, ok := f.peerLocks["fresh"]; !ok {
		t.Fatalf("expected fresh lock to remain")
	}
}

func TestBlocksFetcherSelectFailOverPeer(t *testing.T) {
	f := newTestBlocksFetcher()

	if _, err := f.selectFailOverPeer("", nil); err == nil {
		t.Fatalf("expected error for empty peer list")
	}

	peers := []peer.ID{"a"}
	if _, err := f.selectFailOverPeer("a", peers); err == nil {
		t.Fatalf("expected error when all peers excluded")
	}

	peers = []peer.ID{"a", "b", "c"}
	selected, err := f.selectFailOverPeer("a", peers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected == "a" {
		t.Fatalf("expected excluded peer not to be selected")
	}
}

func TestTrimPeers(t *testing.T) {
	peers := []peer.ID{"a", "b", "c", "d"}

	if got := trimPeers(nil, 0.5, 1); got != nil {
		t.Fatalf("expected nil for empty input")
	}

	got := trimPeers(peers, 0.5, 1)
	if len(got) != 2 {
		t.Fatalf("expected 2 peers (50%% of 4), got %d", len(got))
	}

	// minSyncPeers floor should be respected.
	got = trimPeers(peers, 0.1, 3)
	if len(got) != 3 {
		t.Fatalf("expected minSyncPeers floor of 3, got %d", len(got))
	}

	// Limit cannot exceed the number of peers available.
	got = trimPeers(peers, 1.0, 10)
	if len(got) != 4 {
		t.Fatalf("expected capped at len(peers)=4, got %d", len(got))
	}
}
