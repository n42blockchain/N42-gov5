// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// rebuild_state.go rebuilds PlainState from leaves journal files.
// Reads all leaves into memory, sorts, then writes to MDBX with Append.

package ethel

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// RebuildState reads leaves from a freezer table, accumulates in memory,
// then writes sorted PlainState to MDBX via Append.
// Genesis state is included in leaves block 0 — no genesis.json needed.
func RebuildState(ctx context.Context, db kv.RwDB, outFreezer *freezer.Freezer,
	inputFreezer *freezer.Freezer, endBlock uint64,
) error {
	t0 := time.Now()

	// Read all leaves into memory maps (block 0 = genesis).
	journalTbl := outFreezer.Table("leaves_journal")
	if journalTbl == nil {
		return fmt.Errorf("leaves_journal table not found")
	}
	items := journalTbl.Items()
	if items == 0 {
		return fmt.Errorf("leaves_journal is empty")
	}
	if endBlock == 0 || endBlock > items {
		endBlock = items
	}
	log.Info("Reading leaves journal", "blocks", endBlock, "items", items)

	// Account: addr(20) → V2-encoded value
	acctMap := make(map[types.Address][]byte, 500_000)
	// Storage: addr(20)+slot(32)=52B → raw value
	storMap := make(map[string][]byte, 5_000_000)

	lastLogTime := time.Now()
	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		data, err := journalTbl.Retrieve(blockNum)
		if err != nil {
			return fmt.Errorf("read block %d: %w", blockNum, err)
		}
		if len(data) < 8 {
			continue // empty block
		}

		accounts, storage, err := DecodeLeavesJournal(data)
		if err != nil {
			return fmt.Errorf("decode block %d: %w", blockNum, err)
		}

		for _, a := range accounts {
			if len(a.Value) == 0 {
				delete(acctMap, a.Address)
			} else {
				acctMap[a.Address] = a.Value
			}
		}
		for _, s := range storage {
			var key [52]byte
			copy(key[:20], s.Address[:])
			copy(key[20:], s.Slot[:])
			k := string(key[:])
			if len(s.Value) == 0 {
				delete(storMap, k)
			} else {
				storMap[k] = s.Value
			}
		}

		if time.Since(lastLogTime) > 5*time.Second {
			elapsed := time.Since(t0)
			pct := float64(blockNum) / float64(endBlock) * 100
			log.Info("Reading leaves",
				"block", blockNum,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"accounts", len(acctMap),
				"storage", len(storMap),
				"elapsed", elapsed.Truncate(time.Second))
			lastLogTime = time.Now()
		}
	}

	readElapsed := time.Since(t0)
	log.Info("Leaves loaded into memory",
		"blocks", endBlock,
		"accounts", len(acctMap),
		"storage", len(storMap),
		"elapsed", readElapsed.Truncate(time.Second))

	// 3. Sort keys for Append.
	log.Info("Sorting account keys...")
	acctKeys := make([]types.Address, 0, len(acctMap))
	for addr := range acctMap {
		acctKeys = append(acctKeys, addr)
	}
	sort.Slice(acctKeys, func(i, j int) bool {
		return bytes.Compare(acctKeys[i][:], acctKeys[j][:]) < 0
	})

	log.Info("Sorting storage keys...")
	storKeys := make([]string, 0, len(storMap))
	for k := range storMap {
		storKeys = append(storKeys, k)
	}
	sort.Strings(storKeys)

	sortElapsed := time.Since(t0) - readElapsed
	log.Info("Sort complete",
		"accounts", len(acctKeys),
		"storage", len(storKeys),
		"sortTime", sortElapsed.Truncate(time.Second))

	// 4. Write to MDBX with Append (sequential, fastest).
	log.Info("Writing PlainState to MDBX...")
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}

	for i, addr := range acctKeys {
		if err := tx.Append(modules.Account, addr[:], acctMap[addr]); err != nil {
			// Append requires strict ordering; fall back to Put.
			if err := tx.Put(modules.Account, addr[:], acctMap[addr]); err != nil {
				tx.Rollback()
				return fmt.Errorf("write account %d: %w", i, err)
			}
		}
		if i > 0 && i%500_000 == 0 {
			log.Info("  accounts written", "count", i)
		}
	}

	for i, k := range storKeys {
		v := storMap[k]
		if err := tx.Append(modules.Storage, []byte(k), v); err != nil {
			if err := tx.Put(modules.Storage, []byte(k), v); err != nil {
				tx.Rollback()
				return fmt.Errorf("write storage %d: %w", i, err)
			}
		}
		if i > 0 && i%2_000_000 == 0 {
			log.Info("  storage written", "count", i)
		}
	}

	// Write progress marker.
	var progBuf [8]byte
	binary.BigEndian.PutUint64(progBuf[:], endBlock-1)
	if err := tx.Put("DbInfo", []byte("ethel_progress"), progBuf[:]); err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	writeElapsed := time.Since(t0) - readElapsed - sortElapsed
	log.Info("PlainState written",
		"accounts", len(acctKeys),
		"storage", len(storKeys),
		"writeTime", writeElapsed.Truncate(time.Second))

	// 5. Verify state root against header.
	log.Info("Computing state root for verification...")
	headerData, err := inputFreezer.Ancient(freezer.TableHeaders, endBlock-1)
	if err != nil {
		log.Warn("Cannot read header for verification", "block", endBlock-1, "err", err)
		return nil
	}
	header, err := DecodeGethHeader(headerData)
	if err != nil {
		log.Warn("Cannot decode header", "err", err)
		return nil
	}

	tx2, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	// Init hash state for root computation.
	if err := InitHashState(tx2); err != nil {
		tx2.Rollback()
		log.Warn("InitHashState failed", "err", err)
		return nil
	}
	if err := tx2.Commit(); err != nil {
		return err
	}

	tx3, err := db.BeginRw(ctx)
	if err != nil {
		return err
	}
	computedRoot, err := CalcStateRoot(tx3)
	tx3.Rollback()
	if err != nil {
		log.Warn("CalcStateRoot failed", "err", err)
	} else if computedRoot == header.Root {
		log.Info("State root VERIFIED",
			"block", endBlock-1,
			"root", header.Root.Hex(),
			"elapsed", time.Since(t0).Truncate(time.Second))
	} else {
		log.Error("State root MISMATCH",
			"block", endBlock-1,
			"computed", computedRoot.Hex(),
			"expected", header.Root.Hex())
	}

	return nil
}

// ReadHeaderRoot reads a header's state root from the input freezer.
func ReadHeaderRoot(inputFreezer *freezer.Freezer, blockNum uint64) (types.Hash, error) {
	data, err := inputFreezer.Ancient(freezer.TableHeaders, blockNum)
	if err != nil {
		return types.Hash{}, err
	}
	header, err := DecodeGethHeader(data)
	if err != nil {
		return types.Hash{}, err
	}
	return header.Root, nil
}

