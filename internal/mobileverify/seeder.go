// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Seeder makes a block's encoded packet available on a swarm transport
// (design §5b target form) and returns a magnet URI phones can join.
// Implemented by the node-level adapter over the BitTorrent bridge
// (internal/distributed/storage/torrent); interface-decoupled so this
// package carries no torrent dependency and tests use a fake.
type Seeder interface {
	Seed(blockHash types.Hash, data []byte) (magnet string, err error)
}

// packetSeeds tracks the magnet URI per seeded block, window-evicted in
// lockstep with the packet cache's retention intent (same bound, its
// own index — magnets are tiny).
type packetSeeds struct {
	mu     sync.RWMutex
	max    int
	byHash map[types.Hash]string
	order  []types.Hash
}

func newPacketSeeds(max int) *packetSeeds {
	if max <= 0 {
		max = 256
	}
	return &packetSeeds{max: max, byHash: make(map[types.Hash]string)}
}

func (p *packetSeeds) put(hash types.Hash, magnet string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byHash[hash]; ok {
		return
	}
	p.byHash[hash] = magnet
	p.order = append(p.order, hash)
	if len(p.order) > p.max {
		oldest := p.order[0]
		p.order = p.order[1:]
		delete(p.byHash, oldest)
	}
}

func (p *packetSeeds) get(hash types.Hash) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	m, ok := p.byHash[hash]
	return m, ok
}

// SetSeeder wires swarm seeding into the packet service: every packet
// that enters the cache (produced locally or received via gossip) is
// also seeded, best-effort — a seeding failure never blocks caching or
// gossip. Call before Start.
func (s *PacketService) SetSeeder(seeder Seeder, window int) {
	s.seeder = seeder
	s.seeds = newPacketSeeds(window)
}

// Magnet returns the swarm URI for a seeded block's packet.
func (s *PacketService) Magnet(blockHash types.Hash) (string, bool) {
	if s.seeds == nil {
		return "", false
	}
	return s.seeds.get(blockHash)
}

// seedAsync hands a cached packet to the seeder off the caller's path.
func (s *PacketService) seedAsync(blockHash types.Hash, data []byte) {
	if s.seeder == nil {
		return
	}
	if _, already := s.seeds.get(blockHash); already {
		return
	}
	go func() {
		magnet, err := s.seeder.Seed(blockHash, data)
		if err != nil {
			log.Debug("mobileverify: packet seeding failed", "hash", blockHash.Hex()[:12], "err", err)
			return
		}
		s.seeds.put(blockHash, magnet)
	}()
}
