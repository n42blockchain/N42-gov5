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
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Reorg rolls back PlainState to the given target block by reading
// changesets from the output freezer and applying original values.
func Reorg(db kv.RwDB, outFreezer *freezer.Freezer, targetBlock uint64) error {
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	currentHead := ReadProgress(tx)
	if currentHead <= targetBlock {
		return nil
	}

	log.Info("Reorg: rolling back state", "from", currentHead, "to", targetBlock)

	accTable := outFreezer.Table(freezer.TableAccountChanges)
	stoTable := outFreezer.Table(freezer.TableStorageChanges)

	// Sanity check: storcs/acctcs MUST exist for every block in the unwind
	// range. Without them Reorg silently no-ops the state revert and only
	// updates the progress marker, leaving PlainState with future-block
	// values while progress points to a past block. Subsequent forward
	// replay reads "future" values from PlainState and diverges from
	// mainnet (root cause of V4-class drift, observed at 12501844 / 12617540).
	// Also catches the case where freezer was truncated independently of MDBX.
	if accTable != nil || stoTable != nil {
		missingStorcs := uint64(0)
		missingAcctcs := uint64(0)
		var firstMissing uint64
		for blk := targetBlock + 1; blk <= currentHead; blk++ {
			if stoTable != nil {
				if data, err := stoTable.Retrieve(blk); err != nil || data == nil {
					if missingStorcs == 0 {
						firstMissing = blk
					}
					missingStorcs++
				}
			}
			if accTable != nil {
				if data, err := accTable.Retrieve(blk); err != nil || data == nil {
					missingAcctcs++
				}
			}
		}
		if missingStorcs > 0 || missingAcctcs > 0 {
			return fmt.Errorf("Reorg: storcs/acctcs incomplete in unwind range [%d, %d]: missing %d storcs and %d acctcs entries (first missing block: %d). PlainState cannot be safely reverted; would leave datadir in half-unwound state (state ahead, progress behind). Either rebuild changesets first or restart from a clean snapshot",
				targetBlock+1, currentHead, missingStorcs, missingAcctcs, firstMissing)
		}
	}

	for blockNum := currentHead; blockNum > targetBlock; blockNum-- {
		// Revert account changes from freezer (apply OLD values).
		if accTable != nil {
			accData, err := accTable.Retrieve(blockNum)
			if err == nil && len(accData) > 0 {
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
		}

		// Revert storage changes from freezer (apply OLD values).
		if stoTable != nil {
			stoData, err := stoTable.Retrieve(blockNum)
			if err == nil && len(stoData) > 0 {
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
