// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bridge between N42 keccak256 CAS hashes and BitTorrent infohashes.
// HashMapping records the ContentHash, the derived metainfo.Hash and
// the bencoded MetaInfo used for seeding, so uploads through the CAS
// precompile automatically become downloadable via anacrolix/torrent.

package torrent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/log"
)

// HashMapping maps a keccak256 content hash to a torrent infohash.
type HashMapping struct {
	ContentHash [32]byte         // keccak256 of the content
	InfoHash    metainfo.Hash    // torrent infohash (SHA-1 of bencoded info dict)
	MetaInfo    []byte           // serialized torrent metainfo (optional, for seeding)
	CreatedAt   time.Time
}

// Bridge connects CAS (Content-Addressed Storage) with BitTorrent.
// It creates torrents from CAS content and maps content hashes to infohashes.
type Bridge struct {
	client *Client

	mu       sync.RWMutex
	mappings map[[32]byte]*HashMapping // contentHash -> mapping
}

// NewBridge creates a CAS↔BitTorrent bridge.
func NewBridge(client *Client) *Bridge {
	return &Bridge{
		client:   client,
		mappings: make(map[[32]byte]*HashMapping),
	}
}

// CreateTorrent builds a torrent metainfo from raw content data.
// It returns the metainfo and the computed infohash.
func (b *Bridge) CreateTorrent(name string, data []byte, pieceSize int) (*metainfo.MetaInfo, metainfo.Hash, error) {
	if pieceSize <= 0 {
		pieceSize = 256 * 1024 // 256KB default
	}
	if len(data) == 0 {
		return nil, metainfo.Hash{}, fmt.Errorf("empty data")
	}

	// Compute piece hashes
	var pieces []byte
	for i := 0; i < len(data); i += pieceSize {
		end := i + pieceSize
		if end > len(data) {
			end = len(data)
		}
		h := sha1.Sum(data[i:end])
		pieces = append(pieces, h[:]...)
	}

	info := metainfo.Info{
		PieceLength: int64(pieceSize),
		Pieces:      pieces,
		Name:        name,
		Length:      int64(len(data)),
	}

	mi := &metainfo.MetaInfo{
		InfoBytes: func() bencode.Bytes {
			b, _ := bencode.Marshal(info)
			return b
		}(),
		CreationDate: time.Now().Unix(),
		Comment:      "N42 CAS content",
		CreatedBy:    "N42 BitTorrent Bridge",
	}

	ih := mi.HashInfoBytes()
	return mi, ih, nil
}

// SeedContent creates a torrent from CAS content and starts seeding it.
// Returns the mapping between content hash and torrent infohash.
func (b *Bridge) SeedContent(ctx context.Context, contentHash [32]byte, name string, data []byte, pieceSize int) (*HashMapping, error) {
	mi, ih, err := b.CreateTorrent(name, data, pieceSize)
	if err != nil {
		return nil, err
	}

	// Serialize metainfo for storage
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, fmt.Errorf("serialize metainfo: %w", err)
	}

	mapping := &HashMapping{
		ContentHash: contentHash,
		InfoHash:    ih,
		MetaInfo:    buf.Bytes(),
		CreatedAt:   time.Now(),
	}

	// Persist the data to the storage path the client serves from, so the
	// seeder actually HAS the bytes to upload. NewFileByInfoHash lays a single
	// torrent out at <DataDir>/<infohashHex>/<name>; without this write the
	// client would advertise a torrent it cannot serve (0 complete pieces).
	if b.client != nil && b.client.cfg != nil && b.client.cfg.DataDir != "" {
		dst := filepath.Join(b.client.cfg.DataDir, ih.HexString(), name)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return nil, fmt.Errorf("seed mkdir: %w", err)
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return nil, fmt.Errorf("seed write: %w", err)
		}
	}

	// Add to client for seeding
	t, err := b.client.AddTorrent(mi)
	if err != nil {
		return nil, err
	}
	// Mark pieces complete from the on-disk data so the client serves them
	// (also populates in-memory piece completion when the sqlite/bolt backends
	// are compiled out under -tags nosqlite,noboltdb).
	t.VerifyData()

	b.mu.Lock()
	b.mappings[contentHash] = mapping
	b.mu.Unlock()

	log.Info("Seeding CAS content",
		"contentHash", hex.EncodeToString(contentHash[:8])+"...",
		"infoHash", ih.HexString(),
		"size", len(data),
		"name", t.Name(),
	)

	return mapping, nil
}

// FetchByInfoHash downloads content by torrent infohash.
func (b *Bridge) FetchByInfoHash(ctx context.Context, ih metainfo.Hash) ([]byte, error) {
	t, ok := b.client.GetTorrent(ih)
	if !ok {
		// Try adding as magnet
		magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", ih.HexString())
		var err error
		t, err = b.client.AddMagnet(magnet)
		if err != nil {
			return nil, fmt.Errorf("add torrent %s: %w", ih.HexString(), err)
		}
	}

	if err := b.client.Download(ctx, t); err != nil {
		return nil, fmt.Errorf("download %s: %w", ih.HexString(), err)
	}

	return b.client.ReadTorrentData(t)
}

// FetchByContentHash downloads content using a keccak256 hash mapping.
func (b *Bridge) FetchByContentHash(ctx context.Context, contentHash [32]byte) ([]byte, error) {
	b.mu.RLock()
	mapping, ok := b.mappings[contentHash]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no torrent mapping for content hash %s", hex.EncodeToString(contentHash[:]))
	}
	return b.FetchByInfoHash(ctx, mapping.InfoHash)
}

// GetMapping retrieves the hash mapping for a content hash.
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

// InfoHashFromContentHash looks up a torrent infohash from a content hash.
func (b *Bridge) InfoHashFromContentHash(contentHash [32]byte) (metainfo.Hash, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	m, ok := b.mappings[contentHash]
	if !ok {
		return metainfo.Hash{}, false
	}
	return m.InfoHash, true
}

// ContentHashFromInfoHash performs a reverse lookup.
func (b *Bridge) ContentHashFromInfoHash(ih metainfo.Hash) ([32]byte, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, m := range b.mappings {
		if m.InfoHash == ih {
			return m.ContentHash, true
		}
	}
	return [32]byte{}, false
}

// FormatMagnet generates a magnet URI for a content hash.
func (b *Bridge) FormatMagnet(contentHash [32]byte) (string, error) {
	b.mu.RLock()
	m, ok := b.mappings[contentHash]
	b.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no mapping for %s", hex.EncodeToString(contentHash[:]))
	}
	return FormatMagnetURI(m.InfoHash, "", nil), nil
}

// ParseInfoHash parses a hex-encoded infohash string.
func ParseInfoHash(s string) (metainfo.Hash, error) {
	s = strings.TrimPrefix(s, "0x")
	if len(s) != 40 {
		return metainfo.Hash{}, fmt.Errorf("invalid infohash length: %d", len(s))
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return metainfo.Hash{}, fmt.Errorf("invalid infohash hex: %w", err)
	}
	var ih metainfo.Hash
	copy(ih[:], b)
	return ih, nil
}
