// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// verify_incremental.go — per-block incremental HPH state-root verification.
//
// Prerequisite: a datadir bootstrapped via rebuild-state --persist-trie so
// HashedAccounts, HashedStorage, TrieOfAccounts and TrieOfStorage are all
// populated at the starting block.
//
// For each block in [start, end]:
//  1. Read acctcs + storcs from the leaves freezer
//  2. Apply to PlainState (Account, Storage)
//  3. Build dirty maps feeding TrieRootComputer (incremental mode)
//  4. ComputeRoot updates HashedAccounts/HashedStorage/TrieOfAccounts/
//     TrieOfStorage incrementally and returns the new state root
//  5. Compare with header.Root from the geth ancient freezer
//  6. On mismatch: log and abort — the first mismatch block is the bug site
//
// Cost per block is O(dirty), typically 1-50 ms depending on tx count.

package ethel

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// VerifyIncrementalOptions controls per-block HPH root verification.
type VerifyIncrementalOptions struct {
	// StartBlock — first block to verify (inclusive). The datadir must be
	// bootstrapped such that its HashedState/TrieOf* reflect the state AT
	// StartBlock-1 (i.e., before applying StartBlock).
	StartBlock uint64
	// EndBlock — last block to verify (inclusive). 0 = no limit.
	EndBlock uint64
	// LeavesDir is the filesystem path to the changeset freezer (containing
	// acctcs.cidx / storcs.cidx). Used to open acctcs/storcs as raw
	// read-only tables with explicit batch-size/compression, bypassing the
	// auto-detect on Freezer.EnsureTableCompressed that misfires when the
	// item count is a multiple of BatchSize.
	LeavesDir string
	// InputFreezer holds geth-format ancient headers for Root comparison.
	InputFreezer *freezer.Freezer
	// Commit every N blocks to MDBX. 0 = commit only at end.
	// Smaller values increase durability; larger values increase throughput.
	CommitEvery uint64
	// UseIncremental opts into the experimental TrieRootComputer incremental
	// mode (populated RetainList + retain TrieOf* cache). As of 2026-04-18
	// this path gives WRONG roots for dirty blocks — kept behind a flag for
	// future debugging. Default is legacy full-rebuild per block, which is
	// correct but O(state) per call (multi-minute per block at 10M scale,
	// so impractical for multi-block bisect — use rebuild-state --verify N
	// with binary-search narrowing instead).
	UseIncremental bool
}

// VerifyIncremental runs per-block HPH root verification.
// Returns nil if every block's computed root matches the header root.
// Returns an error wrapping the first mismatch block number otherwise.
func VerifyIncremental(ctx context.Context, db kv.RwDB, opts VerifyIncrementalOptions) error {
	if opts.LeavesDir == "" {
		return fmt.Errorf("verify-incremental: LeavesDir is empty")
	}
	if opts.InputFreezer == nil {
		return fmt.Errorf("verify-incremental: InputFreezer is nil")
	}
	start := opts.StartBlock
	end := opts.EndBlock
	if end == 0 || end < start {
		return fmt.Errorf("verify-incremental: invalid [start=%d, end=%d)", start, end)
	}
	commitEvery := opts.CommitEvery
	if commitEvery == 0 {
		commitEvery = 10_000
	}

	// Bootstrap check.
	{
		roTx, err := db.BeginRo(ctx)
		if err != nil {
			return fmt.Errorf("verify-incremental: begin ro tx: %w", err)
		}
		ok, err := IsHPHBootstrapped(roTx)
		roTx.Rollback()
		if err != nil {
			return fmt.Errorf("verify-incremental: bootstrap probe: %w", err)
		}
		if !ok {
			return fmt.Errorf("verify-incremental: datadir is not HPH-bootstrapped; run `ethexec rebuild-state --persist-trie --end %d` first", start)
		}
	}

	// Open via NewFreezerTableReadOnly + ForceBatchSize + SetCompressed
	// rather than Freezer.EnsureTableCompressed. The latter goes through
	// newFreezerTableCompressed's auto-detect batch-size probe, which
	// samples items[mid-1] and items[mid]; when total items is a multiple
	// of 64 (BatchSize) those two samples straddle a batch boundary,
	// auto-detect misfires with batchSize=0, and Retrieve silently returns
	// empty data for every block. Matches rebuild-state's open recipe.
	acctTbl, err := freezer.NewFreezerTableReadOnly(opts.LeavesDir, freezer.TableAccountChanges, "c")
	if err != nil {
		return fmt.Errorf("verify-incremental: open acctcs: %w", err)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)

	stoTbl, err := freezer.NewFreezerTableReadOnly(opts.LeavesDir, freezer.TableStorageChanges, "c")
	if err != nil {
		return fmt.Errorf("verify-incremental: open storcs: %w", err)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return fmt.Errorf("verify-incremental: begin rw tx: %w", err)
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(opts.UseIncremental)

	t0 := time.Now()
	lastLog := t0
	var totalDirtyAcct, totalDirtyStor uint64

	rotateTx := func() error {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		tx = nil
		newTx, err := db.BeginRw(ctx)
		if err != nil {
			return fmt.Errorf("rotate begin: %w", err)
		}
		tx = newTx
		trc.SetRwTx(tx)
		return nil
	}

	for blockNum := start; blockNum <= end; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 1. Read block's changeset.
		accData, err := acctTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read acctcs block %d: %w", blockNum, err)
		}
		stoData, err := stoTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read storcs block %d: %w", blockNum, err)
		}

		// 2. Decode changesets.
		var accEntries []AccountChange
		if len(accData) > 0 {
			accEntries, err = DecodeAccountChanges(accData)
			if err != nil {
				return fmt.Errorf("decode acctcs block %d: %w", blockNum, err)
			}
		}
		var stoEntries []StorageChange
		if len(stoData) > 0 {
			stoEntries, err = DecodeStorageChanges(stoData)
			if err != nil {
				return fmt.Errorf("decode storcs block %d: %w", blockNum, err)
			}
		}

		// 3. Apply to PlainState AND build TrieRootComputer's dirty maps.
		accDirty := make(map[types.Address]*account.StateAccount, len(accEntries))
		for _, e := range accEntries {
			if len(e.NewValue) == 0 {
				accDirty[e.Address] = nil
				if err := tx.Delete(modules.Account, e.Address[:]); err != nil {
					return fmt.Errorf("delete account %x block %d: %w", e.Address, blockNum, err)
				}
			} else {
				acc := &account.StateAccount{}
				if err := acc.DecodeForStorage(e.NewValue); err != nil {
					return fmt.Errorf("decode account %x block %d: %w", e.Address, blockNum, err)
				}
				accDirty[e.Address] = acc
				if err := tx.Put(modules.Account, e.Address[:], e.NewValue); err != nil {
					return fmt.Errorf("put account %x block %d: %w", e.Address, blockNum, err)
				}
			}
		}

		stoDirty := make(map[types.Address]map[types.Hash]*uint256.Int, len(stoEntries))
		for _, e := range stoEntries {
			if len(e.CompositeKey) < 52 {
				continue
			}
			var addr types.Address
			var slot types.Hash
			copy(addr[:], e.CompositeKey[:20])
			copy(slot[:], e.CompositeKey[20:52])

			inner, ok := stoDirty[addr]
			if !ok {
				inner = make(map[types.Hash]*uint256.Int)
				stoDirty[addr] = inner
			}

			if len(e.NewValue) == 0 {
				inner[slot] = new(uint256.Int) // zero → delete
				if err := tx.Delete(modules.Storage, e.CompositeKey); err != nil {
					return fmt.Errorf("delete storage %x block %d: %w", e.CompositeKey, blockNum, err)
				}
			} else {
				val := new(uint256.Int).SetBytes(e.NewValue)
				inner[slot] = val
				if err := tx.Put(modules.Storage, e.CompositeKey, e.NewValue); err != nil {
					return fmt.Errorf("put storage %x block %d: %w", e.CompositeKey, blockNum, err)
				}
			}
		}

		// 4. Incremental root computation.
		computed, err := trc.ComputeRoot(accDirty, stoDirty)
		if err != nil {
			return fmt.Errorf("compute root block %d: %w", blockNum, err)
		}

		// 5. Read header root.
		hdrData, err := opts.InputFreezer.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			return fmt.Errorf("read header block %d: %w", blockNum, err)
		}
		hdr, err := DecodeGethHeader(hdrData)
		if err != nil {
			return fmt.Errorf("decode header block %d: %w", blockNum, err)
		}

		// 6. Compare.
		if computed != hdr.Root {
			log.Error("STATE ROOT MISMATCH (incremental)",
				"block", blockNum,
				"computed", computed.Hex(),
				"expected", hdr.Root.Hex(),
				"dirtyAccts", len(accDirty),
				"dirtyStoSlots", sumSlotCount(stoDirty))
			return fmt.Errorf("state root mismatch at block %d", blockNum)
		}

		totalDirtyAcct += uint64(len(accDirty))
		totalDirtyStor += uint64(sumSlotCount(stoDirty))

		// Periodic commit.
		if commitEvery > 0 && (blockNum-start+1)%commitEvery == 0 {
			if err := rotateTx(); err != nil {
				return err
			}
		}

		// Progress log.
		if time.Since(lastLog) > 5*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			done := blockNum - start + 1
			remain := end - blockNum
			bps := float64(done) / time.Since(t0).Seconds()
			etaSec := float64(remain) / bps
			log.Info("verify-incremental progress",
				"block", blockNum,
				"done", done,
				"remain", remain,
				"blk/s", fmt.Sprintf("%.1f", bps),
				"eta", (time.Duration(etaSec) * time.Second).Truncate(time.Second),
				"avgDirtyAccts", totalDirtyAcct/done,
				"avgDirtyStor", totalDirtyStor/done,
				"allocGB", fmt.Sprintf("%.1f", float64(m.Alloc)/1e9))
			lastLog = time.Now()
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("final commit: %w", err)
	}
	tx = nil

	log.Info("verify-incremental COMPLETE — all blocks verified",
		"start", start,
		"end", end,
		"blocks", end-start+1,
		"elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

func sumSlotCount(m map[types.Address]map[types.Hash]*uint256.Int) int {
	n := 0
	for _, s := range m {
		n += len(s)
	}
	return n
}
