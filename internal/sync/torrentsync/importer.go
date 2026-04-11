// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Importer reads EraE segment files and reinserts the blocks into the
// local KV database. ImportFile opens a segment via era.OpenReader and
// optionally runs block-level verification driven by the verify flag
// set at NewImporter time.

package torrentsync

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/era"
)

// Importer imports EraE segment files into the database.
type Importer struct {
	db     kv.RwDB
	eraDir string
	verify bool
}

// NewImporter creates an Importer that reads EraE segments and writes blocks to db.
func NewImporter(db kv.RwDB, eraDir string, verify bool) *Importer {
	return &Importer{
		db:     db,
		eraDir: eraDir,
		verify: verify,
	}
}

// ImportFile reads a single EraE file and inserts its blocks into the database.
// It returns the number of blocks imported.
func (imp *Importer) ImportFile(ctx context.Context, eraPath string) (imported uint64, err error) {
	r, err := era.OpenReader(eraPath)
	if err != nil {
		return 0, fmt.Errorf("torrentsync: open era file: %w", err)
	}
	defer r.Close()

	first, last := r.BlockRange()

	var prevHash types.Hash

	// If we're verifying and not starting at genesis, load the parent hash
	// from the database.
	if imp.verify && first > 0 {
		err = imp.db.View(ctx, func(tx kv.Tx) error {
			parentBlock, err := rawdb.ReadBlockByNumber(tx, first-1)
			if err != nil {
				return fmt.Errorf("read parent block %d: %w", first-1, err)
			}
			if parentBlock != nil {
				prevHash = parentBlock.Hash()
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}

	err = imp.db.Update(ctx, func(tx kv.RwTx) error {
		for num := first; num <= last; num++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			blockData, _, err := r.ReadRecord(num)
			if err != nil {
				return fmt.Errorf("read record %d: %w", num, err)
			}

			var blk block.Block
			if err := blk.Unmarshal(blockData); err != nil {
				return fmt.Errorf("unmarshal block %d: %w", num, err)
			}

			// Verify block hash chain: parent hash must match previous block's hash.
			// For the first block in the segment (num == first), prevHash is either
			// zero (genesis) or loaded from the DB (when first > 0).
			if imp.verify && (num > first || (num == first && first > 0)) {
				header, ok := blk.Header().(*block.Header)
				if !ok {
					return fmt.Errorf("block %d: unexpected header type", num)
				}
				if header.ParentHash != prevHash {
					return fmt.Errorf("block %d: parent hash mismatch: want %s, got %s",
						num, prevHash.Hex(), header.ParentHash.Hex())
				}
			}

			if err := rawdb.WriteBlock(tx, &blk); err != nil {
				return fmt.Errorf("write block %d: %w", num, err)
			}

			if err := rawdb.WriteCanonicalHash(tx, blk.Hash(), num); err != nil {
				return fmt.Errorf("write canonical hash %d: %w", num, err)
			}

			prevHash = blk.Hash()
			imported++
		}
		return nil
	})
	if err != nil {
		return imported, err
	}

	log.Info("Imported EraE segment", "file", filepath.Base(eraPath),
		"from", first, "to", last, "imported", imported)
	return imported, nil
}

// ImportAll scans eraDir for .era files and imports them in order.
// Returns the total number of blocks imported across all files.
func (imp *Importer) ImportAll(ctx context.Context, eraDir string) (uint64, error) {
	files, err := listEraFiles(eraDir)
	if err != nil {
		return 0, err
	}

	var total uint64
	for _, f := range files {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}

		n, err := imp.ImportFile(ctx, f)
		if err != nil {
			return total, fmt.Errorf("import %s: %w", filepath.Base(f), err)
		}
		total += n
	}
	return total, nil
}
