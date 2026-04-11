// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// `rebuild-trie` subcommand: backup state-root path via CalcTrieRoot.
// Hashes PlainState keys with keccak256, populates HashedAccounts
// and HashedStorage tables and then runs lib/trie CalcTrieRoot
// (Erigon 2.7 FlatDBTrieLoader) to compute the standard Ethereum
// state root. Provides a reference implementation to cross-check
// the HPH-based rebuild-mpt path.

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	rootCmd = append(rootCmd, rebuildTrieCommand)
}

var rebuildTrieCommand = &cli.Command{
	Name:  "rebuild-trie",
	Usage: "Rebuild standard Ethereum trie from PlainState via CalcTrieRoot (erigon2.7)",
	Description: `Reads all accounts and storage from the existing database,
hashes keys with keccak256, populates HashedAccounts + HashedStorage tables,
then runs CalcTrieRoot to compute the standard Ethereum state root.

This is the backup approach using erigon2.7's FlatDBTrieLoader.
Unlike rebuild-mpt (which uses HPH), this builds trie nodes from scratch.`,
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "datadir", Usage: "Chain data directory", Required: true},
	},
	Action: runRebuildTrie,
}

func runRebuildTrie(cliCtx *cli.Context) error {
	dataDir := cliCtx.String("datadir")
	ctx := context.Background()
	logger := log2.New()

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	db, err := mdbx.NewMDBX(logger).
		Path(dataDir+"/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(4 * datasize.TB).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open DB: %w", err)
	}
	defer db.Close()

	// Read the expected state root from the latest block header.
	var expectedRoot types.Hash
	var headBlockNum uint64
	if err := db.View(ctx, func(tx kv.Tx) error {
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
		c, err := tx.Cursor(modules.HeaderCanonical)
		if err != nil {
			return nil
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

	// Phase 1: Populate HashedAccounts + HashedStorage from PlainState.
	fmt.Println("Phase 1/2: Hashing PlainState → HashedAccounts + HashedStorage...")
	hashStart := time.Now()
	var accountCount, storageCount uint64

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		// Clear existing hashed tables.
		if err := tx.ClearBucket(modules.HashedAccounts); err != nil {
			return err
		}
		if err := tx.ClearBucket(modules.HashedStorage); err != nil {
			return err
		}
		if err := tx.ClearBucket(modules.TrieOfAccounts); err != nil {
			return err
		}
		if err := tx.ClearBucket(modules.TrieOfStorage); err != nil {
			return err
		}

		hasher := sha3.NewLegacyKeccak256()

		// Hash accounts: PlainState.Account → HashedAccounts
		ac, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer ac.Close()

		for k, v, err := ac.First(); k != nil; k, v, err = ac.Next() {
			if err != nil {
				return err
			}
			if len(k) != 20 {
				continue
			}
			hasher.Reset()
			hasher.Write(k)
			var addrHash types.Hash
			hasher.Sum(addrHash[:0])

			if err := tx.Put(modules.HashedAccounts, addrHash[:], v); err != nil {
				return err
			}
			accountCount++
		}

		// Hash storage: PlainState.Storage → HashedStorage
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
			// Parse plain key: addr[20] + incarnation[2or8] + slot[32]
			var addr [20]byte
			copy(addr[:], k[:20])
			var slot [32]byte
			var incarnation uint64
			if len(k) == 54 {
				// 20 + 2 + 32 (N42 uint16 incarnation)
				incarnation = uint64(binary.BigEndian.Uint16(k[20:22]))
				copy(slot[:], k[22:54])
			} else if len(k) >= 60 {
				// 20 + 8 + 32 (erigon uint64 incarnation)
				incarnation = binary.BigEndian.Uint64(k[20:28])
				copy(slot[:], k[28:60])
			} else {
				continue
			}

			if len(v) == 0 {
				continue
			}

			// Hash addr and slot
			hasher.Reset()
			hasher.Write(addr[:])
			var addrHash types.Hash
			hasher.Sum(addrHash[:0])

			hasher.Reset()
			hasher.Write(slot[:])
			var slotHash types.Hash
			hasher.Sum(slotHash[:0])

			// Composite key: addrHash[32] + incarnation[8] + slotHash[32]
			var compositeKey [72]byte
			copy(compositeKey[:32], addrHash[:])
			binary.BigEndian.PutUint64(compositeKey[32:40], incarnation)
			copy(compositeKey[40:], slotHash[:])

			if err := tx.Put(modules.HashedStorage, compositeKey[:], v); err != nil {
				return err
			}
			storageCount++
		}

		return nil
	}); err != nil {
		return fmt.Errorf("hash state: %w", err)
	}

	fmt.Printf("  Accounts: %d, Storage: %d (%.1fs)\n",
		accountCount, storageCount, time.Since(hashStart).Seconds())

	// Phase 2: CalcTrieRoot from HashedAccounts + HashedStorage.
	fmt.Println("Phase 2/2: CalcTrieRoot...")
	calcStart := time.Now()

	var finalRoot types.Hash
	if err := db.View(ctx, func(tx kv.Tx) error {
		// Full rebuild: retain everything (empty RetainList = hash all).
		rl := trie.NewRetainList(0)

		// Hash collectors write intermediate hashes to TrieOfAccounts/TrieOfStorage.
		// For a read-only CalcTrieRoot, we pass nil collectors (just compute root).
		loader := trie.NewFlatDBTrieLoader("rebuild-trie", rl, nil, nil, false)

		var err error
		finalRoot, err = loader.CalcTrieRoot(tx, nil)
		return err
	}); err != nil {
		return fmt.Errorf("CalcTrieRoot: %w", err)
	}

	elapsed := time.Since(hashStart)
	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("Trie rebuild complete (CalcTrieRoot)\n")
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
	fmt.Printf("  Hash time:  %s\n", time.Since(hashStart).Round(time.Millisecond)-time.Since(calcStart).Round(time.Millisecond))
	fmt.Printf("  Calc time:  %s\n", time.Since(calcStart).Round(time.Second))
	fmt.Printf("  Total:      %s\n", elapsed.Round(time.Second))
	total := accountCount + storageCount
	fmt.Printf("  Rate:       %.0f entries/s\n", float64(total)/elapsed.Seconds())
	fmt.Println("========================================")

	// Suppress unused import warnings.
	_ = account.StateAccount{}
	_ = logger

	return nil
}
