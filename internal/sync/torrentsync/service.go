// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// OtterSync Service orchestrating EraE export and import. Wires an
// Exporter and Importer over a conf.TorrentSyncCfg and exposes the
// high-level Export entrypoint used by the node to publish chain
// archives for torrent distribution.

package torrentsync

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Service orchestrates OtterSync: exporting and importing EraE segments.
type Service struct {
	cfg      *conf.TorrentSyncCfg
	db       kv.RwDB
	eraDir   string
	exporter *Exporter
	importer *Importer
}

// NewService creates a new OtterSync service.
func NewService(cfg *conf.TorrentSyncCfg, db kv.RwDB, eraDir string) *Service {
	return &Service{
		cfg:      cfg,
		db:       db,
		eraDir:   eraDir,
		exporter: NewExporter(db, eraDir, cfg.SegmentSize),
		importer: NewImporter(db, eraDir, cfg.VerifyBlocks),
	}
}

// Export reads the current chain from the database and writes EraE segments
// covering all available blocks.
func (s *Service) Export(ctx context.Context) error {
	// Determine the head block number.
	var headNum uint64
	err := s.db.View(ctx, func(tx kv.Tx) error {
		// Walk backward from a large number to find the highest canonical block.
		// A more efficient approach would be to read the head header, but for
		// simplicity we scan starting from the highest plausible block.
		hash, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if hash == (emptyHash) {
			return fmt.Errorf("no genesis block in database")
		}

		// Binary search for the highest canonical block.
		lo, hi := uint64(0), uint64(1)
		for {
			h, err := rawdb.ReadCanonicalHash(tx, hi)
			if err != nil {
				return err
			}
			if h == (emptyHash) {
				break
			}
			lo = hi
			hi *= 2
			if hi > 1<<40 {
				break
			}
		}
		// Narrow down.
		for lo < hi {
			mid := lo + (hi-lo+1)/2
			h, err := rawdb.ReadCanonicalHash(tx, mid)
			if err != nil {
				return err
			}
			if h == (emptyHash) {
				hi = mid - 1
			} else {
				lo = mid
			}
		}
		headNum = lo
		return nil
	})
	if err != nil {
		return fmt.Errorf("torrentsync: determine head: %w", err)
	}

	if headNum == 0 {
		log.Info("OtterSync: chain is empty, nothing to export")
		return nil
	}

	segments, err := s.exporter.ExportRange(ctx, 0, headNum)
	if err != nil {
		return fmt.Errorf("torrentsync: export: %w", err)
	}

	log.Info("OtterSync export complete",
		"segments", len(segments), "blocks", headNum+1)
	return nil
}

// Import reads available EraE segments from eraDir and inserts them into the database.
func (s *Service) Import(ctx context.Context) error {
	total, err := s.importer.ImportAll(ctx, s.eraDir)
	if err != nil {
		return fmt.Errorf("torrentsync: import: %w", err)
	}
	log.Info("OtterSync import complete", "blocks", total)
	return nil
}

// GenerateManifest scans eraDir and writes a manifest.json file.
func (s *Service) GenerateManifest() error {
	m, err := GenerateManifest(s.eraDir, 0)
	if err != nil {
		return fmt.Errorf("torrentsync: generate manifest: %w", err)
	}
	outPath := filepath.Join(s.eraDir, "manifest.json")
	if err := SaveManifest(m, outPath); err != nil {
		return fmt.Errorf("torrentsync: save manifest: %w", err)
	}
	log.Info("OtterSync manifest generated", "path", outPath, "segments", len(m.Segments))
	return nil
}

// emptyHash is used for zero-value hash comparisons.
var emptyHash [32]byte
