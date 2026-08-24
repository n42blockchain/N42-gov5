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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/snapshot"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CheckpointEntry records a trusted block for fast sync.
type CheckpointEntry struct {
	SourceHead uint64 `json:"sourceHead"`
	Number     uint64 `json:"number"`
	Hash       string `json:"hash"`
}

// RunPostExport creates snapshot, EraE segments, and checkpoint after replay.
func (e *EngineV2) RunPostExport(ctx context.Context) error {
	if e.dstDB == nil {
		return fmt.Errorf("target DB not open")
	}
	targetHead, targetHash, err := e.postReplayHead(ctx)
	if err != nil {
		return fmt.Errorf("resolve target head: %w", err)
	}
	sourceHead, err := e.postReplaySourceHead(ctx)
	if err != nil {
		return fmt.Errorf("resolve replay source head: %w", err)
	}

	if e.cfg.SnapshotAtEnd {
		fmt.Println("Creating final snapshot...")
		if err := e.createSnapshot(ctx, targetHead); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
	}

	if e.cfg.ExportEraE {
		fmt.Println("Exporting EraE segments...")
		if err := e.exportEraE(ctx, targetHead); err != nil {
			return fmt.Errorf("era export: %w", err)
		}
	}

	// Always write checkpoint
	return e.writeCheckpoint(sourceHead, targetHead, targetHash)
}

func (e *EngineV2) postReplaySourceHead(ctx context.Context) (uint64, error) {
	resumeTable := jmtstore.JMTRootTable
	switch e.cfg.TreeType {
	case "bmt":
		resumeTable = bmtstore.BMTRootTable
	case "qmdb":
		resumeTable = modules.QMDBMeta
	case "mpt":
		resumeTable = modules.MPTRoot
	}

	var sourceHead uint64
	err := e.dstDB.View(ctx, func(tx kv.Tx) error {
		data, err := tx.GetOne(resumeTable, []byte("replay_src_height"))
		if err != nil {
			return err
		}
		if len(data) >= 8 {
			sourceHead = binary.BigEndian.Uint64(data)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if sourceHead == 0 {
		// Older tests and callers may not have persisted replay_src_height.
		// CurrentBlock is source progress (never the gap-filled target height),
		// so it is a safe compatibility fallback when non-zero.
		if e.stats != nil && e.stats.CurrentBlock > 0 {
			return e.stats.CurrentBlock, nil
		}
		return 0, fmt.Errorf("replay_src_height is missing from %s", resumeTable)
	}
	return sourceHead, nil
}

func (e *EngineV2) postReplayHead(ctx context.Context) (uint64, types.Hash, error) {
	var (
		number uint64
		hash   types.Hash
	)
	err := e.dstDB.View(ctx, func(tx kv.Tx) error {
		hash = rawdb.ReadHeadBlockHash(tx)
		if hash == (types.Hash{}) {
			return fmt.Errorf("target head block hash is empty")
		}
		n := rawdb.ReadHeaderNumber(tx, hash)
		if n == nil {
			return fmt.Errorf("target head number missing for hash %s", hash)
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *n)
		if err != nil {
			return fmt.Errorf("read target canonical hash at %d: %w", *n, err)
		}
		if canonical != hash {
			return fmt.Errorf("target head is not canonical at %d: head=%s canonical=%s", *n, hash, canonical)
		}
		number = *n
		return nil
	})
	return number, hash, err
}

func (e *EngineV2) createSnapshot(ctx context.Context, targetHead uint64) error {
	return e.dstDB.Update(ctx, func(tx kv.RwTx) error {
		meta := &snapshot.Meta{
			BlockNumber: targetHead,
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

func (e *EngineV2) exportEraE(ctx context.Context, targetHead uint64) error {
	eraDir := filepath.Join(e.cfg.TargetPath, "era")
	os.MkdirAll(eraDir, 0700)
	exporter := torrentsync.NewExporter(e.dstDB, eraDir, e.cfg.EraESegmentSize)
	if _, err := exporter.ExportRange(ctx, 0, targetHead); err != nil {
		return err
	}
	// Generate manifest
	svc := torrentsync.NewService(&conf.TorrentSyncCfg{
		SegmentSize: e.cfg.EraESegmentSize,
	}, e.dstDB, eraDir)
	return svc.GenerateManifest()
}

func (e *EngineV2) writeCheckpoint(sourceHead, targetHead uint64, targetHash types.Hash) error {
	cp := CheckpointEntry{SourceHead: sourceHead, Number: targetHead, Hash: targetHash.Hex()}
	data, _ := json.MarshalIndent(cp, "", "  ")
	path := filepath.Join(e.cfg.TargetPath, "checkpoint.json")
	fmt.Printf("Checkpoint written: source block %d, target block %d → %s\n", sourceHead, targetHead, path)
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
