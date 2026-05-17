// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// internal/bundle implements the minimal-client bundle manifest:
// blake2b-256 hashes of every freezer file under a datadir, with
// BT-style 64 MiB segment hashes for large files. The server produces
// the manifest once after state-root verify; clients verify downloads
// against it. Multiple independent servers reproducing the same
// manifest are the trustless anchor (see
// memory/project_minimal_client_design.md).

package bundle

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	// ManifestVersion is incremented when the wire format changes
	// incompatibly. Verify rejects unknown versions.
	ManifestVersion = 1

	// Algorithm identifies the digest. blake2b-256 chosen for speed
	// (~3× SHA-256 on AMD64 — see golang.org/x/crypto/blake2b) with
	// cryptographic strength comparable to SHA-256.
	Algorithm = "blake2b-256"

	// SegmentSize is the BT-style chunk size for per-segment hashes.
	// 64 MiB — small enough that a single corrupt segment costs only
	// 64 MiB re-download, large enough that overhead (per-segment
	// JSON entry, hashing seek) stays under 0.01% on 1 TiB bundles.
	SegmentSize int64 = 64 * 1024 * 1024

	// SegmentThreshold is the file-size floor for per-segment hashes.
	// Files smaller than this only get a whole-file hash. 1 GiB is
	// the sweet spot: smaller than this and partial re-download
	// savings don't beat the manifest bloat.
	SegmentThreshold int64 = 1 * 1024 * 1024 * 1024
)

// Manifest is the top-level JSON document describing a bundle. It lists
// every file the server included plus integrity metadata. The bundle
// itself is identified by (chainID, blockRange, generatedAt) — clients
// MUST cross-check these against their expected values before trusting
// any hash.
type Manifest struct {
	Version     int        `json:"version"`
	Algorithm   string     `json:"algorithm"`
	SegmentSize int64      `json:"segment_size"`
	GeneratedAt time.Time  `json:"generated_at"`
	ChainID     uint64     `json:"chain_id"`
	BlockRange  BlockRange `json:"block_range"`
	Files       []File     `json:"files"`
}

// BlockRange is inclusive on both ends. Empty bundle uses Start=End=0
// with End < Start interpreted as no-data.
type BlockRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// File is one bundle file. Path is relative to the bundle root, always
// using forward slashes (Windows servers normalize at hash time). Hash
// is the whole-file blake2b-256, lowercase hex (no 0x prefix). Segments
// is present only when Size >= SegmentThreshold; each entry is the
// blake2b-256 of SegmentSize bytes (last segment may be short).
type File struct {
	Path     string   `json:"path"`
	Size     int64    `json:"size"`
	Hash     string   `json:"hash"`
	Segments []string `json:"segments,omitempty"`
}

// Save writes m to a JSON file at path with 2-space indentation
// (human-readable; manifests are small enough that compactness doesn't
// matter and operators inspect them by eye).
func (m *Manifest) Save(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("bundle: create manifest %s: %w", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("bundle: encode manifest: %w", err)
	}
	return nil
}

// Load parses a manifest from path. Returns an error if version is not
// recognized — clients should refuse to verify unknown formats rather
// than silently treat them as a different version.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bundle: open manifest %s: %w", path, err)
	}
	defer f.Close()
	return LoadReader(f)
}

// LoadReader parses a manifest from r.
func LoadReader(r io.Reader) (*Manifest, error) {
	var m Manifest
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return nil, fmt.Errorf("bundle: decode manifest: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("bundle: unsupported manifest version %d (want %d)", m.Version, ManifestVersion)
	}
	if m.Algorithm != Algorithm {
		return nil, fmt.Errorf("bundle: unsupported algorithm %q (want %q)", m.Algorithm, Algorithm)
	}
	if m.SegmentSize <= 0 {
		return nil, fmt.Errorf("bundle: invalid segment_size %d", m.SegmentSize)
	}
	return &m, nil
}
