// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// Reorg rolls back the PlainState to the given target block number by
// replaying changesets in reverse (applying original values).
func Reorg(db kv.RwDB, targetBlock uint64) error {
	tx, err := db.BeginRw(nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	currentHead := ReadProgress(tx)
	if currentHead <= targetBlock {
		return nil // already at or before target
	}

	log.Info("Reorg: rolling back state", "from", currentHead, "to", targetBlock)

	// Roll back block by block from currentHead down to targetBlock+1.
	for blockNum := currentHead; blockNum > targetBlock; blockNum-- {
		if err := revertBlock(tx, blockNum); err != nil {
			return fmt.Errorf("revert block %d: %w", blockNum, err)
		}
	}

	// Update progress.
	if err := WriteProgress(tx, targetBlock); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Info("Reorg complete", "head", targetBlock)
	return nil
}

// revertBlock applies the original (pre-execution) values from changesets
// to restore PlainState to the state before this block was executed.
func revertBlock(tx kv.RwTx, blockNum uint64) error {
	// Revert account changes.
	if err := revertAccountChanges(tx, blockNum); err != nil {
		return err
	}
	// Revert storage changes.
	if err := revertStorageChanges(tx, blockNum); err != nil {
		return err
	}
	return nil
}

// revertAccountChanges reads the AccountChangeSet for blockNum and restores
// original account values in the Account table.
func revertAccountChanges(tx kv.RwTx, blockNum uint64) error {
	c, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return err
	}
	defer c.Close()

	var blockKey [8]byte
	binary.BigEndian.PutUint64(blockKey[:], blockNum)

	// AccountChangeSet: DupSort value = address(20) + encoded_original_account
	for _, v, err := c.SeekExact(blockKey[:]); v != nil; _, v, err = c.NextDup() {
		if err != nil {
			return err
		}
		if len(v) < 20 {
			continue
		}
		addr := v[:20]
		origVal := v[20:]
		if len(origVal) == 0 {
			// Account didn't exist before this block.
			if err := tx.Delete(modules.Account, addr); err != nil {
				return err
			}
		} else {
			// Restore original account data.
			if err := tx.Put(modules.Account, addr, origVal); err != nil {
				return err
			}
		}
	}

	// Delete the changeset entry itself.
	if err := tx.Delete(modules.AccountChangeSet, blockKey[:]); err != nil {
		return err
	}

	return nil
}

// revertStorageChanges reads the StorageChangeSet for blockNum and restores
// original storage values.
func revertStorageChanges(tx kv.RwTx, blockNum uint64) error {
	c, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		return err
	}
	defer c.Close()

	var blockKey [8]byte
	binary.BigEndian.PutUint64(blockKey[:], blockNum)

	// StorageChangeSet dupSort value = address(20) + incarnation(2) + storageKey(32) + originalValue
	for _, v, err := c.SeekExact(blockKey[:]); v != nil; _, v, err = c.NextDup() {
		if err != nil {
			return err
		}
		if len(v) < 54 { // 20+2+32
			continue
		}
		compositeKey := v[:54] // address + incarnation + slot
		origVal := v[54:]
		if len(origVal) == 0 {
			if err := tx.Delete(modules.Storage, compositeKey); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Storage, compositeKey, origVal); err != nil {
				return err
			}
		}
	}

	if err := tx.Delete(modules.StorageChangeSet, blockKey[:]); err != nil {
		return err
	}

	return nil
}

