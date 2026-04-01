// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"fmt"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func init() {
	rootCmd = append(rootCmd, migrateJMTCommand)
}

var migrateJMTCommand = &cli.Command{
	Name:  "migrate-jmt",
	Usage: "Build JMT state commitment from existing flat Account/Storage tables",
	Description: `Offline migration tool that scans all accounts and storage slots
in the existing MDBX database and builds a Jellyfish Merkle Tree (JMT) with
Blake3 hashing. The JMT nodes are written to the JMTNode table, and the root
hash is stored in JMTRoot.

This must be run BEFORE enabling jmt_commitment in the config. The node must
be stopped during migration.

Supports crash recovery: if interrupted, re-run the command and it will
resume from the last checkpoint.`,
	Flags: []cli.Flag{
		DataDirFlag,
		&cli.IntFlag{
			Name:  "batch-size",
			Usage: "Number of accounts per batch commit",
			Value: 10000,
		},
		&cli.BoolFlag{
			Name:  "verify",
			Usage: "Verify the built tree after migration",
			Value: true,
		},
	},
	Action: migrateJMT,
}

// jmtCheckpointKey is the MDBX key used to store the migration cursor position.
const jmtCheckpointKey = "jmt-migrate-cursor"

func migrateJMT(ctx *cli.Context) error {
	batchSize := ctx.Int("batch-size")
	doVerify := ctx.Bool("verify")

	stack, err := node.NewNode(ctx, &DefaultConfig)
	if err != nil {
		return fmt.Errorf("failed to open node: %w", err)
	}
	db := stack.Database()
	defer stack.Close()

	log.Info("Starting JMT migration", "batch_size", batchSize)
	start := time.Now()

	var totalAccounts, totalStorage uint64
	var jmtRoot jmt.Hash

	// Phase 1: Migrate accounts in batched transactions.
	log.Info("Phase 1/2: Migrating accounts...")
	totalAccounts, jmtRoot, err = migrateBatched(ctx, db, modules.Account, batchSize, start,
		func(k, v []byte) (*jmt.BatchEntry, error) {
			if len(k) < 20 {
				return nil, nil
			}
			var addr types.Address
			copy(addr[:], k[:20])

			acct, decErr := decodeAccount(v)
			if decErr != nil {
				log.Warn("Skipping account with decode error", "addr", addr, "err", decErr)
				return nil, nil
			}

			keyHash := commitment.AccountKeyHash(addr)
			value := commitment.EncodeAccountValue(acct)
			return &jmt.BatchEntry{KeyHash: keyHash, Value: value}, nil
		},
	)
	if err != nil {
		return fmt.Errorf("account migration failed: %w", err)
	}
	log.Info("Phase 1/2 complete", "accounts", totalAccounts, "root", fmt.Sprintf("%x", jmtRoot[:8]), "elapsed", time.Since(start))

	// Phase 2: Migrate storage in batched transactions.
	log.Info("Phase 2/2: Migrating storage...")
	totalStorage, jmtRoot, err = migrateBatched(ctx, db, modules.Storage, batchSize, start,
		func(k, v []byte) (*jmt.BatchEntry, error) {
			if len(k) < 54 {
				return nil, nil
			}
			var addr types.Address
			copy(addr[:], k[:20])
			var slot types.Hash
			copy(slot[:], k[22:54])

			keyHash := commitment.StorageKeyHash(addr, slot)
			valueCopy := make([]byte, len(v))
			copy(valueCopy, v)
			if len(valueCopy) < 32 {
				padded := make([]byte, 32)
				copy(padded[32-len(valueCopy):], valueCopy)
				valueCopy = padded
			}
			return &jmt.BatchEntry{KeyHash: keyHash, Value: valueCopy}, nil
		},
	)
	if err != nil {
		return fmt.Errorf("storage migration failed: %w", err)
	}
	log.Info("Phase 2/2 complete", "storage_slots", totalStorage, "elapsed", time.Since(start))

	// Clean up checkpoint markers.
	if err := db.Update(ctx.Context, func(tx kv.RwTx) error {
		_ = tx.Delete(jmtstore.JMTRootTable, []byte(jmtCheckpointKey+"-"+modules.Account))
		_ = tx.Delete(jmtstore.JMTRootTable, []byte(jmtCheckpointKey+"-"+modules.Storage))
		return nil
	}); err != nil {
		log.Warn("Failed to clean up checkpoint markers", "err", err)
	}

	elapsed := time.Since(start)
	log.Info("JMT migration complete",
		"accounts", totalAccounts,
		"storage_slots", totalStorage,
		"root", fmt.Sprintf("%x", jmtRoot[:]),
		"elapsed", elapsed,
	)

	// Phase 3: Optional verification.
	if doVerify {
		log.Info("Verifying JMT migration...")
		verifyStart := time.Now()
		if err := verifyJMTMigration(ctx, db, jmtRoot, totalAccounts); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}
		log.Info("Verification passed", "elapsed", time.Since(verifyStart))
	}

	fmt.Printf("\nMigration complete!\n")
	fmt.Printf("  Accounts:      %d\n", totalAccounts)
	fmt.Printf("  Storage slots: %d\n", totalStorage)
	fmt.Printf("  JMT root:      %x\n", jmtRoot)
	fmt.Printf("  Time:          %s\n", elapsed.Round(time.Second))
	fmt.Printf("\nYou can now enable jmt_commitment: true in your config.\n")

	return nil
}

// entryConverter converts a raw key-value pair from MDBX into a JMT BatchEntry.
// Returns nil to skip the entry (e.g., invalid key length).
type entryConverter func(k, v []byte) (*jmt.BatchEntry, error)

// migrateBatched scans a table and inserts entries into the JMT in batched
// transactions. Each batch opens a new MDBX transaction, processes up to
// batchSize entries, flushes dirty JMT nodes, saves a checkpoint, and commits.
// This avoids accumulating unbounded dirty pages in a single transaction.
func migrateBatched(
	ctx *cli.Context,
	db kv.RwDB,
	table string,
	batchSize int,
	start time.Time,
	convert entryConverter,
) (totalCount uint64, finalRoot jmt.Hash, retErr error) {
	checkpointKeyBytes := []byte(jmtCheckpointKey + "-" + table)

	for {
		var batchCount int
		var done bool

		if err := db.Update(ctx.Context, func(tx kv.RwTx) error {
			// Load current JMT root.
			mdbxStore := jmtstore.NewMDBXStore(tx, modules.JMTNode)
			existingRoot, _ := jmtstore.ReadJMTRoot(tx)
			var tree *jmt.Tree
			if existingRoot == jmt.EmptyHash {
				tree = jmt.New(mdbxStore)
			} else {
				tree = jmt.NewFromRoot(mdbxStore, existingRoot)
			}

			// Load cursor checkpoint for this table.
			var seekKey []byte
			if cp, err := tx.GetOne(jmtstore.JMTRootTable, checkpointKeyBytes); err == nil && len(cp) > 0 {
				seekKey = make([]byte, len(cp))
				copy(seekKey, cp)
			}

			cursor, err := tx.Cursor(table)
			if err != nil {
				return fmt.Errorf("failed to open cursor for %s: %w", table, err)
			}
			defer cursor.Close()

			// Seek to checkpoint or start from the beginning.
			var k, v []byte
			if len(seekKey) > 0 {
				k, v, err = cursor.Seek(seekKey)
				if err != nil {
					return fmt.Errorf("cursor seek error: %w", err)
				}
				// Skip the checkpoint key itself (already processed).
				if k != nil && bytes.Equal(k, seekKey) {
					k, v, err = cursor.Next()
					if err != nil {
						return fmt.Errorf("cursor next error: %w", err)
					}
				}
			} else {
				k, v, err = cursor.First()
				if err != nil {
					return fmt.Errorf("cursor first error: %w", err)
				}
			}

			batch := make([]jmt.BatchEntry, 0, batchSize)
			var lastKey []byte

			for ; k != nil; k, v, err = cursor.Next() {
				if err != nil {
					return fmt.Errorf("cursor error: %w", err)
				}

				entry, convErr := convert(k, v)
				if convErr != nil {
					return convErr
				}
				if entry == nil {
					continue
				}

				batch = append(batch, *entry)
				lastKey = make([]byte, len(k))
				copy(lastKey, k)
				batchCount++

				if batchCount >= batchSize {
					break
				}
			}

			if len(batch) == 0 {
				done = true
				finalRoot = tree.Root()
				return nil
			}

			// Apply batch and flush to MDBX.
			if _, err := tree.BatchUpdate(batch); err != nil {
				return fmt.Errorf("batch update failed: %w", err)
			}
			if err := tree.Flush(); err != nil {
				return fmt.Errorf("flush failed: %w", err)
			}

			// Save root and cursor checkpoint atomically.
			finalRoot = tree.Root()
			if err := jmtstore.WriteJMTRoot(tx, finalRoot); err != nil {
				return fmt.Errorf("failed to write JMT root: %w", err)
			}
			if lastKey != nil {
				if err := tx.Put(jmtstore.JMTRootTable, checkpointKeyBytes, lastKey); err != nil {
					return fmt.Errorf("failed to write checkpoint: %w", err)
				}
			}

			// Check if we've reached the end of the table.
			if batchCount < batchSize {
				done = true
			}

			return nil
		}); err != nil {
			return totalCount, finalRoot, err
		}

		totalCount += uint64(batchCount)
		if batchCount > 0 {
			log.Info(fmt.Sprintf("%s progress", table), "migrated", totalCount, "elapsed", time.Since(start))
		}

		if done {
			break
		}
	}

	return totalCount, finalRoot, nil
}

// verifyJMTMigration spot-checks a sample of accounts against the JMT.
func verifyJMTMigration(ctx *cli.Context, db kv.RwDB, expectedRoot jmt.Hash, totalAccounts uint64) error {
	return db.View(ctx.Context, func(tx kv.Tx) error {
		mdbxStore := &readOnlyMDBXStore{tx: tx, table: modules.JMTNode}
		tree := jmt.NewFromRoot(mdbxStore, expectedRoot)

		// Verify root is not empty.
		if tree.Root() == jmt.EmptyHash && totalAccounts > 0 {
			return fmt.Errorf("root is empty but %d accounts were migrated", totalAccounts)
		}

		// Spot-check first 100 accounts.
		accountCursor, err := tx.Cursor(modules.Account)
		if err != nil {
			return err
		}
		defer accountCursor.Close()

		checked := 0
		for k, v, err := accountCursor.First(); k != nil && checked < 100; k, v, err = accountCursor.Next() {
			if err != nil {
				return err
			}
			if len(k) < 20 {
				continue
			}

			var addr types.Address
			copy(addr[:], k[:20])
			keyHash := commitment.AccountKeyHash(addr)

			got, err := tree.Get(keyHash)
			if err != nil {
				return fmt.Errorf("account %x not found in JMT: %w", addr[:4], err)
			}

			// Verify the encoded account matches byte-for-byte.
			acct, _ := decodeAccount(v)
			if acct != nil {
				expected := commitment.EncodeAccountValue(acct)
				if !bytes.Equal(got, expected) {
					return fmt.Errorf("account %x: value mismatch (got %d bytes, want %d bytes)", addr[:4], len(got), len(expected))
				}
			}
			checked++
		}

		log.Info("Spot-checked accounts", "count", checked)
		return nil
	})
}

// readOnlyMDBXStore adapts a read-only tx as a NodeStore (for verification).
type readOnlyMDBXStore struct {
	tx    kv.Tx
	table string
}

func (s *readOnlyMDBXStore) Get(hash jmt.Hash) ([]byte, error) {
	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, jmt.ErrNotFound
	}
	return data, nil
}

func (s *readOnlyMDBXStore) Put(jmt.Hash, []byte) error { return jmt.ErrReadOnly }
func (s *readOnlyMDBXStore) Delete(jmt.Hash) error       { return jmt.ErrReadOnly }
func (s *readOnlyMDBXStore) Has(hash jmt.Hash) (bool, error) {
	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// decodeAccount decodes a V2-encoded StateAccount from storage bytes.
func decodeAccount(data []byte) (*account.StateAccount, error) {
	acct := new(account.StateAccount)
	if err := acct.DecodeForStorage(data); err != nil {
		return nil, err
	}
	return acct, nil
}

// Compile-time check.
var _ jmt.NodeStore = (*readOnlyMDBXStore)(nil)
