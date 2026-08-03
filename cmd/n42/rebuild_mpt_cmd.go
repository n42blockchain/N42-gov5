// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// `rebuild-mpt` subcommand: regenerate the Ethereum MPT in place.
// Scans the Account and Storage PlainState tables and feeds each
// entry into HexPatriciaHashed via modules/state/commitment so
// the MPTBranch / MPTRoot tables are re-produced without replaying
// any block. Clears previous MPT data first to guarantee a fresh,
// deterministic root.

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func init() {
	rootCmd = append(rootCmd, rebuildMPTCommand)
}

var rebuildMPTCommand = &cli.Command{
	Name:  "rebuild-mpt",
	Usage: "Rebuild Ethereum MPT branches from PlainState (Account + Storage tables)",
	Description: `Reads all accounts and storage from the existing database and feeds them
into HexPatriciaHashed to produce a complete MPT branch set. This allows
computing standard Ethereum state roots without replaying blocks.

The database is modified in-place: MPTBranch and MPTRoot tables are populated.
Existing MPT data is cleared before rebuild.

This follows the Erigon RebuildCommitmentFiles pattern: iterate PlainState,
TouchKey for each entry, ComputeCommitment to generate branches.`,
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "datadir", Usage: "Chain data directory", Required: true},
		&cli.IntFlag{Name: "batch", Usage: "Entries per HPH batch", Value: 100000},
	},
	Action: runRebuildMPT,
}

func runRebuildMPT(cliCtx *cli.Context) error {
	dataDir := cliCtx.String("datadir")
	ctx := context.Background()
	logger := log2.New()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db, err := mdbx.NewMDBX(logger).
		Path(dataDir + "/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(4 * datasize.TB).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open DB: %w", err)
	}
	defer db.Close()

	// Read the expected state root from the latest block header for comparison.
	// Try HeadBlockHash first, then fall back to scanning HeaderCanonical.
	var expectedRoot types.Hash
	var headBlockNum uint64
	if err := db.View(ctx, func(tx kv.Tx) error {
		// Try HeadBlockHash / HeadHeaderHash.
		headHash := rawdb.ReadHeadBlockHash(tx)
		if headHash == (types.Hash{}) {
			headHash = rawdb.ReadHeadHeaderHash(tx)
		}
		if headHash != (types.Hash{}) {
			if num := rawdb.ReadHeaderNumber(tx, headHash); num != nil {
				headBlockNum = *num
				hdr := rawdb.ReadHeader(tx, headHash, headBlockNum)
				if hdr != nil {
					expectedRoot = hdr.StateRoot()
				}
				return nil
			}
		}
		// Fall back: scan HeaderCanonical backwards to find latest block.
		c, err := tx.Cursor(modules.HeaderCanonical)
		if err != nil {
			return nil // non-fatal
		}
		defer c.Close()
		k, v, err := c.Last()
		if err != nil || k == nil {
			return nil
		}
		headBlockNum = binary.BigEndian.Uint64(k)
		var hash types.Hash
		copy(hash[:], v)
		hdr := rawdb.ReadHeader(tx, hash, headBlockNum)
		if hdr != nil {
			expectedRoot = hdr.StateRoot()
		}
		return nil
	}); err != nil {
		return fmt.Errorf("read head: %w", err)
	}
	if expectedRoot != (types.Hash{}) {
		fmt.Printf("Head block: %d, expected stateRoot: %x\n", headBlockNum, expectedRoot)
	}

	// Phase 1: Load all accounts and storage into maps (same format as replay).
	fmt.Println("Phase 1/3: Loading accounts and storage...")
	loadStart := time.Now()

	var accountCount, storageCount uint64
	emptyCodeHash := crypto.Keccak256Hash(nil)

	acctMap := make(map[types.Address]*account.StateAccount)
	storMap := make(map[types.Address]map[types.Hash]*uint256.Int)

	if err := db.View(ctx, func(tx kv.Tx) error {
		c, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer c.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			if len(k) != 20 {
				continue
			}
			var acct account.StateAccount
			if err := acct.DecodeForStorage(v); err != nil {
				continue
			}
			// Normalize: same as stateObject (state_object.go:113-114).
			if acct.CodeHash == (types.Hash{}) {
				acct.CodeHash = emptyCodeHash
			}
			var addr types.Address
			copy(addr[:], k)
			cp := acct.SelfCopy()
			acctMap[addr] = cp
			accountCount++
		}

		sc, err := tx.Cursor(modules.Storage)
		if err != nil {
			return err
		}
		defer sc.Close()
		for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
			if err != nil {
				return err
			}
			if len(k) < 52 {
				continue
			}
			var addr types.Address
			copy(addr[:], k[:20])
			var slot types.Hash
			if len(k) == 54 {
				copy(slot[:], k[22:54])
			} else if len(k) >= 60 {
				copy(slot[:], k[28:60])
			} else {
				continue
			}
			val := new(uint256.Int)
			if len(v) > 0 {
				val.SetBytes(v)
			}
			if val.IsZero() {
				continue
			}
			if storMap[addr] == nil {
				storMap[addr] = make(map[types.Hash]*uint256.Int)
			}
			storMap[addr][slot] = val
			storageCount++
		}
		return nil
	}); err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	fmt.Printf("  Accounts: %d, Storage: %d (%.1fs)\n",
		accountCount, storageCount, time.Since(loadStart).Seconds())

	// Phase 2+3: Compute MPT root and flush branches to MDBX in one write tx.
	fmt.Println("Phase 2/3: Computing MPT root + flushing...")
	var finalRoot types.Hash
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.ClearBucket(modules.MPTBranch); err != nil {
			return err
		}
		if err := tx.ClearBucket(modules.MPTRoot); err != nil {
			return err
		}
		mptRC := commitment.NewMPTRootComputer()
		mptRC.SetStateReader(commitment.NewPlainStateMPTReader(tx))
		var err error
		finalRoot, err = mptRC.ComputeRoot(acctMap, storMap)
		if err != nil {
			return err
		}
		// Flush branches from mem to MDBX.
		if err := mptRC.FlushBranches(tx); err != nil {
			return err
		}
		return commitment.WriteMPTRoot(tx, finalRoot)
	}); err != nil {
		return fmt.Errorf("flush to MDBX: %w", err)
	}

	elapsed := time.Since(loadStart)
	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("MPT rebuild complete\n")
	fmt.Printf("  Entries:    %d accounts + %d storage\n", accountCount, storageCount)
	fmt.Printf("  Root:       %x\n", finalRoot)
	if expectedRoot != (types.Hash{}) {
		if finalRoot == expectedRoot {
			fmt.Printf("  Verify:     MATCH ✓ (block %d stateRoot)\n", headBlockNum)
		} else {
			fmt.Printf("  Expected:   %x\n", expectedRoot)
			fmt.Printf("  Verify:     MISMATCH ✗\n")
		}
	}
	// branch count not available
	fmt.Printf("  Duration:   %s\n", elapsed.Round(time.Second))
	total := accountCount + storageCount
	fmt.Printf("  Rate:       %.0f entries/s\n", float64(total)/elapsed.Seconds())
	fmt.Println("========================================")

	return nil
}
