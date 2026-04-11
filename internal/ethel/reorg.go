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
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// Reorg rolls back PlainState to the given target block by reading
// changesets from the output freezer and applying original values.
func Reorg(db kv.RwDB, outFreezer *freezer.Freezer, targetBlock uint64) error {
	tx, err := db.BeginRw(nil)
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
					if len(e.OldValue) == 0 {
						tx.Delete(modules.Account, e.Address[:])
					} else {
						tx.Put(modules.Account, e.Address[:], e.OldValue)
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
