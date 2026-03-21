// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// memoryCAS is a test CAS backend backed by an in-memory map.
type memoryCAS struct {
	mu   sync.Mutex
	data map[[32]byte][]byte
}

func newMemoryCAS() *memoryCAS {
	return &memoryCAS{data: make(map[[32]byte][]byte)}
}

func (m *memoryCAS) Store(data []byte) ([32]byte, error) {
	h := sha256.Sum256(data)
	m.mu.Lock()
	m.data[h] = append([]byte(nil), data...)
	m.mu.Unlock()
	return h, nil
}

func (m *memoryCAS) Load(hash [32]byte) ([]byte, error) {
	m.mu.Lock()
	d, ok := m.data[hash]
	m.mu.Unlock()
	if !ok {
		return nil, errors.New("not found")
	}
	return d, nil
}

// makeRecord creates a MessageRecord with the given parameters for testing.
func makeRecord(topic string, payload []byte, sender string, ts int64) *MessageRecord {
	var id [32]byte
	h := sha256.Sum256(append([]byte(topic), payload...))
	copy(id[:], h[:])
	// Make ID unique by mixing in timestamp
	id[0] ^= byte(ts)
	id[1] ^= byte(ts >> 8)
	return &MessageRecord{
		ID:          id,
		Topic:       topic,
		Payload:     payload,
		ContentType: "text/plain",
		Timestamp:   ts,
		SenderID:    sender,
	}
}

func TestPersistentStoreSaveLoad(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)

	record := makeRecord("chat/general", []byte("hello world"), "alice", time.Now().UnixNano())

	hash, err := ps.Save(record)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if hash == ([32]byte{}) {
		t.Fatal("expected non-zero hash from Save")
	}

	loaded, err := ps.Load(hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Topic != record.Topic {
		t.Errorf("Topic = %q, want %q", loaded.Topic, record.Topic)
	}
	if string(loaded.Payload) != string(record.Payload) {
		t.Errorf("Payload = %q, want %q", loaded.Payload, record.Payload)
	}
	if loaded.SenderID != record.SenderID {
		t.Errorf("SenderID = %q, want %q", loaded.SenderID, record.SenderID)
	}
	if loaded.Timestamp != record.Timestamp {
		t.Errorf("Timestamp = %d, want %d", loaded.Timestamp, record.Timestamp)
	}
	if loaded.ContentType != record.ContentType {
		t.Errorf("ContentType = %q, want %q", loaded.ContentType, record.ContentType)
	}
}

func TestPersistentStoreMultipleTopics(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)

	now := time.Now().UnixNano()

	for i := 0; i < 3; i++ {
		r := makeRecord("topic-a", []byte(fmt.Sprintf("a-%d", i)), "alice", now+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save topic-a msg %d: %v", i, err)
		}
	}
	for i := 0; i < 5; i++ {
		r := makeRecord("topic-b", []byte(fmt.Sprintf("b-%d", i)), "bob", now+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save topic-b msg %d: %v", i, err)
		}
	}

	if ps.Count("topic-a") != 3 {
		t.Errorf("topic-a count = %d, want 3", ps.Count("topic-a"))
	}
	if ps.Count("topic-b") != 5 {
		t.Errorf("topic-b count = %d, want 5", ps.Count("topic-b"))
	}

	topics := ps.Topics()
	if len(topics) != 2 {
		t.Errorf("Topics() = %d topics, want 2", len(topics))
	}
}

func TestPersistentStorePrune(t *testing.T) {
	cas := newMemoryCAS()
	maxAge := 50 * time.Millisecond
	ps := NewPersistentStore(cas, maxAge, 10)

	// Save old messages (well beyond the maxAge)
	oldTs := time.Now().Add(-2 * time.Second).UnixNano()
	for i := 0; i < 3; i++ {
		r := makeRecord("prune-topic", []byte(fmt.Sprintf("old-%d", i)), "alice", oldTs+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save old msg %d: %v", i, err)
		}
	}

	// Save new messages
	newTs := time.Now().UnixNano()
	for i := 0; i < 2; i++ {
		r := makeRecord("prune-topic", []byte(fmt.Sprintf("new-%d", i)), "alice", newTs+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save new msg %d: %v", i, err)
		}
	}

	if ps.Count("prune-topic") != 5 {
		t.Fatalf("count before prune = %d, want 5", ps.Count("prune-topic"))
	}

	pruned := ps.Prune()
	if pruned != 3 {
		t.Errorf("pruned = %d, want 3", pruned)
	}
	if ps.Count("prune-topic") != 2 {
		t.Errorf("count after prune = %d, want 2", ps.Count("prune-topic"))
	}
}

func TestContentHash(t *testing.T) {
	// Same inputs produce same hash
	h1 := ContentHash("topic", []byte("payload"), 12345)
	h2 := ContentHash("topic", []byte("payload"), 12345)
	if h1 != h2 {
		t.Error("same inputs produced different hashes")
	}

	// Different topic
	h3 := ContentHash("other-topic", []byte("payload"), 12345)
	if h1 == h3 {
		t.Error("different topics produced same hash")
	}

	// Different payload
	h4 := ContentHash("topic", []byte("other-payload"), 12345)
	if h1 == h4 {
		t.Error("different payloads produced same hash")
	}

	// Different timestamp
	h5 := ContentHash("topic", []byte("payload"), 99999)
	if h1 == h5 {
		t.Error("different timestamps produced same hash")
	}
}

func TestQueryEngineBasic(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	qe := NewQueryEngine(ps)

	now := time.Now().UnixNano()
	for i := 0; i < 5; i++ {
		r := makeRecord("query-topic", []byte(fmt.Sprintf("msg-%d", i)), "alice", now+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save msg %d: %v", i, err)
		}
	}

	result, err := qe.Query(&QueryFilter{
		Topic: "query-topic",
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Messages) != 5 {
		t.Errorf("Messages count = %d, want 5", len(result.Messages))
	}
	if result.Total != 5 {
		t.Errorf("Total = %d, want 5", result.Total)
	}
}

func TestQueryEngineTimeRange(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	qe := NewQueryEngine(ps)

	base := int64(1_000_000_000) // fixed base to avoid clock issues
	for i := 0; i < 10; i++ {
		r := makeRecord("time-topic", []byte(fmt.Sprintf("msg-%d", i)), "alice", base+int64(i*1000))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save msg %d: %v", i, err)
		}
	}

	// Query for messages in the middle range (indices 3-6)
	result, err := qe.Query(&QueryFilter{
		Topic: "time-topic",
		From:  base + 3000,
		To:    base + 6000,
		Limit: 50,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Messages) != 4 {
		t.Errorf("Messages count = %d, want 4 (indices 3-6)", len(result.Messages))
	}
}

func TestQueryEngineSenderFilter(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	qe := NewQueryEngine(ps)

	now := time.Now().UnixNano()
	senders := []string{"alice", "bob", "alice", "carol", "alice"}
	for i, sender := range senders {
		r := makeRecord("sender-topic", []byte(fmt.Sprintf("msg-%d", i)), sender, now+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save msg %d: %v", i, err)
		}
	}

	result, err := qe.Query(&QueryFilter{
		Topic:    "sender-topic",
		SenderID: "alice",
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Messages) != 3 {
		t.Errorf("Messages from alice = %d, want 3", len(result.Messages))
	}
	for _, msg := range result.Messages {
		if msg.SenderID != "alice" {
			t.Errorf("expected sender alice, got %q", msg.SenderID)
		}
	}
}

func TestQueryEnginePagination(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	qe := NewQueryEngine(ps)

	now := time.Now().UnixNano()
	for i := 0; i < 10; i++ {
		r := makeRecord("page-topic", []byte(fmt.Sprintf("msg-%d", i)), "alice", now+int64(i))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save msg %d: %v", i, err)
		}
	}

	var allMessages []*MessageRecord
	cursor := ""
	pages := 0

	for {
		result, err := qe.Query(&QueryFilter{
			Topic:  "page-topic",
			Limit:  3,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("Query page %d: %v", pages, err)
		}
		allMessages = append(allMessages, result.Messages...)
		pages++

		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor

		if pages > 10 {
			t.Fatal("too many pages, possible infinite loop")
		}
	}

	if len(allMessages) != 10 {
		t.Errorf("total messages across pages = %d, want 10", len(allMessages))
	}
	if pages != 4 { // 3+3+3+1
		t.Errorf("pages = %d, want 4", pages)
	}
}

func TestQueryByHash(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	qe := NewQueryEngine(ps)

	record := makeRecord("hash-topic", []byte("find-me"), "alice", time.Now().UnixNano())
	hash, err := ps.Save(record)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := qe.QueryByHash(hash)
	if err != nil {
		t.Fatalf("QueryByHash: %v", err)
	}
	if string(loaded.Payload) != "find-me" {
		t.Errorf("Payload = %q, want %q", loaded.Payload, "find-me")
	}

	// Query with non-existent hash
	var badHash [32]byte
	badHash[0] = 0xFF
	_, err = qe.QueryByHash(badHash)
	if err == nil {
		t.Error("expected error for non-existent hash")
	}
}

func TestSyncProtocolAvailability(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	sp := NewSyncProtocol(ps)

	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)

	sp.AdvertiseAvailability("topic-a", from, to)
	sp.AdvertiseAvailability("topic-b", from, to)

	avail := sp.GetAvailability()
	if len(avail) != 2 {
		t.Fatalf("availability count = %d, want 2", len(avail))
	}

	rangeA, ok := avail["topic-a"]
	if !ok {
		t.Fatal("topic-a not in availability")
	}
	if rangeA.From != from.UnixNano() {
		t.Errorf("topic-a From = %d, want %d", rangeA.From, from.UnixNano())
	}
	if rangeA.To != to.UnixNano() {
		t.Errorf("topic-a To = %d, want %d", rangeA.To, to.UnixNano())
	}
}

func TestSyncProtocolMissingRanges(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	sp := NewSyncProtocol(ps)

	// Local has topic-a from 100 to 200
	sp.AdvertiseAvailability("topic-a",
		time.Unix(0, 100), time.Unix(0, 200))

	// Peer has topic-a from 50 to 300 and topic-b from 0 to 100
	peerAvail := map[string]TimeRange{
		"topic-a": {From: 50, To: 300},
		"topic-b": {From: 0, To: 100},
	}

	missing := sp.GetMissingRanges(peerAvail)

	// We should be missing the beginning (50-100) and end (200-300) of topic-a
	missingA, ok := missing["topic-a"]
	if !ok {
		t.Fatal("topic-a should be in missing ranges")
	}
	if missingA.From != 50 {
		t.Errorf("missing topic-a From = %d, want 50", missingA.From)
	}
	// The implementation merges: if both before and after are missing, To becomes peerRange.To
	if missingA.To != 300 {
		t.Errorf("missing topic-a To = %d, want 300", missingA.To)
	}

	// topic-b: we have nothing, so the entire peer range is missing
	missingB, ok := missing["topic-b"]
	if !ok {
		t.Fatal("topic-b should be in missing ranges")
	}
	if missingB.From != 0 || missingB.To != 100 {
		t.Errorf("missing topic-b = [%d, %d], want [0, 100]", missingB.From, missingB.To)
	}
}

func TestSyncProtocolMessagesForSync(t *testing.T) {
	cas := newMemoryCAS()
	ps := NewPersistentStore(cas, time.Hour, 10)
	sp := NewSyncProtocol(ps)

	base := int64(1000)
	for i := 0; i < 10; i++ {
		r := makeRecord("sync-topic", []byte(fmt.Sprintf("msg-%d", i)), "alice", base+int64(i*100))
		if _, err := ps.Save(r); err != nil {
			t.Fatalf("Save msg %d: %v", i, err)
		}
	}

	// Query a range that should match entries 3-6 (timestamps 1300-1600)
	entries := sp.MessagesForSync("sync-topic", 1300, 1600, 100)
	if len(entries) != 4 {
		t.Errorf("MessagesForSync count = %d, want 4", len(entries))
	}

	// Query with limit
	entries = sp.MessagesForSync("sync-topic", base, base+10000, 3)
	if len(entries) != 3 {
		t.Errorf("MessagesForSync with limit = %d, want 3", len(entries))
	}

	// Query non-existent topic
	entries = sp.MessagesForSync("no-such-topic", 0, 99999, 100)
	if len(entries) != 0 {
		t.Errorf("MessagesForSync non-existent = %d, want 0", len(entries))
	}
}
