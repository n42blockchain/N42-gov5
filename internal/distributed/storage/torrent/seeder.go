// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Seeder manages automatic BitTorrent seeding of on-chain CAS content.
// SeedEntry tracks ContentHash, InfoHash, name, size and SeedingSince
// time; when the CAS precompile stores new content the seeder wraps it
// in a torrent and keeps uploading to the swarm.

package torrent

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/log"
)

// SeedEntry tracks a piece of seeded content.
type SeedEntry struct {
	ContentHash [32]byte
	InfoHash    metainfo.Hash
	Name        string
	Size        int64
	SeedingSince time.Time
}

// Seeder manages automatic seeding of on-chain CAS content via BitTorrent.
// When content is stored through the CAS precompile, the Seeder can
// automatically create and seed a torrent for that content.
type Seeder struct {
	bridge *Bridge

	mu    sync.RWMutex
	seeds map[[32]byte]*SeedEntry

	ctx    context.Context
	cancel context.CancelFunc
}

// NewSeeder creates a content seeder.
func NewSeeder(bridge *Bridge) *Seeder {
	ctx, cancel := context.WithCancel(context.Background())
	return &Seeder{
		bridge: bridge,
		seeds:  make(map[[32]byte]*SeedEntry),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins the seeder service.
func (s *Seeder) Start() {
	log.Info("CAS content seeder started")
}

// Stop shuts down the seeder.
func (s *Seeder) Stop() {
	s.cancel()
	log.Info("CAS content seeder stopped")
}

// Seed creates a torrent and starts seeding content.
func (s *Seeder) Seed(contentHash [32]byte, name string, data []byte, pieceSize int) (*SeedEntry, error) {
	// Check if already seeding (read lock only)
	s.mu.RLock()
	if entry, ok := s.seeds[contentHash]; ok {
		s.mu.RUnlock()
		return entry, nil
	}
	s.mu.RUnlock()

	// Do I/O outside lock
	mapping, err := s.bridge.SeedContent(s.ctx, contentHash, name, data, pieceSize)
	if err != nil {
		return nil, err
	}

	entry := &SeedEntry{
		ContentHash:  contentHash,
		InfoHash:     mapping.InfoHash,
		Name:         name,
		Size:         int64(len(data)),
		SeedingSince: time.Now(),
	}

	// Re-acquire write lock to store result
	s.mu.Lock()
	// Double-check: another goroutine may have seeded while we were working
	if existing, ok := s.seeds[contentHash]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.seeds[contentHash] = entry
	s.mu.Unlock()

	log.Debug("Seeding new content",
		"hash", hex.EncodeToString(contentHash[:8])+"...",
		"infohash", mapping.InfoHash.HexString(),
		"size", len(data),
	)

	return entry, nil
}

// Unseed stops seeding content.
func (s *Seeder) Unseed(contentHash [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.seeds[contentHash]
	if !ok {
		return
	}

	s.bridge.client.RemoveTorrent(entry.InfoHash)
	delete(s.seeds, contentHash)

	log.Debug("Unseeded content",
		"hash", hex.EncodeToString(contentHash[:8])+"...",
	)
}

// IsSeeding checks if content is being seeded.
func (s *Seeder) IsSeeding(contentHash [32]byte) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.seeds[contentHash]
	return ok
}

// ListSeeds returns all active seed entries.
func (s *Seeder) ListSeeds() []*SeedEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*SeedEntry, 0, len(s.seeds))
	for _, entry := range s.seeds {
		result = append(result, entry)
	}
	return result
}

// SeedCount returns the number of active seeds.
func (s *Seeder) SeedCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.seeds)
}
