// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// compare_mdbx.go — byte-exact comparison of Account/Storage tables
// between two MDBX databases. Used to detect changeset-vs-EVM divergence
// without the cost of computing two MPT state roots.

package ethel

import (
	"bytes"
	"fmt"

	"context"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// CompareMDBXResult holds the outcome of a table comparison.
type CompareMDBXResult struct {
	Table      string
	OK         bool
	Mismatches int
	OnlyInA    int // present in primary but not shadow
	OnlyInB    int // present in shadow but not primary
	FirstDiff  string
}

// CompareMDBXTables compares Account and Storage tables between two read
// transactions. Returns on first table with a mismatch, logging up to
// maxDiffs sample differences for debugging.
func CompareMDBXTables(txA, txB kv.Tx, maxDiffs int) ([]CompareMDBXResult, error) {
	tables := []string{modules.Account, modules.Storage}
	var results []CompareMDBXResult

	for _, table := range tables {
		res, err := compareTable(txA, txB, table, maxDiffs)
		if err != nil {
			return results, fmt.Errorf("compare %s: %w", table, err)
		}
		results = append(results, res)
		if res.OK {
			log.Info("Table MATCH", "table", table)
		} else {
			log.Error("Table MISMATCH",
				"table", table,
				"mismatches", res.Mismatches,
				"onlyInPrimary", res.OnlyInA,
				"onlyInShadow", res.OnlyInB,
				"firstDiff", res.FirstDiff)
		}
	}
	return results, nil
}

func compareTable(txA, txB kv.Tx, table string, maxDiffs int) (CompareMDBXResult, error) {
	res := CompareMDBXResult{Table: table, OK: true}

	cA, err := txA.Cursor(table)
	if err != nil {
		return res, err
	}
	defer cA.Close()

	cB, err := txB.Cursor(table)
	if err != nil {
		return res, err
	}
	defer cB.Close()

	kA, vA, errA := cA.First()
	kB, vB, errB := cB.First()

	for {
		if errA != nil {
			return res, errA
		}
		if errB != nil {
			return res, errB
		}

		if kA == nil && kB == nil {
			break
		}

		cmp := 0
		if kA == nil {
			cmp = 1 // A exhausted, B has more
		} else if kB == nil {
			cmp = -1 // B exhausted, A has more
		} else {
			cmp = bytes.Compare(kA, kB)
		}

		if cmp == 0 {
			// Same key — compare values.
			if !bytes.Equal(vA, vB) {
				res.OK = false
				res.Mismatches++
				if res.Mismatches <= maxDiffs {
					msg := fmt.Sprintf("key=%x valA=%x valB=%x", kA, truncHex(vA, 32), truncHex(vB, 32))
					if res.FirstDiff == "" {
						res.FirstDiff = msg
					}
					log.Error("  DIFF", "table", table, "detail", msg)
				}
			}
			kA, vA, errA = cA.Next()
			kB, vB, errB = cB.Next()
		} else if cmp < 0 {
			// kA < kB — key only in A.
			res.OK = false
			res.OnlyInA++
			if res.OnlyInA+res.Mismatches <= maxDiffs {
				msg := fmt.Sprintf("key=%x (only in primary)", kA)
				if res.FirstDiff == "" {
					res.FirstDiff = msg
				}
				log.Error("  ONLY-PRIMARY", "table", table, "key", fmt.Sprintf("%x", kA))
			}
			kA, vA, errA = cA.Next()
		} else {
			// kA > kB — key only in B.
			res.OK = false
			res.OnlyInB++
			if res.OnlyInB+res.Mismatches <= maxDiffs {
				msg := fmt.Sprintf("key=%x (only in shadow)", kB)
				if res.FirstDiff == "" {
					res.FirstDiff = msg
				}
				log.Error("  ONLY-SHADOW", "table", table, "key", fmt.Sprintf("%x", kB))
			}
			kB, vB, errB = cB.Next()
		}
	}

	return res, nil
}

func truncHex(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	return b[:max]
}

// ApplyChangesetsToMDBX reads changesets from acctcs/storcs freezer tables
// for blocks [from, to) and applies the NEW values to the target MDBX.
func ApplyChangesetsToMDBX(ctx context.Context, db kv.RwDB, leavesDir string, from, to uint64) error {
	acctTbl, err := freezer.NewFreezerTableReadOnly(leavesDir, "acctcs", "c")
	if err != nil {
		return fmt.Errorf("open acctcs: %w", err)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)

	stoTbl, err := freezer.NewFreezerTableReadOnly(leavesDir, "storcs", "c")
	if err != nil {
		return fmt.Errorf("open storcs: %w", err)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}

	for blockNum := from; blockNum < to; blockNum++ {
		// Account changes.
		accData, err := acctTbl.Retrieve(blockNum)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("read acctcs block %d: %w", blockNum, err)
		}
		if len(accData) > 0 {
			entries, err := DecodeAccountChanges(accData)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("decode acctcs block %d: %w", blockNum, err)
			}
			for _, e := range entries {
				if len(e.NewValue) == 0 {
					tx.Delete(modules.Account, e.Address[:])
				} else {
					if err := tx.Put(modules.Account, e.Address[:], e.NewValue); err != nil {
						tx.Rollback()
						return err
					}
				}
			}
		}

		// Storage changes.
		stoData, err := stoTbl.Retrieve(blockNum)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("read storcs block %d: %w", blockNum, err)
		}
		if len(stoData) > 0 {
			entries, err := DecodeStorageChanges(stoData)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("decode storcs block %d: %w", blockNum, err)
			}
			for _, e := range entries {
				if len(e.NewValue) == 0 {
					tx.Delete(modules.Storage, e.CompositeKey)
				} else {
					if err := tx.Put(modules.Storage, e.CompositeKey, e.NewValue); err != nil {
						tx.Rollback()
						return err
					}
				}
			}
		}

		// Commit every 100K blocks to avoid oversized txs.
		if blockNum > from && (blockNum-from)%100_000 == 0 {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit at %d: %w", blockNum, err)
			}
			tx, err = db.BeginRw(ctx)
			if err != nil {
				return err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("final commit: %w", err)
	}
	return nil
}
