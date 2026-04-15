// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// journal_verify.go verifies that leaves_journal can reconstruct PlainState
// and produce correct state roots matching the headers.

package ethel

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// JournalVerifier replays leaves_journal entries onto an empty MDBX PlainState,
// periodically computing the state root and comparing against the header.
type JournalVerifier struct {
	db           kv.RwDB
	inputFreezer *freezer.Freezer // Geth ancient (headers)
	outFreezer   *freezer.Freezer // output (leaves_journal)
	verify       uint64           // 0=disabled, N=every N blocks
	endBlock     uint64
	commitEvery  uint64
}

func NewJournalVerifier(db kv.RwDB, input, output *freezer.Freezer, verify, endBlock uint64) *JournalVerifier {
	return &JournalVerifier{
		db:           db,
		inputFreezer: input,
		outFreezer:   output,
		verify:       verify,
		endBlock:     endBlock,
		commitEvery:  10000,
	}
}

type acctValue struct {
	value []byte // nil = delete
}

type storValue struct {
	value []byte // nil = delete
}

func (v *JournalVerifier) Run(ctx context.Context) error {
	// V2: replay from the unified acctcs + storcs tables. Both must be
	// present and block-aligned; the legacy leaves_journal is no longer
	// produced and not consulted.
	acctTbl := v.outFreezer.Table(freezer.TableAccountChanges)
	stoTbl := v.outFreezer.Table(freezer.TableStorageChanges)
	if acctTbl == nil || stoTbl == nil {
		return fmt.Errorf("acctcs/storcs table not found in output freezer")
	}
	acctTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.ForceBatchSize(freezer.BatchSize)
	maxJournal := acctTbl.Items()
	if stoTbl.Items() < maxJournal {
		maxJournal = stoTbl.Items()
	}
	if maxJournal == 0 {
		return fmt.Errorf("acctcs/storcs is empty")
	}

	endBlock := v.endBlock
	if endBlock == 0 || endBlock > maxJournal {
		endBlock = maxJournal
	}

	// Verify genesis state root (block 0) before replaying journal.
	{
		genTx, err := v.db.BeginRw(ctx)
		if err != nil {
			return err
		}
		genRoot, err := FullStateRootVerify(genTx)
		genTx.Rollback()
		if err != nil {
			log.Error("Genesis root computation failed", "err", err)
		} else {
			headerData, err := v.inputFreezer.Ancient(freezer.TableHeaders, 0)
			if err == nil {
				header, err := DecodeGethHeader(headerData)
				if err == nil {
					if genRoot == header.Root {
						log.Info("Genesis state root OK", "root", genRoot.Hex()[:18])
					} else {
						log.Error("Genesis state root MISMATCH",
							"computed", genRoot.Hex(),
							"expected", header.Root.Hex())
					}
				}
			}
		}
	}

	log.Info("Journal verify starting",
		"journalItems", maxJournal,
		"endBlock", endBlock,
		"verify", v.verify,
		"commitEvery", v.commitEvery)

	tx, err := v.db.BeginRw(ctx)
	if err != nil {
		return err
	}

	// Two typed maps: zero-allocation keys for accounts, composite string for storage.
	acctBuf := make(map[types.Address]acctValue)
	storBuf := make(map[string]storValue) // key = composite 52-byte string (addr+slot)
	var applied, verified, mismatches uint64
	t0 := time.Now()

	flushBuf := func() error {
		// Flush accounts sorted by address.
		if len(acctBuf) > 0 {
			addrs := make([]types.Address, 0, len(acctBuf))
			for a := range acctBuf {
				addrs = append(addrs, a)
			}
			sort.Slice(addrs, func(i, j int) bool {
				return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
			})
			for _, a := range addrs {
				e := acctBuf[a]
				incarnation, err := valueIncarnation(tx, a, e.value)
				if err != nil {
					return err
				}
				if err := applyAccountValue(tx, a, e.value, incarnation); err != nil {
					return err
				}
			}
			acctBuf = make(map[types.Address]acctValue, len(acctBuf))
		}

		// Flush storage sorted by composite key.
		if len(storBuf) > 0 {
			keys := make([]string, 0, len(storBuf))
			for k := range storBuf {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				e := storBuf[k]
				if e.value == nil {
					if err := tx.Delete(modules.Storage, []byte(k)); err != nil {
						return err
					}
				} else {
					if err := tx.Put(modules.Storage, []byte(k), e.value); err != nil {
						return err
					}
				}
			}
			storBuf = make(map[string]storValue, len(storBuf))
		}
		return nil
	}

	commitTx := func(blockNum uint64) error {
		if err := flushBuf(); err != nil {
			return fmt.Errorf("flush at %d: %w", blockNum, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit at %d: %w", blockNum, err)
		}
		tx, err = v.db.BeginRw(ctx)
		if err != nil {
			return err
		}
		return nil
	}

	// Start from block 0: with the fixed executor, journal[0] contains the
	// full genesis state. For old journal data, genesis is pre-loaded via
	// InitEthGenesisState and journal[0] may be stale — verify from block 1.
	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			break
		}

		accData, err := acctTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read acctcs block %d: %w", blockNum, err)
		}
		stoData, err := stoTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read storcs block %d: %w", blockNum, err)
		}

		if len(accData) > 0 {
			entries, err := DecodeAccountChanges(accData)
			if err != nil {
				return fmt.Errorf("decode acctcs block %d: %w", blockNum, err)
			}
			for _, e := range entries {
				acctBuf[e.Address] = acctValue{value: e.NewValue}
			}
		}
		if len(stoData) > 0 {
			entries, err := DecodeStorageChanges(stoData)
			if err != nil {
				return fmt.Errorf("decode storcs block %d: %w", blockNum, err)
			}
			for _, e := range entries {
				storBuf[string(e.CompositeKey)] = storValue{value: e.NewValue}
			}
		}
		applied++

		// Flush + verify at verify boundary.
		// Block 0 is verified as well — with fixed journal[0] containing
		// full genesis state, block 0's root should match.
		if v.verify > 0 && blockNum%v.verify == 0 {
			if err := commitTx(blockNum); err != nil {
				return err
			}

			computedRoot, err := FullStateRootVerify(tx)
			if err != nil {
				return fmt.Errorf("block %d: compute root: %w", blockNum, err)
			}

			headerData, err := v.inputFreezer.Ancient(freezer.TableHeaders, blockNum)
			if err != nil {
				return fmt.Errorf("block %d: read header: %w", blockNum, err)
			}
			header, err := DecodeGethHeader(headerData)
			if err != nil {
				return fmt.Errorf("block %d: decode header: %w", blockNum, err)
			}

			elapsed := time.Since(t0)
			if computedRoot == header.Root {
				log.Info("State root OK",
					"block", blockNum,
					"root", computedRoot.Hex()[:18],
					"elapsed", elapsed.Truncate(time.Second))
				verified++
			} else {
				log.Error("State root MISMATCH",
					"block", blockNum,
					"computed", computedRoot.Hex(),
					"expected", header.Root.Hex())
				mismatches++
			}
			continue
		}

		// Periodic flush + commit (no verification).
		if blockNum%v.commitEvery == 0 {
			if err := commitTx(blockNum); err != nil {
				return err
			}
		}

		// Progress log.
		if applied%100000 == 0 {
			elapsed := time.Since(t0)
			blkPerSec := float64(applied) / elapsed.Seconds()
			pct := float64(blockNum) / float64(endBlock) * 100
			log.Info("Journal replay",
				"block", blockNum,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"blk/s", fmt.Sprintf("%.0f", blkPerSec),
				"acctBuf", len(acctBuf),
				"storBuf", len(storBuf))
		}
	}

	// Final flush + commit.
	if err := commitTx(endBlock); err != nil {
		return err
	}
	tx.Rollback() // no-op if already committed

	elapsed := time.Since(t0)
	log.Info("Journal verify complete",
		"applied", applied,
		"verified", verified,
		"mismatches", mismatches,
		"elapsed", elapsed.Truncate(time.Second))

	if mismatches > 0 {
		return fmt.Errorf("%d state root mismatches", mismatches)
	}

	// --- Revert test: undo blocks backwards using changesets ---
	if v.verify > 0 && endBlock > 1 {
		if err := v.revertTest(ctx, endBlock); err != nil {
			return err
		}
	}

	return nil
}

// revertTest replays journal once forward to endBlock-1, then reverts
// backwards through checkpoints, verifying each state root. O(N) total.
func (v *JournalVerifier) revertTest(ctx context.Context, endBlock uint64) error {
	accTbl := v.outFreezer.Table(freezer.TableAccountChanges)
	stoTbl := v.outFreezer.Table(freezer.TableStorageChanges)
	if accTbl == nil || stoTbl == nil {
		log.Warn("Revert test skipped: changeset tables not found")
		return nil
	}
	accTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.ForceBatchSize(freezer.BatchSize)

	// Pick checkpoints in descending order.
	step := endBlock / 7
	if step < 1 {
		step = 1
	}
	var checkpoints []uint64
	for b := endBlock - 1; b > 0 && len(checkpoints) < 5; b -= step {
		checkpoints = append(checkpoints, b)
	}

	maxBlock := checkpoints[0]
	log.Info("Revert test starting",
		"replayTo", maxBlock,
		"checkpoints", len(checkpoints))

	// 1. Replay forward from 0 to maxBlock (once).
	tx, err := v.db.BeginRw(ctx)
	if err != nil {
		return err
	}
	if err := clearAllState(tx); err != nil {
		tx.Rollback()
		return fmt.Errorf("clear state: %w", err)
	}

	for b := uint64(0); b <= maxBlock; b++ {
		accData, err := accTbl.Retrieve(b)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("acctcs read %d: %w", b, err)
		}
		stoData, err := stoTbl.Retrieve(b)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("storcs read %d: %w", b, err)
		}
		if err := applyChangesetForward(tx, accData, stoData); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply forward %d: %w", b, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// 2. Revert backwards through checkpoints.
	cpIdx := 0
	for revertFrom := maxBlock; revertFrom > 0 && cpIdx < len(checkpoints); revertFrom-- {
		if ctx.Err() != nil {
			break
		}

		tx, err := v.db.BeginRw(ctx)
		if err != nil {
			return err
		}
		if err := applyChangeset(tx, accTbl, stoTbl, revertFrom); err != nil {
			tx.Rollback()
			return fmt.Errorf("revert %d: %w", revertFrom, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		// Check if this is a checkpoint to verify.
		if revertFrom == checkpoints[cpIdx] {
			tx, err := v.db.BeginRw(ctx)
			if err != nil {
				return err
			}
			computedRoot, err := FullStateRootVerify(tx)
			tx.Rollback()
			if err != nil {
				return fmt.Errorf("revert verify %d: %w", revertFrom, err)
			}

			expectedBlock := revertFrom - 1
			headerData, err := v.inputFreezer.Ancient(freezer.TableHeaders, expectedBlock)
			if err != nil {
				return fmt.Errorf("read header %d: %w", expectedBlock, err)
			}
			header, err := DecodeGethHeader(headerData)
			if err != nil {
				return fmt.Errorf("decode header %d: %w", expectedBlock, err)
			}

			if computedRoot == header.Root {
				log.Info("Revert OK",
					"reverted", revertFrom,
					"stateAt", expectedBlock,
					"root", computedRoot.Hex()[:18])
			} else {
				log.Error("Revert MISMATCH",
					"reverted", revertFrom,
					"stateAt", expectedBlock,
					"computed", computedRoot.Hex(),
					"expected", header.Root.Hex())
				return fmt.Errorf("revert mismatch at block %d", revertFrom)
			}
			cpIdx++
		}
	}

	log.Info("Revert test complete", "tests", cpIdx)
	return nil
}

// applyChangesetForward reads per-block V2 changesets and applies each
// entry's NEW value to MDBX. This is the forward-replay path that
// replaces the legacy applyJournalEntry (which decoded leaves journal
// blobs).
func applyChangesetForward(tx kv.RwTx, accData, stoData []byte) error {
	if len(accData) > 0 {
		entries, err := DecodeAccountChanges(accData)
		if err != nil {
			return err
		}
		for _, e := range entries {
			incarnation, err := valueIncarnation(tx, e.Address, e.NewValue)
			if err != nil {
				return err
			}
			if err := applyAccountValue(tx, e.Address, e.NewValue, incarnation); err != nil {
				return err
			}
		}
	}
	if len(stoData) > 0 {
		entries, err := DecodeStorageChanges(stoData)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(e.NewValue) == 0 {
				if err := tx.Delete(modules.Storage, e.CompositeKey); err != nil {
					return err
				}
			} else {
				if err := tx.Put(modules.Storage, e.CompositeKey, e.NewValue); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// applyChangeset reads account+storage changesets for blockNum and writes
// the OLD values back to MDBX, effectively reverting that block.
//
// Account OLD values omit CodeHash by design. The historical incarnation is
// carried in the reserved V2 field-bit slot so the correct code version can be
// restored from PlainContractCode during backward unwind.
func applyChangeset(tx kv.RwTx, accTbl, stoTbl *freezer.FreezerTable, blockNum uint64) error {
	accData, err := accTbl.Retrieve(blockNum)
	if err != nil {
		return fmt.Errorf("read acc cs: %w", err)
	}
	if len(accData) > 0 {
		entries, err := DecodeAccountChanges(accData)
		if err != nil {
			return fmt.Errorf("decode acc cs: %w", err)
		}
		for _, e := range entries {
			incarnation, err := revertIncarnation(tx, e.Address, e.OldValue, e.NewValue)
			if err != nil {
				return err
			}
			if err := applyAccountValue(tx, e.Address, e.OldValue, incarnation); err != nil {
				return err
			}
		}
	}

	stoData, err := stoTbl.Retrieve(blockNum)
	if err != nil {
		return fmt.Errorf("read sto cs: %w", err)
	}
	if len(stoData) > 0 {
		entries, err := DecodeStorageChanges(stoData)
		if err != nil {
			return fmt.Errorf("decode sto cs: %w", err)
		}
		for _, e := range entries {
			if len(e.OldValue) == 0 {
				if err := tx.Delete(modules.Storage, e.CompositeKey); err != nil {
					return err
				}
			} else {
				if err := tx.Put(modules.Storage, e.CompositeKey, e.OldValue); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// deleteStorageByPrefix deletes all storage entries for a given address.
func deleteStorageByPrefix(tx kv.RwTx, addr types.Address) error {
	cursor, err := tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	var keysToDelete [][]byte
	for k, _, err := cursor.Seek(addr[:]); k != nil; k, _, err = cursor.Next() {
		if err != nil {
			cursor.Close()
			return err
		}
		if len(k) < 20 || !bytes.Equal(k[:20], addr[:]) {
			break
		}
		keysToDelete = append(keysToDelete, append([]byte{}, k...))
	}
	cursor.Close()
	for _, k := range keysToDelete {
		if err := tx.Delete(modules.Storage, k); err != nil {
			return err
		}
	}
	return nil
}

// clearAllState deletes all entries from Account and Storage tables.
func clearAllState(tx kv.RwTx) error {
	if err := tx.ClearBucket(modules.Account); err != nil {
		return err
	}
	if err := tx.ClearBucket(modules.Storage); err != nil {
		return err
	}
	if err := tx.ClearBucket(modules.PlainContractCode); err != nil {
		return err
	}
	return tx.ClearBucket(modules.IncarnationMap)
}
