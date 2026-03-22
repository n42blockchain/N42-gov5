// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package torrentsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestEraFileName(t *testing.T) {
	tests := []struct {
		from, to uint64
		want     string
	}{
		{0, 8191, "era-000000-008191.era"},
		{8192, 16383, "era-008192-016383.era"},
		{0, 0, "era-000000-000000.era"},
		{999999, 1000000, "era-999999-1000000.era"},
	}
	for _, tt := range tests {
		got := EraFileName(tt.from, tt.to)
		if got != tt.want {
			t.Errorf("EraFileName(%d, %d) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")

	original := &Manifest{
		ChainID: 42,
		Segments: []SegmentInfo{
			{FromBlock: 0, ToBlock: 8191, FileName: "era-000000-008191.era", Size: 1024, BlockCount: 8192, SHA256: "abc123"},
			{FromBlock: 8192, ToBlock: 16383, FileName: "era-008192-016383.era", Size: 2048, BlockCount: 8192, SHA256: "def456"},
		},
	}

	// Save.
	if err := SaveManifest(original, manifestPath); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Load.
	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if loaded.ChainID != original.ChainID {
		t.Errorf("ChainID: got %d, want %d", loaded.ChainID, original.ChainID)
	}
	if len(loaded.Segments) != len(original.Segments) {
		t.Fatalf("Segments: got %d, want %d", len(loaded.Segments), len(original.Segments))
	}
	for i, seg := range loaded.Segments {
		orig := original.Segments[i]
		if seg.FromBlock != orig.FromBlock || seg.ToBlock != orig.ToBlock {
			t.Errorf("Segment[%d] range: got [%d,%d], want [%d,%d]",
				i, seg.FromBlock, seg.ToBlock, orig.FromBlock, orig.ToBlock)
		}
		if seg.FileName != orig.FileName {
			t.Errorf("Segment[%d] file: got %q, want %q", i, seg.FileName, orig.FileName)
		}
		if seg.SHA256 != orig.SHA256 {
			t.Errorf("Segment[%d] SHA256: got %q, want %q", i, seg.SHA256, orig.SHA256)
		}
	}

	// FindSegment.
	seg := loaded.FindSegment(0)
	if seg == nil || seg.FromBlock != 0 {
		t.Errorf("FindSegment(0): got %v, want segment starting at 0", seg)
	}
	seg = loaded.FindSegment(5000)
	if seg == nil || seg.FromBlock != 0 {
		t.Errorf("FindSegment(5000): got %v, want segment starting at 0", seg)
	}
	seg = loaded.FindSegment(8192)
	if seg == nil || seg.FromBlock != 8192 {
		t.Errorf("FindSegment(8192): got %v, want segment starting at 8192", seg)
	}
	seg = loaded.FindSegment(99999)
	if seg != nil {
		t.Errorf("FindSegment(99999): got %v, want nil", seg)
	}
}

func TestGenerateManifest(t *testing.T) {
	dir := t.TempDir()

	// Create some fake .era files.
	for _, name := range []string{"era-000000-000009.era", "era-000010-000019.era", "not-era.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fake"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	m, err := GenerateManifest(dir, 42)
	if err != nil {
		t.Fatalf("GenerateManifest: %v", err)
	}
	if m.ChainID != 42 {
		t.Errorf("ChainID: got %d, want 42", m.ChainID)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("Segments: got %d, want 2", len(m.Segments))
	}
	if m.Segments[0].FromBlock != 0 || m.Segments[0].ToBlock != 9 {
		t.Errorf("Segment[0] range: got [%d,%d], want [0,9]", m.Segments[0].FromBlock, m.Segments[0].ToBlock)
	}
	if m.Segments[1].FromBlock != 10 || m.Segments[1].ToBlock != 19 {
		t.Errorf("Segment[1] range: got [%d,%d], want [10,19]", m.Segments[1].FromBlock, m.Segments[1].ToBlock)
	}
}

// makeTestBlock creates a simple block with the given number and parent hash.
func makeTestBlock(number uint64, parentHash types.Hash) *block.Block {
	header := &block.Header{
		ParentHash: parentHash,
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
		GasLimit:   8_000_000,
		Time:       1000 + number,
		Extra:      []byte("test"),
	}
	return block.NewBlock(header, nil).(*block.Block)
}

func TestExportImport(t *testing.T) {
	ctx := context.Background()
	eraDir := t.TempDir()

	// Create source database and populate with blocks.
	srcDB := memdb.NewTestDB(t)

	const numBlocks = 20

	// Write blocks to source DB.
	var blocks []*block.Block
	parentHash := types.Hash{} // genesis parent is zero
	for i := uint64(0); i < numBlocks; i++ {
		blk := makeTestBlock(i, parentHash)
		blocks = append(blocks, blk)
		parentHash = blk.Hash()
	}

	err := srcDB.Update(ctx, func(tx kv.RwTx) error {
		for _, blk := range blocks {
			num := blk.Number64().Uint64()
			if err := rawdb.WriteBlock(tx, blk); err != nil {
				return err
			}
			if err := rawdb.WriteCanonicalHash(tx, blk.Hash(), num); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("populate source DB: %v", err)
	}

	// Export.
	exporter := NewExporter(srcDB, eraDir, 10) // 10 blocks per segment
	segments, err := exporter.ExportRange(ctx, 0, numBlocks-1)
	if err != nil {
		t.Fatalf("ExportRange: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	for _, seg := range segments {
		if seg.SHA256 == "" {
			t.Errorf("segment %s: missing SHA256", seg.FileName)
		}
		if seg.Size == 0 {
			t.Errorf("segment %s: zero size", seg.FileName)
		}
	}

	// Import into a fresh database.
	dstDB := memdb.NewTestDB(t)
	importer := NewImporter(dstDB, eraDir, false) // skip verify for cross-DB test
	total, err := importer.ImportAll(ctx, eraDir)
	if err != nil {
		t.Fatalf("ImportAll: %v", err)
	}
	if total != numBlocks {
		t.Errorf("imported %d blocks, want %d", total, numBlocks)
	}

	// Verify blocks match.
	err = dstDB.View(ctx, func(tx kv.Tx) error {
		for i := uint64(0); i < numBlocks; i++ {
			blk, err := rawdb.ReadBlockByNumber(tx, i)
			if err != nil {
				return fmt.Errorf("read block %d: %w", i, err)
			}
			if blk == nil {
				return fmt.Errorf("block %d not found in destination DB", i)
			}
			if blk.Hash() != blocks[i].Hash() {
				return fmt.Errorf("block %d hash mismatch: got %s, want %s",
					i, blk.Hash().Hex(), blocks[i].Hash().Hex())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("verify imported blocks: %v", err)
	}
}

func TestServiceGenerateManifest(t *testing.T) {
	eraDir := t.TempDir()

	// Create a fake .era file so the manifest has something to find.
	if err := os.WriteFile(filepath.Join(eraDir, "era-000000-000099.era"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	db := memdb.NewTestDB(t)
	cfg := conf.DefaultTorrentSyncCfg()
	svc := NewService(&cfg, db, eraDir)

	if err := svc.GenerateManifest(); err != nil {
		t.Fatalf("GenerateManifest: %v", err)
	}

	// Verify manifest file was written.
	m, err := LoadManifest(filepath.Join(eraDir, "manifest.json"))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Segments) != 1 {
		t.Errorf("expected 1 segment, got %d", len(m.Segments))
	}
}
