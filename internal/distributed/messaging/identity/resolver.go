// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// DIDResolver for did:n42 documents with in-memory LRU caching.
// defaultCacheSize of 1024 entries and defaultCacheTTL of 1 hour back
// Register / Resolve / Update / Deactivate over a local registry map,
// so higher layers do not pay resolution cost on every message.

package identity

import (
	"errors"
	"sync"
	"time"
)

const (
	// defaultCacheSize is the default LRU cache size for resolved DIDs.
	defaultCacheSize = 1024

	// defaultCacheTTL is the default time-to-live for cached DID documents.
	defaultCacheTTL = 1 * time.Hour
)

// cacheEntry holds a cached DID document with expiry.
type cacheEntry struct {
	doc       *DIDDocument
	expiresAt time.Time
}

// DIDResolver resolves DID documents with LRU caching.
type DIDResolver struct {
	mu         sync.RWMutex
	registry   map[string]*DIDDocument // did -> document (on-chain registry simulation)
	cache      map[string]*cacheEntry  // did -> cached entry
	cacheOrder []string                // LRU order
	cacheSize  int
	cacheTTL   time.Duration
}

// NewDIDResolver creates a new DID resolver.
func NewDIDResolver(cacheSize int, cacheTTL time.Duration) *DIDResolver {
	if cacheSize <= 0 {
		cacheSize = defaultCacheSize
	}
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	return &DIDResolver{
		registry:  make(map[string]*DIDDocument),
		cache:     make(map[string]*cacheEntry),
		cacheSize: cacheSize,
		cacheTTL:  cacheTTL,
	}
}

// Register stores a DID document in the registry.
// In production, this would submit a transaction to the blockchain.
func (r *DIDResolver) Register(doc *DIDDocument) error {
	if doc == nil {
		return errors.New("nil document")
	}
	if doc.ID == "" {
		return errors.New("empty DID")
	}
	_, err := ParseDID(doc.ID)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	doc.Updated = time.Now()
	r.registry[doc.ID] = doc

	// Update cache
	r.cacheDoc(doc)

	return nil
}

// Resolve looks up a DID document, checking cache first, then registry.
func (r *DIDResolver) Resolve(did string) (*DIDDocument, error) {
	_, err := ParseDID(did)
	if err != nil {
		return nil, err
	}

	// Check cache
	r.mu.RLock()
	if entry, ok := r.cache[did]; ok && time.Now().Before(entry.expiresAt) {
		r.mu.RUnlock()
		return entry.doc, nil
	}
	r.mu.RUnlock()

	// Check registry
	r.mu.Lock()
	defer r.mu.Unlock()

	doc, ok := r.registry[did]
	if !ok {
		return nil, errors.New("DID not found")
	}

	r.cacheDoc(doc)
	return doc, nil
}

// Update updates an existing DID document.
func (r *DIDResolver) Update(doc *DIDDocument) error {
	if doc == nil {
		return errors.New("nil document")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registry[doc.ID]; !exists {
		return errors.New("DID not registered")
	}

	doc.Updated = time.Now()
	r.registry[doc.ID] = doc
	r.cacheDoc(doc)

	return nil
}

// Deactivate removes a DID document from the registry.
func (r *DIDResolver) Deactivate(did string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registry[did]; !exists {
		return errors.New("DID not registered")
	}

	delete(r.registry, did)
	delete(r.cache, did)

	return nil
}

// Size returns the number of registered DIDs.
func (r *DIDResolver) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.registry)
}

// cacheDoc adds a document to the LRU cache. Must be called with lock held.
func (r *DIDResolver) cacheDoc(doc *DIDDocument) {
	r.cache[doc.ID] = &cacheEntry{
		doc:       doc,
		expiresAt: time.Now().Add(r.cacheTTL),
	}

	// Remove old entry from order if exists
	for i, did := range r.cacheOrder {
		if did == doc.ID {
			r.cacheOrder = append(r.cacheOrder[:i], r.cacheOrder[i+1:]...)
			break
		}
	}
	r.cacheOrder = append(r.cacheOrder, doc.ID)

	// Evict if over capacity
	for len(r.cacheOrder) > r.cacheSize {
		oldest := r.cacheOrder[0]
		r.cacheOrder = r.cacheOrder[1:]
		delete(r.cache, oldest)
	}
}
