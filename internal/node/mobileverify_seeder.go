// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Adapts the BitTorrent bridge (internal/distributed/storage/torrent)
// to mobileverify.Seeder, so the mobile attestation pipeline can seed
// block packets on a swarm without carrying a torrent dependency in the
// mobileverify package itself (design §5b target form). Reuses the same
// bridge/client the eth-el cold-segment distribution already uses.

package node

import (
	"context"

	n42types "github.com/n42blockchain/N42/common/types"
	dtorrent "github.com/n42blockchain/N42/internal/distributed/storage/torrent"
)

// torrentPacketSeeder implements mobileverify.Seeder over the torrent bridge.
type torrentPacketSeeder struct {
	bridge    *dtorrent.Bridge
	pieceSize int
}

// Seed creates a torrent from the packet bytes (content-addressed by
// the block hash) and returns a magnet URI phones can join. Idempotent
// at the bridge level: the same bytes yield the same infohash.
func (s *torrentPacketSeeder) Seed(blockHash n42types.Hash, data []byte) (string, error) {
	var contentHash [32]byte
	copy(contentHash[:], blockHash[:])
	if _, err := s.bridge.SeedContent(context.Background(), contentHash, "mobileverify-packet", data, s.pieceSize); err != nil {
		return "", err
	}
	return s.bridge.FormatMagnet(contentHash)
}
