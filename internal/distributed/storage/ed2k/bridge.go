// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bridge between N42 keccak256 CAS hashes and ed2k / MD4 file hashes.
// HashMapping stores the content hash, the ed2k hash, size and name
// so that ComputeAndMap can publish local content to eDonkey networks
// and reverse lookups can recover the CAS key from an ed2k identifier.

package ed2k

import (
	"encoding/hex"
	"fmt"
	"sync"
)

// HashMapping maps a keccak256 content hash to an ed2k hash.
type HashMapping struct {
	ContentHash [32]byte       // keccak256 of the content
	Ed2kHash    [HashSize]byte // ed2k/MD4 hash
	Size        int64
	Name        string
}

// Bridge connects CAS content hashes with ed2k hashes and links.
type Bridge struct {
	mu       sync.RWMutex
	mappings map[[32]byte]*HashMapping // contentHash -> mapping
}

// NewBridge creates an ed2k bridge.
func NewBridge() *Bridge {
	return &Bridge{
		mappings: make(map[[32]byte]*HashMapping),
	}
}

// ComputeAndMap computes the ed2k hash for data and stores the mapping.
func (b *Bridge) ComputeAndMap(contentHash [32]byte, name string, data []byte) (*HashMapping, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("ed2k: empty data")
	}

	ed2kHash := Hash(data)

	mapping := &HashMapping{
		ContentHash: contentHash,
		Ed2kHash:    ed2kHash,
		Size:        int64(len(data)),
		Name:        name,
	}

	b.mu.Lock()
	b.mappings[contentHash] = mapping
	b.mu.Unlock()

	return mapping, nil
}

// GetMapping retrieves a mapping by content hash.
func (b *Bridge) GetMapping(contentHash [32]byte) (*HashMapping, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	m, ok := b.mappings[contentHash]
	return m, ok
}

// AddMapping registers an existing mapping (e.g., loaded from DB).
func (b *Bridge) AddMapping(m *HashMapping) {
	b.mu.Lock()
	b.mappings[m.ContentHash] = m
	b.mu.Unlock()
}

// Ed2kHashFromContentHash looks up an ed2k hash from a content hash.
func (b *Bridge) Ed2kHashFromContentHash(contentHash [32]byte) ([HashSize]byte, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	m, ok := b.mappings[contentHash]
	if !ok {
		return [HashSize]byte{}, false
	}
	return m.Ed2kHash, true
}

// ContentHashFromEd2k performs a reverse lookup.
func (b *Bridge) ContentHashFromEd2k(ed2kHash [HashSize]byte) ([32]byte, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, m := range b.mappings {
		if m.Ed2kHash == ed2kHash {
			return m.ContentHash, true
		}
	}
	return [32]byte{}, false
}

// FormatLink creates an ed2k:// link for a content hash.
func (b *Bridge) FormatLink(contentHash [32]byte) (string, error) {
	b.mu.RLock()
	m, ok := b.mappings[contentHash]
	b.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no ed2k mapping for %s", hex.EncodeToString(contentHash[:]))
	}
	return FormatLink(m.Name, m.Size, m.Ed2kHash), nil
}

// MappingCount returns the number of stored mappings.
func (b *Bridge) MappingCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.mappings)
}
