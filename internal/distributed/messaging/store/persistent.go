// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package store provides persistent message storage backed by content-addressed storage (CAS).
// Messages are indexed by topic and timestamp for efficient querying.
package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/sha3"
)

// MessageRecord is the persisted form of a message.
type MessageRecord struct {
	ID          [32]byte `json:"id"`
	Topic       string   `json:"topic"`
	Payload     []byte   `json:"payload"`
	ContentType string   `json:"content_type"`
	Timestamp   int64    `json:"timestamp"` // unix nano
	SenderID    string   `json:"sender_id"`
	ContentHash [32]byte `json:"content_hash"` // CAS hash
}

// CASBackend is the interface for content-addressed storage operations.
type CASBackend interface {
	Store(data []byte) ([32]byte, error)
	Load(hash [32]byte) ([]byte, error)
}

// IndexEntry represents an indexed message pointer.
type IndexEntry struct {
	ContentHash [32]byte
	MessageID   [32]byte
	Timestamp   int64
	Topic       string
	SenderID    string
}

// PersistentStore provides CAS-backed message persistence with topic+time indexing.
type PersistentStore struct {
	mu    sync.RWMutex
	cas   CASBackend
	index map[string][]IndexEntry // topic -> sorted entries (by timestamp)

	maxAge  time.Duration
	maxSize int64 // max total storage size in bytes
	curSize int64
}

// NewPersistentStore creates a persistent message store.
func NewPersistentStore(cas CASBackend, maxAge time.Duration, maxSizeMB int) *PersistentStore {
	return &PersistentStore{
		cas:     cas,
		index:   make(map[string][]IndexEntry),
		maxAge:  maxAge,
		maxSize: int64(maxSizeMB) * 1024 * 1024,
	}
}

// ContentHash computes the content hash for a message.
func ContentHash(topic string, payload []byte, timestamp int64) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte("n42-msg-cas-v1"))
	topicBytes := []byte(topic)
	var topicLen [2]byte
	binary.BigEndian.PutUint16(topicLen[:], uint16(len(topicBytes)))
	h.Write(topicLen[:])
	h.Write(topicBytes)
	h.Write(payload)
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(timestamp))
	h.Write(ts[:])
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// Save persists a message to CAS and adds it to the index.
func (ps *PersistentStore) Save(record *MessageRecord) ([32]byte, error) {
	if ps.cas == nil {
		return [32]byte{}, errors.New("no CAS backend configured")
	}

	data, err := json.Marshal(record)
	if err != nil {
		return [32]byte{}, fmt.Errorf("marshal message: %w", err)
	}

	hash, err := ps.cas.Store(data)
	if err != nil {
		return [32]byte{}, fmt.Errorf("store in CAS: %w", err)
	}

	record.ContentHash = hash

	ps.mu.Lock()
	defer ps.mu.Unlock()

	entry := IndexEntry{
		ContentHash: hash,
		MessageID:   record.ID,
		Timestamp:   record.Timestamp,
		Topic:       record.Topic,
		SenderID:    record.SenderID,
	}

	entries := ps.index[record.Topic]
	// Insert in sorted order by timestamp
	inserted := false
	for i, e := range entries {
		if record.Timestamp < e.Timestamp {
			entries = append(entries, IndexEntry{}) // grow by 1
			copy(entries[i+1:], entries[i:])
			entries[i] = entry
			inserted = true
			break
		}
	}
	if !inserted {
		entries = append(entries, entry)
	}
	ps.index[record.Topic] = entries
	ps.curSize += int64(len(data))

	return hash, nil
}

// Load retrieves a message from CAS by content hash.
func (ps *PersistentStore) Load(hash [32]byte) (*MessageRecord, error) {
	if ps.cas == nil {
		return nil, errors.New("no CAS backend configured")
	}

	data, err := ps.cas.Load(hash)
	if err != nil {
		return nil, fmt.Errorf("load from CAS: %w", err)
	}

	var record MessageRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	record.ContentHash = hash
	return &record, nil
}

// Topics returns all indexed topics.
func (ps *PersistentStore) Topics() []string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	topics := make([]string, 0, len(ps.index))
	for t := range ps.index {
		topics = append(topics, t)
	}
	return topics
}

// Count returns the number of indexed messages for a topic.
func (ps *PersistentStore) Count(topic string) int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.index[topic])
}

// Prune removes messages older than maxAge from the index.
func (ps *PersistentStore) Prune() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	cutoff := time.Now().Add(-ps.maxAge).UnixNano()
	pruned := 0

	for topic, entries := range ps.index {
		firstValid := 0
		for i, e := range entries {
			if e.Timestamp >= cutoff {
				firstValid = i
				break
			}
			firstValid = i + 1
		}
		if firstValid > 0 {
			pruned += firstValid
			ps.index[topic] = entries[firstValid:]
		}
		if len(ps.index[topic]) == 0 {
			delete(ps.index, topic)
		}
	}

	return pruned
}
