// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package ethel segment.go handles splitting freezer data into distributable
// segments for BitTorrent-based chain sync.

package ethel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	// DefaultSegmentSize is the number of blocks per segment.
	DefaultSegmentSize = 500_000
)

// SegmentInfo describes one downloadable segment.
type SegmentInfo struct {
	Start  uint64   `json:"start"`
	End    uint64   `json:"end"`    // inclusive
	Tables []string `json:"tables"` // table names included
}

// Manifest lists all available segments.
type Manifest struct {
	ChainID  uint64        `json:"chain_id"`
	Segments []SegmentInfo `json:"segments"`
}

// GenerateSegments scans the freezer and creates a manifest of available segments.
func GenerateSegments(f *freezer.Freezer, segmentSize uint64) *Manifest {
	if segmentSize == 0 {
		segmentSize = DefaultSegmentSize
	}
	frozen := f.Frozen()
	if frozen == 0 {
		return &Manifest{}
	}

	tables := []string{
		freezer.TableHeaders, freezer.TableBodies, freezer.TableReceipts,
		freezer.TableHashes, freezer.TableDifficulty,
	}

	var segments []SegmentInfo
	for start := uint64(0); start < frozen; start += segmentSize {
		end := start + segmentSize - 1
		if end >= frozen {
			end = frozen - 1
		}
		segments = append(segments, SegmentInfo{
			Start:  start,
			End:    end,
			Tables: tables,
		})
	}

	return &Manifest{ChainID: 1, Segments: segments}
}

// WriteManifest writes the manifest JSON to the given directory.
func WriteManifest(dir string, m *Manifest) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	log.Info("Manifest written", "path", path, "segments", len(m.Segments))
	return nil
}

// ReadManifest reads a manifest from the given directory.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}
