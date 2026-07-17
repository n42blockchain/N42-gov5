// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// A healthy seven-validator HotStuff deployment can transiently have one
// candidate per validator at a height. Bound unsigned peer input just above
// that shape so fabricated self-consistent headers cannot grow byNum/byHash
// without limit at one height.
const maxPeerPacketsPerHeight = 8

// PacketCache is the rolling window of encoded StreamPackets an IDC
// node serves to mobile verifiers (design §4): content-addressed by
// block hash, bounded to the most recent N blocks by number. Every IDC
// node holds the same window (packets arrive via gossip), so any node
// can serve any recent block — leader rotation never interrupts mobile
// service.
type PacketCache struct {
	mu     sync.RWMutex
	window uint64
	byHash map[types.Hash][]byte
	byNum  map[uint64][]types.Hash // eviction index; forks make this 1..n hashes per height
	maxNum uint64
}

// NewPacketCache creates a cache retaining packets for blocks within
// `window` of the highest number seen.
func NewPacketCache(window uint64) *PacketCache {
	if window == 0 {
		window = 256
	}
	return &PacketCache{
		window: window,
		byHash: make(map[types.Hash][]byte),
		byNum:  make(map[uint64][]types.Hash),
	}
}

// Put stores an encoded packet under its block hash and evicts
// everything older than the window. Idempotent per hash (packets are
// content-addressed: same hash, same bytes).
func (c *PacketCache) Put(blockHash types.Hash, blockNumber uint64, encoded []byte) {
	c.put(blockHash, blockNumber, encoded, false)
}

// PutPeer stores an unsigned-gossip packet with a per-height fork bound.
// Returns true only when a new packet was inserted.
func (c *PacketCache) PutPeer(blockHash types.Hash, blockNumber uint64, encoded []byte) bool {
	return c.put(blockHash, blockNumber, encoded, true)
}

func (c *PacketCache) put(blockHash types.Hash, blockNumber uint64, encoded []byte, peerBound bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byHash[blockHash]; ok {
		return false
	}
	if peerBound && len(c.byNum[blockNumber]) >= maxPeerPacketsPerHeight {
		return false
	}
	cp := make([]byte, len(encoded))
	copy(cp, encoded)
	c.byHash[blockHash] = cp
	c.byNum[blockNumber] = append(c.byNum[blockNumber], blockHash)
	if blockNumber > c.maxNum {
		c.maxNum = blockNumber
		if c.maxNum > c.window {
			floor := c.maxNum - c.window
			for num, hashes := range c.byNum {
				if num < floor {
					for _, h := range hashes {
						delete(c.byHash, h)
					}
					delete(c.byNum, num)
				}
			}
		}
	}
	return true
}

// Get returns the encoded packet for a block hash.
func (c *PacketCache) Get(blockHash types.Hash) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, ok := c.byHash[blockHash]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), data...), true
}

// Len returns the number of cached packets.
func (c *PacketCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byHash)
}
