// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// fakeSeeder records seeded packets and returns a deterministic magnet.
type fakeSeeder struct {
	mu    sync.Mutex
	seeds map[types.Hash][]byte
	fail  bool
}

func newFakeSeeder() *fakeSeeder { return &fakeSeeder{seeds: make(map[types.Hash][]byte)} }

func (f *fakeSeeder) Seed(blockHash types.Hash, data []byte) (string, error) {
	if f.fail {
		return "", fmt.Errorf("seed failed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.seeds[blockHash] = cp
	return "magnet:?xt=urn:btih:" + blockHash.Hex(), nil
}

func (f *fakeSeeder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seeds)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before deadline")
	}
}

func TestPacketServiceSeedsOnPublish(t *testing.T) {
	cache := NewPacketCache(8)
	svc := NewPacketService(cache, nil, "/t")
	seeder := newFakeSeeder()
	svc.SetSeeder(seeder, 8)

	blockHash, encoded := buildTestPacket(t, 5)
	// PublishLocal caches + seeds; re-decode a real packet through it.
	pkt, err := decodePacketForTest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishLocal(pkt, 5); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool { return seeder.count() == 1 })
	magnet, ok := svc.Magnet(blockHash)
	if !ok || magnet == "" {
		t.Fatalf("magnet not recorded after seeding: %q %v", magnet, ok)
	}

	// Idempotent: re-publishing the same block does not re-seed.
	_ = svc.PublishLocal(pkt, 5)
	time.Sleep(50 * time.Millisecond)
	if seeder.count() != 1 {
		t.Fatalf("re-seeded an already-seeded block: count=%d", seeder.count())
	}
}

func TestPacketServiceSeedFailureIsBestEffort(t *testing.T) {
	cache := NewPacketCache(8)
	svc := NewPacketService(cache, nil, "/t")
	seeder := newFakeSeeder()
	seeder.fail = true
	svc.SetSeeder(seeder, 8)

	blockHash, encoded := buildTestPacket(t, 9)
	pkt, err := decodePacketForTest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	// A seeding failure must not fail the publish, and the packet is still cached.
	if err := svc.PublishLocal(pkt, 9); err != nil {
		t.Fatalf("publish failed on seed error: %v", err)
	}
	if _, ok := cache.Get(blockHash); !ok {
		t.Fatal("packet not cached when seeding failed")
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := svc.Magnet(blockHash); ok {
		t.Fatal("magnet recorded despite seed failure")
	}
}

func TestPacketServiceNoSeederIsNoop(t *testing.T) {
	svc := NewPacketService(NewPacketCache(8), nil, "/t")
	_, encoded := buildTestPacket(t, 3)
	pkt, err := decodePacketForTest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishLocal(pkt, 3); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Magnet(types.Hash{}); ok {
		t.Fatal("magnet reported with no seeder")
	}
}
