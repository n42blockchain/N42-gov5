// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// reorg.go — PlainState rollback using stored changesets.
//
// Reorg walks the output freezer's account and storage changeset tables
// from the current head backwards to the given target block and applies
// the stored original values back into MDBX, which unwinds the executor's
// state without re-running transactions. Length-zero changeset values
// mean "deleted — drop the key", matching the encoding produced by
// EncodeAccountChanges / EncodeStorageChanges in changeset_codec.go.

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
		// Revert account changes from freezer.
		if accTable != nil {
			accData, err := accTable.Retrieve(blockNum)
			if err == nil && len(accData) > 0 {
				addrs, values, err := DecodeAccountChanges(accData)
				if err != nil {
					return fmt.Errorf("decode account changes at %d: %w", blockNum, err)
				}
				for i, addr := range addrs {
					if len(values[i]) == 0 {
						tx.Delete(modules.Account, addr[:])
					} else {
						tx.Put(modules.Account, addr[:], values[i])
					}
				}
			}
		}

		// Revert storage changes from freezer.
		if stoTable != nil {
			stoData, err := stoTable.Retrieve(blockNum)
			if err == nil && len(stoData) > 0 {
				keys, values, err := DecodeStorageChanges(stoData)
				if err != nil {
					return fmt.Errorf("decode storage changes at %d: %w", blockNum, err)
				}
				for i, key := range keys {
					if len(values[i]) == 0 {
						tx.Delete(modules.Storage, key)
					} else {
						tx.Put(modules.Storage, key, values[i])
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
