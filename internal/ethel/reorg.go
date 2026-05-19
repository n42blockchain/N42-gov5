// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reorg.go — PlainState rollback using stored V2 changesets.
//
// Reorg walks the output freezer's unified account and storage changeset
// tables from the current head backwards to the target block and applies
// each entry's OLD value back into MDBX. This unwinds the executor's
// state without re-running the EVM. Length-zero OLD values mean the key
// did not exist before the block — unwind deletes it. Matches the V2
// encoding produced by EncodeAccountChanges / EncodeStorageChanges
// in changeset_codec.go.

package ethel

import (
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/internal/cs"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Reorg rolls back PlainState to the given target block by reading
// changesets from the output freezer and applying original values.
//
// This is a back-compat wrapper that uses the full freezer as the
// changeset source. New callers should use ReorgWithSource and pass
// a cs.Source (FreezerSource / WarmSource / TieredSource).
func Reorg(db kv.RwDB, outFreezer *freezer.Freezer, targetBlock uint64) error {
	return ReorgWithSource(db, cs.NewFreezerSource(outFreezer), targetBlock)
}

// ReorgWithSource rolls back PlainState to the given target block via
// the provided changeset Source. The source decides which blocks are
// available; sourcing a block outside the source's window aborts the
// reorg with cs.ErrDeepReorg (descriptive message includes the
// source's window).
//
// This abstraction enables the warm CS tier: after cmd/n42-cs-prune
// drops old changesets to save disk, callers wire a WarmSource into
// the reorg path. Deep-reorg requests beyond the warm window
// fail-loud rather than silently mis-reverting state (the V4-class
// drift bug observed at 12,501,844 was the original motivation for
// the pre-flight sanity check now generalized here).
func ReorgWithSource(db kv.RwDB, src cs.Source, targetBlock uint64) error {
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	currentHead := ReadProgress(tx)
	if currentHead <= targetBlock {
		return nil
	}

	log.Info("Reorg: rolling back state",
		"from", currentHead, "to", targetBlock, "source", src.WindowDescription())

	// Pre-flight sanity check: EVERY block in the unwind range must be
	// available from src. Generalizes the original freezer-only check —
	// covers warm tier (out-of-window deep reorgs) and the historical
	// V4-class drift bug (12,501,844 / 12,617,540) where missing
	// changesets caused silent partial unwinds.
	for blk := targetBlock + 1; blk <= currentHead; blk++ {
		if !src.Available(blk) {
			return fmt.Errorf("Reorg: block %d not available from source (%s). "+
				"Unwind range [%d, %d] cannot be safely applied; would leave datadir half-reverted (state ahead, progress behind). "+
				"Recovery: re-execute via EVM from a known snapshot, or reload full archive from a blake2b-verified bundle. %w",
				blk, src.WindowDescription(), targetBlock+1, currentHead, cs.ErrDeepReorg)
		}
	}

	for blockNum := currentHead; blockNum > targetBlock; blockNum-- {
		// Revert account changes (apply OLD values).
		accData, err := src.RetrieveAccount(blockNum)
		if err != nil && !errors.Is(err, cs.ErrDeepReorg) {
			return fmt.Errorf("retrieve account changes at %d: %w", blockNum, err)
		}
		if len(accData) > 0 {
			entries, err := DecodeAccountChanges(accData)
			if err != nil {
				return fmt.Errorf("decode account changes at %d: %w", blockNum, err)
			}
			for _, e := range entries {
				if err := applyAccountValue(tx, e.Address, e.OldValue); err != nil {
					return err
				}
			}
		}

		// Revert storage changes (apply OLD values).
		stoData, err := src.RetrieveStorage(blockNum)
		if err != nil && !errors.Is(err, cs.ErrDeepReorg) {
			return fmt.Errorf("retrieve storage changes at %d: %w", blockNum, err)
		}
		if len(stoData) > 0 {
			entries, err := DecodeStorageChanges(stoData)
			if err != nil {
				return fmt.Errorf("decode storage changes at %d: %w", blockNum, err)
			}
			for _, e := range entries {
				if len(e.OldValue) == 0 {
					tx.Delete(modules.Storage, e.CompositeKey)
				} else {
					tx.Put(modules.Storage, e.CompositeKey, e.OldValue)
				}
			}
		}
	}

	if err := WriteProgress(tx, targetBlock); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("Reorg complete", "head", targetBlock)
	return nil
}
