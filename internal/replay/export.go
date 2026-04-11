// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Post-replay export helpers. RunPostExport produces a final snapshot,
// EraE archive segments via torrentsync and a CheckpointEntry JSON
// listing trusted block heights, so other nodes can fast-sync from
// the freshly-rebuilt database.

package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CheckpointEntry records a trusted block for fast sync.
type CheckpointEntry struct {
	Number uint64 `json:"number"`
	Hash   string `json:"hash"`
}

// RunPostExport creates snapshot, EraE segments, and checkpoint after replay.
func (e *EngineV2) RunPostExport(ctx context.Context) error {
	if e.dstDB == nil {
		return fmt.Errorf("target DB not open")
	}

	if e.cfg.SnapshotAtEnd {
		fmt.Println("Creating final snapshot...")
		if err := e.createSnapshot(ctx); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
	}

	if e.cfg.ExportEraE {
		fmt.Println("Exporting EraE segments...")
		if err := e.exportEraE(ctx); err != nil {
			return fmt.Errorf("era export: %w", err)
		}
	}

	// Always write checkpoint
	return e.writeCheckpoint(ctx)
}

func (e *EngineV2) createSnapshot(ctx context.Context) error {
	return e.dstDB.Update(ctx, func(tx kv.RwTx) error {
		meta := &snapshot.Meta{
			BlockNumber: e.stats.CurrentBlock,
		}
		// Count tables
		e.dstDB.View(ctx, func(roTx kv.Tx) error {
			meta.AccountCount, _ = countTableEntries(roTx, "Account")
			meta.StorageCount, _ = countTableEntries(roTx, "Storage")
			meta.CodeCount, _ = countTableEntries(roTx, "Code")
			return nil
		})
		meta.CreatedAt = e.stats.StartTime.Unix()
		return snapshot.SaveSnapshot(tx, meta)
	})
}

func (e *EngineV2) exportEraE(ctx context.Context) error {
	eraDir := filepath.Join(e.cfg.TargetPath, "era")
	os.MkdirAll(eraDir, 0700)
	exporter := torrentsync.NewExporter(e.dstDB, eraDir, e.cfg.EraESegmentSize)
	if _, err := exporter.ExportRange(ctx, 0, e.stats.CurrentBlock); err != nil {
		return err
	}
	// Generate manifest
	svc := torrentsync.NewService(&conf.TorrentSyncCfg{
		SegmentSize: e.cfg.EraESegmentSize,
	}, e.dstDB, eraDir)
	return svc.GenerateManifest()
}

func (e *EngineV2) writeCheckpoint(ctx context.Context) error {
	var hash string
	e.dstDB.View(ctx, func(tx kv.Tx) error {
		h, _ := rawdb.ReadCanonicalHash(tx, e.stats.CurrentBlock)
		hash = fmt.Sprintf("0x%x", h)
		return nil
	})
	cp := CheckpointEntry{Number: e.stats.CurrentBlock, Hash: hash}
	data, _ := json.MarshalIndent(cp, "", "  ")
	path := filepath.Join(e.cfg.TargetPath, "checkpoint.json")
	fmt.Printf("Checkpoint written: block %d → %s\n", e.stats.CurrentBlock, path)
	return os.WriteFile(path, data, 0644)
}

func countTableEntries(tx kv.Tx, table string) (uint64, error) {
	cursor, err := tx.Cursor(table)
	if err != nil {
		return 0, err
	}
	defer cursor.Close()
	count, err := cursor.Count()
	return count, err
}
