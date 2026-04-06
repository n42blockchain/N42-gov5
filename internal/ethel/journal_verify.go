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

	"github.com/n42blockchain/N42/common/account"
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
	journalTbl := v.outFreezer.Table("leaves_journal")
	if journalTbl == nil {
		return fmt.Errorf("leaves_journal table not found in output freezer")
	}
	// Force batch mode — journal was written with WriteBatch(batch=64).
	journalTbl.ForceBatchSize(freezer.BatchSize)
	maxJournal := journalTbl.Items()
	if maxJournal == 0 {
		return fmt.Errorf("leaves_journal is empty")
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
	storBuf := make(map[string]storValue) // key = composite 54-byte string
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
				if e.value == nil {
					if err := tx.Delete(modules.Account, a[:]); err != nil {
						return err
					}
				} else {
					if err := tx.Put(modules.Account, a[:], e.value); err != nil {
						return err
					}
					// Maintain PlainContractCode for changeset revert.
					var acc account.StateAccount
					if err := acc.DecodeForStorage(e.value); err == nil && !acc.IsEmptyCodeHash() {
						key := modules.PlainGenerateStoragePrefix(a[:])
						if err := tx.Put(modules.PlainContractCode, key, acc.CodeHash[:]); err != nil {
							return err
						}
					}
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

		data, err := journalTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read journal block %d: %w", blockNum, err)
		}

		if len(data) > 0 {
			accounts, storage, err := DecodeLeavesJournal(data)
			if err != nil {
				return fmt.Errorf("decode journal block %d: %w", blockNum, err)
			}

			for _, leaf := range accounts {
				acctBuf[leaf.Address] = acctValue{value: leaf.Value}
			}
			for _, leaf := range storage {
				key := modules.PlainGenerateCompositeStorageKey(
					leaf.Address[:], leaf.Slot[:])
				storBuf[string(key)] = storValue{value: leaf.Value}
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
	accTbl := v.outFreezer.Table("account_changes")
	stoTbl := v.outFreezer.Table("storage_changes")
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

	journalTbl := v.outFreezer.Table("leaves_journal")
	journalTbl.ForceBatchSize(freezer.BatchSize)

	for b := uint64(0); b <= maxBlock; b++ {
		data, err := journalTbl.Retrieve(b)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("journal read %d: %w", b, err)
		}
		if err := applyJournalEntry(tx, data); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply journal %d: %w", b, err)
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

// applyJournalEntry decodes and writes journal leaves to MDBX.
// Also maintains PlainContractCode for contract accounts so that
// changeset revert can recover codeHash.
func applyJournalEntry(tx kv.RwTx, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	accounts, storage, err := DecodeLeavesJournal(data)
	if err != nil {
		return err
	}
	for _, leaf := range accounts {
		if leaf.Value == nil {
			if err := tx.Delete(modules.Account, leaf.Address[:]); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Account, leaf.Address[:], leaf.Value); err != nil {
				return err
			}
			// Maintain PlainContractCode for contract accounts.
			var acc account.StateAccount
			if err := acc.DecodeForStorage(leaf.Value); err == nil && !acc.IsEmptyCodeHash() {
				key := modules.PlainGenerateStoragePrefix(leaf.Address[:])
				if err := tx.Put(modules.PlainContractCode, key, acc.CodeHash[:]); err != nil {
					return err
				}
			}
		}
	}
	for _, leaf := range storage {
		key := modules.PlainGenerateCompositeStorageKey(
			leaf.Address[:], leaf.Slot[:])
		if leaf.Value == nil {
			if err := tx.Delete(modules.Storage, key); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Storage, key, leaf.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyChangeset reads account+storage changesets for blockNum and writes
// the OLD values back to MDBX, effectively reverting that block.
//
// Changeset account values have codeHash omitted (omitHashes=true in Erigon
// convention). For contract accounts (incarnation > 0), the codeHash is
// recovered from the PlainContractCode table (addr+incarnation → codeHash).
func applyChangeset(tx kv.RwTx, accTbl, stoTbl *freezer.FreezerTable, blockNum uint64) error {
	accData, err := accTbl.Retrieve(blockNum)
	if err != nil {
		return fmt.Errorf("read acc cs: %w", err)
	}
	if len(accData) > 0 {
		addrs, values, err := DecodeAccountChanges(accData)
		if err != nil {
			return fmt.Errorf("decode acc cs: %w", err)
		}
		for i, addr := range addrs {
			if len(values[i]) == 0 {
				if err := tx.Delete(modules.Account, addr[:]); err != nil {
					return err
				}
			} else {
				// Recover codeHash from PlainContractCode if omitted.
				restored, err := recoverCodeHash(tx, addr[:], values[i])
				if err != nil {
					return err
				}
				if err := tx.Put(modules.Account, addr[:], restored); err != nil {
					return err
				}
			}
		}
	}

	stoData, err := stoTbl.Retrieve(blockNum)
	if err != nil {
		return fmt.Errorf("read sto cs: %w", err)
	}
	if len(stoData) > 0 {
		keys, values, err := DecodeStorageChanges(stoData)
		if err != nil {
			return fmt.Errorf("decode sto cs: %w", err)
		}
		for i, key := range keys {
			if len(values[i]) == 0 {
				if err := tx.Delete(modules.Storage, key); err != nil {
					return err
				}
			} else {
				if err := tx.Put(modules.Storage, key, values[i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// recoverCodeHash restores the codeHash in a V2-encoded account value.
// Changeset entries use omitHashes=true (Erigon convention), which replaces
// codeHash with emptyCodeHash. For contract accounts (incarnation > 0),
// the real codeHash is looked up from the PlainContractCode table.
func recoverCodeHash(tx kv.Tx, addr, encodedValue []byte) ([]byte, error) {
	var acc account.StateAccount
	if err := acc.DecodeForStorage(encodedValue); err != nil {
		return encodedValue, nil // can't decode, return as-is
	}
	if !acc.IsEmptyCodeHash() {
		return encodedValue, nil // codeHash already present
	}
	// Look up codeHash from PlainContractCode.
	codeHash, err := tx.GetOne(modules.PlainContractCode,
		modules.PlainGenerateStoragePrefix(addr))
	if err != nil {
		return nil, err
	}
	if len(codeHash) > 0 {
		copy(acc.CodeHash[:], codeHash)
		return acc.MarshalV2(), nil
	}
	return encodedValue, nil
}

// clearAllState deletes all entries from Account and Storage tables.
func clearAllState(tx kv.RwTx) error {
	if err := tx.ClearBucket(modules.Account); err != nil {
		return err
	}
	return tx.ClearBucket(modules.Storage)
}
