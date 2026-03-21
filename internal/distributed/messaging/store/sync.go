// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"sync"
	"time"
)

// SyncProtocol provides message synchronization between nodes.
type SyncProtocol struct {
	mu    sync.RWMutex
	store *PersistentStore

	// Advertised availability: topics we have messages for
	available map[string]TimeRange
}

// TimeRange represents a time interval.
type TimeRange struct {
	From int64 // unix nano
	To   int64 // unix nano
}

// NewSyncProtocol creates a new store sync protocol.
func NewSyncProtocol(store *PersistentStore) *SyncProtocol {
	return &SyncProtocol{
		store:     store,
		available: make(map[string]TimeRange),
	}
}

// AdvertiseAvailability updates the advertised message availability for a topic.
func (sp *SyncProtocol) AdvertiseAvailability(topic string, from, to time.Time) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.available[topic] = TimeRange{
		From: from.UnixNano(),
		To:   to.UnixNano(),
	}
}

// GetAvailability returns the advertised availability for all topics.
func (sp *SyncProtocol) GetAvailability() map[string]TimeRange {
	sp.mu.RLock()
	defer sp.mu.RUnlock()
	result := make(map[string]TimeRange, len(sp.available))
	for k, v := range sp.available {
		result[k] = v
	}
	return result
}

// GetMissingRanges compares local availability with a peer's and returns gaps.
func (sp *SyncProtocol) GetMissingRanges(peerAvailability map[string]TimeRange) map[string]TimeRange {
	sp.mu.RLock()
	defer sp.mu.RUnlock()

	missing := make(map[string]TimeRange)
	for topic, peerRange := range peerAvailability {
		localRange, hasLocal := sp.available[topic]
		if !hasLocal {
			// We have nothing for this topic, request everything
			missing[topic] = peerRange
			continue
		}
		// Check if peer has messages we don't
		if peerRange.From < localRange.From {
			missing[topic] = TimeRange{
				From: peerRange.From,
				To:   localRange.From,
			}
		}
		if peerRange.To > localRange.To {
			existing, ok := missing[topic]
			if ok {
				existing.To = peerRange.To
				missing[topic] = existing
			} else {
				missing[topic] = TimeRange{
					From: localRange.To,
					To:   peerRange.To,
				}
			}
		}
	}
	return missing
}

// UpdateAvailability recalculates availability from the index.
func (sp *SyncProtocol) UpdateAvailability() {
	// Copy data from the store under its lock first, then update sp under its
	// own lock. This avoids holding both locks simultaneously which could
	// deadlock if another goroutine acquires them in a different order.
	sp.store.mu.RLock()
	updates := make(map[string]TimeRange)
	for topic, entries := range sp.store.index {
		if len(entries) == 0 {
			continue
		}
		updates[topic] = TimeRange{
			From: entries[0].Timestamp,
			To:   entries[len(entries)-1].Timestamp,
		}
	}
	sp.store.mu.RUnlock()

	sp.mu.Lock()
	defer sp.mu.Unlock()
	for topic, tr := range updates {
		sp.available[topic] = tr
	}
}

// MessagesForSync returns all index entries for a topic within a time range.
// Used by the P2P layer to respond to sync requests.
func (sp *SyncProtocol) MessagesForSync(topic string, from, to int64, limit int) []IndexEntry {
	sp.store.mu.RLock()
	defer sp.store.mu.RUnlock()

	entries := sp.store.index[topic]
	if len(entries) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}

	var result []IndexEntry
	for _, e := range entries {
		if e.Timestamp < from {
			continue
		}
		if e.Timestamp > to {
			break
		}
		result = append(result, e)
		if len(result) >= limit {
			break
		}
	}
	return result
}
