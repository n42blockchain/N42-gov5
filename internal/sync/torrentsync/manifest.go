// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

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
	entries, err := os.ReadDir(eraDir)
	if err != nil {
		return nil, fmt.Errorf("torrentsync: read era dir: %w", err)
	}

	var segments []SegmentInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".era") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		seg := SegmentInfo{
			FileName: entry.Name(),
			Size:     info.Size(),
		}
		// Parse from/to from the filename convention era-NNNNNN-NNNNNN.era.
		n, _ := fmt.Sscanf(entry.Name(), "era-%d-%d.era", &seg.FromBlock, &seg.ToBlock)
		if n == 2 && seg.ToBlock >= seg.FromBlock {
			seg.BlockCount = seg.ToBlock - seg.FromBlock + 1
		}
		segments = append(segments, seg)
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].FromBlock < segments[j].FromBlock
	})

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
func (m *Manifest) FindSegment(blockNum uint64) *SegmentInfo {
	for i := range m.Segments {
		if blockNum >= m.Segments[i].FromBlock && blockNum <= m.Segments[i].ToBlock {
			return &m.Segments[i]
		}
	}
	return nil
}
