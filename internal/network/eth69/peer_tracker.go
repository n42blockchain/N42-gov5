// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package eth69

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/n42blockchain/N42/common/types"
)

// BlockRangeUpdateFrequency defines how often block range updates should be sent.
// As per EIP-7642, updates should be sent at most once per 32-block epoch.
const BlockRangeUpdateFrequency = 32

// PeerBlockRange tracks the available block range for a specific peer.
type PeerBlockRange struct {
	EarliestBlock   uint64     // Earliest available block number
	LatestBlock     uint64     // Latest block number
	LatestBlockHash types.Hash // Hash of the latest block
	LastUpdated     time.Time  // When this information was last updated
}

// PeerRangeTracker manages block range information for all connected peers.
// This allows the node to know which peers can serve which historical blocks.
type PeerRangeTracker struct {
	mu     sync.RWMutex
	ranges map[peer.ID]*PeerBlockRange

	// Local node's block range
	localRange *PeerBlockRange
	localMu    sync.RWMutex

	// Last time we sent a BlockRangeUpdate
	lastUpdate     uint64 // Last block number when update was sent
	lastUpdateTime time.Time
}

// NewPeerRangeTracker creates a new peer range tracker.
func NewPeerRangeTracker() *PeerRangeTracker {
	return &PeerRangeTracker{
		ranges: make(map[peer.ID]*PeerBlockRange),
		localRange: &PeerBlockRange{
			LastUpdated: time.Now(),
		},
	}
}

// UpdatePeerRange updates the block range information for a peer.
func (t *PeerRangeTracker) UpdatePeerRange(peerID peer.ID, earliest, latest uint64, latestHash types.Hash) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ranges[peerID] = &PeerBlockRange{
		EarliestBlock:   earliest,
		LatestBlock:     latest,
		LatestBlockHash: latestHash,
		LastUpdated:     time.Now(),
	}
}

// GetPeerRange retrieves the block range information for a peer.
// Returns a copy to prevent external modification.
func (t *PeerRangeTracker) GetPeerRange(peerID peer.ID) (*PeerBlockRange, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	pr, ok := t.ranges[peerID]
	if !ok {
		return nil, false
	}
	return &PeerBlockRange{
		EarliestBlock:   pr.EarliestBlock,
		LatestBlock:     pr.LatestBlock,
		LatestBlockHash: pr.LatestBlockHash,
		LastUpdated:     pr.LastUpdated,
	}, true
}

// RemovePeer removes block range information for a disconnected peer.
func (t *PeerRangeTracker) RemovePeer(peerID peer.ID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.ranges, peerID)
}

// GetPeersWithBlock returns a list of peers that have a specific block.
func (t *PeerRangeTracker) GetPeersWithBlock(blockNumber uint64) []peer.ID {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var peers []peer.ID
	for peerID, pr := range t.ranges {
		if blockNumber >= pr.EarliestBlock && blockNumber <= pr.LatestBlock {
			peers = append(peers, peerID)
		}
	}
	return peers
}

// GetPeersWithBlockRange returns peers that have all blocks in the given range.
func (t *PeerRangeTracker) GetPeersWithBlockRange(start, end uint64) []peer.ID {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var peers []peer.ID
	for peerID, pr := range t.ranges {
		if start >= pr.EarliestBlock && end <= pr.LatestBlock {
			peers = append(peers, peerID)
		}
	}
	return peers
}

// UpdateLocalRange updates the local node's block range.
func (t *PeerRangeTracker) UpdateLocalRange(earliest, latest uint64, latestHash types.Hash) {
	t.localMu.Lock()
	defer t.localMu.Unlock()

	t.localRange = &PeerBlockRange{
		EarliestBlock:   earliest,
		LatestBlock:     latest,
		LatestBlockHash: latestHash,
		LastUpdated:     time.Now(),
	}
}

// GetLocalRange returns the local node's block range.
func (t *PeerRangeTracker) GetLocalRange() *PeerBlockRange {
	t.localMu.RLock()
	defer t.localMu.RUnlock()

	return &PeerBlockRange{
		EarliestBlock:   t.localRange.EarliestBlock,
		LatestBlock:     t.localRange.LatestBlock,
		LatestBlockHash: t.localRange.LatestBlockHash,
		LastUpdated:     t.localRange.LastUpdated,
	}
}

// ShouldSendUpdate determines if a BlockRangeUpdate should be sent.
// Per EIP-7642, updates should be sent at most once per 32-block epoch,
// but should be sent immediately if the range moves backwards (e.g., due to reorg).
func (t *PeerRangeTracker) ShouldSendUpdate(currentBlock uint64) bool {
	t.localMu.RLock()
	defer t.localMu.RUnlock()

	// Send immediately if range moved backwards (reorg/sethead)
	if currentBlock < t.lastUpdate {
		return true
	}

	// Send if we've advanced by at least BlockRangeUpdateFrequency blocks
	if currentBlock >= t.lastUpdate+BlockRangeUpdateFrequency {
		return true
	}

	// Send if we haven't sent an update in a long time (5 minutes)
	if time.Since(t.lastUpdateTime) > 5*time.Minute {
		return true
	}

	return false
}

// MarkUpdateSent marks that a BlockRangeUpdate was sent at the given block.
func (t *PeerRangeTracker) MarkUpdateSent(blockNumber uint64) {
	t.localMu.Lock()
	defer t.localMu.Unlock()

	t.lastUpdate = blockNumber
	t.lastUpdateTime = time.Now()
}

// GetAllPeerRanges returns a copy of all peer ranges (for debugging/monitoring).
func (t *PeerRangeTracker) GetAllPeerRanges() map[peer.ID]*PeerBlockRange {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[peer.ID]*PeerBlockRange, len(t.ranges))
	for peerID, pr := range t.ranges {
		result[peerID] = &PeerBlockRange{
			EarliestBlock:   pr.EarliestBlock,
			LatestBlock:     pr.LatestBlock,
			LatestBlockHash: pr.LatestBlockHash,
			LastUpdated:     pr.LastUpdated,
		}
	}
	return result
}

// PeerCount returns the number of peers being tracked.
func (t *PeerRangeTracker) PeerCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.ranges)
}
