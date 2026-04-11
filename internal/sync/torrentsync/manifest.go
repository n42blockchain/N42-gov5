// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Manifest and SegmentInfo types describing an EraE archive set. Holds
// chain id, per-segment block ranges, file sizes and SHA256 integrity
// hashes. EraFileName produces the deterministic "era-NNNNNN-NNNNNN.era"
// naming and GenerateManifest scans a directory to build a Manifest.

package torrentsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Manifest lists available EraE segments with block ranges and download metadata.
type Manifest struct {
	ChainID   uint64        `json:"chain_id"`
	Segments  []SegmentInfo `json:"segments"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// SegmentInfo describes a single EraE archive segment.
type SegmentInfo struct {
	FromBlock  uint64 `json:"from_block"`
	ToBlock    uint64 `json:"to_block"`
	FileName   string `json:"file_name"`    // e.g. "era-000000-008192.era"
	Size       int64  `json:"size"`
	BlockCount uint64 `json:"block_count"`
	SHA256     string `json:"sha256"` // hex-encoded file integrity hash
}

// EraFileName returns a deterministic file name for a segment covering [from, to].
func EraFileName(fromBlock, toBlock uint64) string {
	return fmt.Sprintf("era-%06d-%06d.era", fromBlock, toBlock)
}

// GenerateManifest scans eraDir for .era files and builds a Manifest.
// It reads each file's index to determine the block range.
func GenerateManifest(eraDir string, chainID uint64) (*Manifest, error) {
	files, err := listEraFiles(eraDir)
	if err != nil {
		return nil, err
	}

	segments := make([]SegmentInfo, 0, len(files))
	for _, f := range files {
		name := filepath.Base(f)
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		seg := SegmentInfo{
			FileName: name,
			Size:     info.Size(),
		}
		// Parse from/to from the filename convention era-NNNNNN-NNNNNN.era.
		n, _ := fmt.Sscanf(name, "era-%d-%d.era", &seg.FromBlock, &seg.ToBlock)
		if n == 2 && seg.ToBlock >= seg.FromBlock {
			seg.BlockCount = seg.ToBlock - seg.FromBlock + 1
		}
		segments = append(segments, seg)
	}

	return &Manifest{
		ChainID:   chainID,
		Segments:  segments,
		UpdatedAt: time.Now(),
	}, nil
}

// LoadManifest reads a Manifest from a JSON file.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("torrentsync: load manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("torrentsync: decode manifest: %w", err)
	}
	return &m, nil
}

// SaveManifest writes a Manifest to a JSON file.
func SaveManifest(m *Manifest, path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("torrentsync: encode manifest: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("torrentsync: mkdir: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// FindSegment returns the segment that contains blockNum, or nil if not found.
// Uses binary search since Segments are sorted by FromBlock.
func (m *Manifest) FindSegment(blockNum uint64) *SegmentInfo {
	i := sort.Search(len(m.Segments), func(i int) bool {
		return m.Segments[i].FromBlock > blockNum
	})
	// i is the first segment whose FromBlock > blockNum.
	// The candidate is at i-1 (if it exists and its range covers blockNum).
	if i > 0 && blockNum <= m.Segments[i-1].ToBlock {
		return &m.Segments[i-1]
	}
	return nil
}

// listEraFiles returns sorted absolute paths of .era files in dir.
func listEraFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("torrentsync: read dir %q: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".era") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}
