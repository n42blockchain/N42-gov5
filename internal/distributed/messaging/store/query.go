// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// QueryFilter defines message query parameters.
type QueryFilter struct {
	Topic       string
	From        int64  // unix nano, 0 = no lower bound
	To          int64  // unix nano, 0 = no upper bound
	SenderID    string // empty = all senders
	ContentType string // empty = all types
	Limit       int
	Cursor      string // opaque cursor for pagination
}

// QueryResult contains query results with pagination support.
type QueryResult struct {
	Messages   []*MessageRecord
	NextCursor string // empty if no more results
	Total      int    // total matching records (before pagination)
}

// QueryEngine provides structured message querying over PersistentStore.
type QueryEngine struct {
	store *PersistentStore
}

// NewQueryEngine creates a new query engine.
func NewQueryEngine(store *PersistentStore) *QueryEngine {
	return &QueryEngine{store: store}
}

// Query executes a message query with filtering and pagination.
func (qe *QueryEngine) Query(filter *QueryFilter) (*QueryResult, error) {
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}

	qe.store.mu.RLock()
	entries := qe.store.index[filter.Topic]
	qe.store.mu.RUnlock()

	// Parse cursor (offset-based: 8 bytes big-endian uint64)
	startIdx := 0
	if filter.Cursor != "" {
		cursorBytes, err := hex.DecodeString(filter.Cursor)
		if err == nil && len(cursorBytes) >= 8 {
			startIdx = int(binary.BigEndian.Uint64(cursorBytes))
		}
	}

	// Filter entries
	var matching []IndexEntry
	for _, e := range entries {
		if filter.From > 0 && e.Timestamp < filter.From {
			continue
		}
		if filter.To > 0 && e.Timestamp > filter.To {
			continue
		}
		if filter.SenderID != "" && e.SenderID != filter.SenderID {
			continue
		}
		matching = append(matching, e)
	}

	total := len(matching)

	// Apply cursor offset
	if startIdx >= len(matching) {
		return &QueryResult{Total: total}, nil
	}
	matching = matching[startIdx:]

	// Apply limit
	hasMore := len(matching) > filter.Limit
	if hasMore {
		matching = matching[:filter.Limit]
	}

	// Load messages from CAS
	messages := make([]*MessageRecord, 0, len(matching))
	for _, entry := range matching {
		record, err := qe.store.Load(entry.ContentHash)
		if err != nil {
			continue // skip missing entries
		}
		messages = append(messages, record)
	}

	result := &QueryResult{
		Messages: messages,
		Total:    total,
	}

	if hasMore {
		nextOffset := uint64(startIdx + filter.Limit)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], nextOffset)
		result.NextCursor = hex.EncodeToString(buf[:])
	}

	return result, nil
}

// QueryByHash loads a single message by its content hash.
func (qe *QueryEngine) QueryByHash(hash [32]byte) (*MessageRecord, error) {
	return qe.store.Load(hash)
}
