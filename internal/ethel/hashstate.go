// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashstate.go — HashedState rebuild and state-root computation helpers.
//
// SetupStateRootComputer attaches a commitment.TrieRootComputer to an
// IntraBlockState so that the executor can compute roots incrementally.
// CalcStateRoot rebuilds the full state trie from the HashedAccounts and
// HashedStorage tables via trie.FlatDBTrieLoader.CalcTrieRoot, clearing
// TrieOfAccounts and TrieOfStorage first so the result is reproducible.
// InitHashState populates HashedState from scratch by walking PlainState
// after a bulk import such as leaves_journal replay.

package ethel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// envBufSize reads `name` as an unsigned integer GB count; returns
// `defaultGB` when the env var is unset or unparseable.
func envBufSize(name string, defaultGB uint64) datasize.ByteSize {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			return datasize.ByteSize(n)
		}
		log.Warn("ignoring invalid env var", "name", name, "value", v, "default_GB", defaultGB)
	}
	return datasize.ByteSize(defaultGB)
}

// SetupStateRootComputer creates and attaches a TrieRootComputer to the
// IntraBlockState so that state root is computed incrementally.
func SetupStateRootComputer(tx kv.RwTx, ibs *state.IntraBlockState) *commitment.TrieRootComputer {
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	ibs.SetRootComputer(trc)
	return trc
}

// CalcStateRoot runs CalcTrieRoot on already-populated HashedAccounts/Storage.
// Assumes HashedAccounts/Storage are up-to-date (via HashOnlyComputer or InitHashState).
func CalcStateRoot(tx kv.RwTx) (types.Hash, error) {
	// Clear trie tables — CalcTrieRoot outputs fresh trie.
	if err := tx.ClearBucket(kv.TrieOfAccounts); err != nil {
		return types.Hash{}, err
	}
	if err := tx.ClearBucket(kv.TrieOfStorage); err != nil {
		return types.Hash{}, err
	}

	loader := trie.NewFlatDBTrieLoader("ethel-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	return root, nil
}

// RebuildHashedState clears and rebuilds HashedAccounts/HashedStorage from
// plain Account/Storage tables. Call before TrieRootComputer on verify blocks
// to eliminate any drift from HashOnlyComputer incremental updates.
func RebuildHashedState(tx kv.RwTx) error {
	for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return fmt.Errorf("clear %s: %w", tbl, err)
		}
	}
	if err := hashAllAccounts(tx); err != nil {
		return err
	}
	return hashAllStorage(tx)
}

// RebuildHashedStateETL is the etl.Collector-based variant of
// RebuildHashedState. Instead of doing 240M+ random-key tx.Put calls
// (which thrashes the MDBX B-tree and balloons dirty-page memory), it
// streams hashed entries through an etl.Collector that sorts on tmpfile
// and bulk-loads in sorted order — orders of magnitude fewer page
// touches. Expected speedup at 17M+ block scale: 5-10×.
//
// tmpdir holds sort-merge files; size at full 17M state ≈ 17 GB. Pass
// "" to use os.MkdirTemp (system TEMP, may fail on small C:\ — prefer
// an explicit path on the same drive as the MDBX datadir).
//
// SortableBuffer is sized at 1 GB per Collector, ~2 GB combined peak
// in-memory; remaining entries spill to tmpfile.
//
// MDBX requirement: caller's RwTx must have DirtySpace ≥ 32 GB to hold
// the 240M-entry sequential Load. Set --dirty-space-gb 32 (or more) on
// the parent command. Default 2 GB will overflow with MDBX_MAP_FULL.
func RebuildHashedStateETL(ctx context.Context, tx kv.RwTx, tmpdir string, logger log2.Logger) error {
	if tmpdir == "" {
		// ETL spill files for full-state hashing reach ~17 GB on a 14M-block
		// chain; system %TEMP% on Windows is C:\ which routinely lacks space.
		// Honor N42_ETL_TMPDIR before falling through to os.MkdirTemp so users
		// can redirect to a roomier drive without wiring a flag through every
		// caller (FullStateRootVerify, journal_verify, etc.).
		base := os.Getenv("N42_ETL_TMPDIR")
		if base != "" {
			if err := os.MkdirAll(base, 0755); err != nil {
				return fmt.Errorf("create N42_ETL_TMPDIR=%s: %w", base, err)
			}
		}
		dir, err := os.MkdirTemp(base, "hashed-state-etl-*")
		if err != nil {
			return fmt.Errorf("mkdir tmp: %w", err)
		}
		defer os.RemoveAll(dir)
		tmpdir = dir
	}
	for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return fmt.Errorf("clear %s: %w", tbl, err)
		}
	}

	t0 := time.Now()
	emptyCodeHash := crypto.Keccak256Hash(nil)

	// SortableBuffer sizes — sized to hold the hashed working set in
	// memory (Account ~2 GB, Storage ~17 GB at 14M blocks; ~29 GB at
	// 24M). Larger = fewer tmpfile spills; too large risks OOM on hosts
	// running ethexec alongside other heavy processes. Override via env
	// when memory is tight: N42_ETL_BUFFER_ACCT_GB / N42_ETL_BUFFER_STO_GB.
	accBufSize := envBufSize("N42_ETL_BUFFER_ACCT_GB", 4) * datasize.GB
	stoBufSize := envBufSize("N42_ETL_BUFFER_STO_GB", 32) * datasize.GB
	log.Info("RebuildHashedStateETL: buffer sizes",
		"acct_GB", uint64(accBufSize/datasize.GB),
		"sto_GB", uint64(stoBufSize/datasize.GB))

	// Account: walk Plain "Account" → keccak(addr), Collect into etl.
	accColl := etl.NewCollector("rebuild-hashed-acct", tmpdir,
		etl.NewSortableBuffer(accBufSize), logger)
	defer accColl.Close()

	accCur, err := tx.Cursor("Account")
	if err != nil {
		return fmt.Errorf("open Account cursor: %w", err)
	}
	var accCount int
	tProgress := time.Now()
	for k, v, err := accCur.First(); k != nil; k, v, err = accCur.Next() {
		if err != nil {
			accCur.Close()
			return err
		}
		if len(k) != 20 {
			continue
		}
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			accCur.Close()
			return fmt.Errorf("decode account: %w", err)
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}
		hashedKey := crypto.Keccak256(k)
		if err := accColl.Collect(hashedKey, acc.MarshalV2()); err != nil {
			accCur.Close()
			return fmt.Errorf("collect acct: %w", err)
		}
		accCount++
		if time.Since(tProgress) > 10*time.Second {
			log.Info("RebuildHashedStateETL: hashing accounts",
				"done", accCount,
				"rate/s", uint64(float64(accCount)/time.Since(t0).Seconds()),
				"elapsed", time.Since(t0).Truncate(time.Second))
			tProgress = time.Now()
		}
	}
	accCur.Close()
	tCollectAcct := time.Since(t0)
	log.Info("RebuildHashedStateETL: accounts collected",
		"count", accCount, "elapsed", tCollectAcct.Truncate(time.Millisecond))

	// Storage: walk Plain "Storage" → keccak(addr)+inc0+keccak(slot).
	tSto := time.Now()
	stoColl := etl.NewCollector("rebuild-hashed-sto", tmpdir,
		etl.NewSortableBuffer(stoBufSize), logger)
	defer stoColl.Close()

	stoCur, err := tx.Cursor("Storage")
	if err != nil {
		return fmt.Errorf("open Storage cursor: %w", err)
	}
	var stoCount int
	tProgress = time.Now()
	for k, v, err := stoCur.First(); k != nil; k, v, err = stoCur.Next() {
		if err != nil {
			stoCur.Close()
			return err
		}
		if len(k) < 52 {
			continue
		}
		var compositeKey [72]byte
		copy(compositeKey[:32], crypto.Keccak256(k[:20]))
		// 8-byte zero incarnation gap (compositeKey[32:40] stays zero)
		copy(compositeKey[40:72], crypto.Keccak256(k[20:52]))
		if err := stoColl.Collect(compositeKey[:], v); err != nil {
			stoCur.Close()
			return fmt.Errorf("collect sto: %w", err)
		}
		stoCount++
		if time.Since(tProgress) > 10*time.Second {
			log.Info("RebuildHashedStateETL: hashing storage",
				"done", stoCount,
				"rate/s", uint64(float64(stoCount)/time.Since(tSto).Seconds()),
				"elapsed", time.Since(tSto).Truncate(time.Second))
			tProgress = time.Now()
		}
	}
	stoCur.Close()
	tCollectSto := time.Since(tSto)
	log.Info("RebuildHashedStateETL: storage collected",
		"count", stoCount, "elapsed", tCollectSto.Truncate(time.Millisecond))

	// Bulk-load both collectors. etl.Load streams sorted entries to
	// MDBX in sequential B-tree order — page touches are sequential
	// vs random for the legacy tx.Put flow.
	tLoad := time.Now()
	log.Info("RebuildHashedStateETL: loading HashedAccounts to MDBX...")
	if err := accColl.Load(tx, kv.HashedAccounts, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		return fmt.Errorf("load HashedAccounts: %w", err)
	}
	tLoadAcct := time.Since(tLoad)
	log.Info("RebuildHashedStateETL: HashedAccounts loaded",
		"elapsed", tLoadAcct.Truncate(time.Millisecond))

	tLoad = time.Now()
	log.Info("RebuildHashedStateETL: loading HashedStorage to MDBX...")
	if err := stoColl.Load(tx, kv.HashedStorage, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		return fmt.Errorf("load HashedStorage: %w", err)
	}
	tLoadSto := time.Since(tLoad)
	log.Info("RebuildHashedStateETL: HashedStorage loaded",
		"elapsed", tLoadSto.Truncate(time.Millisecond))

	log.Info("RebuildHashedStateETL done",
		"accts", accCount,
		"sto", stoCount,
		"collectAcct", tCollectAcct.Truncate(time.Millisecond),
		"collectSto", tCollectSto.Truncate(time.Millisecond),
		"loadAcct", tLoadAcct.Truncate(time.Millisecond),
		"loadSto", tLoadSto.Truncate(time.Millisecond),
		"total", time.Since(t0).Truncate(time.Millisecond))
	return nil
}

// FullStateRootVerify does a complete re-hash and MPT root computation.
// It clears HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage,
// re-hashes everything from Account/Storage, then runs CalcTrieRoot.
// This is expensive but guaranteed correct for verification.
//
// Uses RebuildHashedStateETL (etl.Collector + sorted bulk-load) for the
// re-hash phase — orders of magnitude faster than the legacy random-Put
// path on 10M+ block states.
func FullStateRootVerify(tx kv.RwTx) (types.Hash, error) {
	// Clear trie tables here; HashedAccounts/HashedStorage are cleared
	// inside RebuildHashedStateETL.
	for _, tbl := range []string{kv.TrieOfAccounts, kv.TrieOfStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return types.Hash{}, fmt.Errorf("clear %s: %w", tbl, err)
		}
	}

	if err := RebuildHashedStateETL(context.Background(), tx, "", log2.New()); err != nil {
		return types.Hash{}, fmt.Errorf("rebuild hashed state: %w", err)
	}

	// CalcTrieRoot with empty RetainList (full rebuild).
	loader := trie.NewFlatDBTrieLoader("ethel-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	return root, nil
}

// BootstrapHPH populates all four HPH tables (HashedAccounts, HashedStorage,
// TrieOfAccounts, TrieOfStorage) from plain state. Unlike FullStateRootVerify
// which discards intermediate nodes after computing the root, this variant
// PERSISTS TrieOf* so that subsequent per-block calls can skip unchanged
// subtrees and run in O(dirty).
//
// Cost: one full MPT rebuild — ~5-10 minutes at the 10M-block scale.
// Return value: the computed state root (same as FullStateRootVerify would).
//
// Call this after rebuild-state reaches a checkpoint to create a fully
// bootstrapped HPH datadir usable by TrieRootComputer in incremental mode
// (e.g. ethexec verify-incremental).
func BootstrapHPH(tx kv.RwTx) (types.Hash, error) {
	// Clear hashed/trie tables so result is reproducible.
	for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage, kv.TrieOfAccounts, kv.TrieOfStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return types.Hash{}, fmt.Errorf("clear %s: %w", tbl, err)
		}
	}

	// Re-hash all accounts / storage from PlainState.
	if err := hashAllAccounts(tx); err != nil {
		return types.Hash{}, err
	}
	if err := hashAllStorage(tx); err != nil {
		return types.Hash{}, err
	}

	// Collect intermediate hashes and write them to TrieOf* at the end
	// (avoid cursor read/write conflict during CalcTrieRoot).
	type kvPair struct{ k, v []byte }
	var accTrie []kvPair
	var storTrie []kvPair

	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 {
			return nil
		}
		k := append([]byte{}, keyHex...)
		if hasState == 0 {
			accTrie = append(accTrie, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accTrie = append(accTrie, kvPair{k, append([]byte{}, v...)})
		return nil
	}
	storCollector := func(accWithInc []byte, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			storTrie = append(storTrie, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		storTrie = append(storTrie, kvPair{k, append([]byte{}, v...)})
		return nil
	}

	loader := trie.NewFlatDBTrieLoader("bootstrap-hph", trie.NewRetainList(0), accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	// Flush collected intermediate nodes to TrieOf*.
	for _, kv := range accTrie {
		if kv.v == nil {
			_ = tx.Delete("TrieAccount", kv.k)
		} else {
			if err := tx.Put("TrieAccount", kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	for _, kv := range storTrie {
		if kv.v == nil {
			_ = tx.Delete("TrieStorage", kv.k)
		} else {
			if err := tx.Put("TrieStorage", kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	return root, nil
}

// BootstrapHPHBatched is a scalable variant of BootstrapHPH that commits
// the hashed-table fill in multiple MDBX transactions to avoid
// MDBX_MAP_FULL at 10M+ block scale.
//
// Phases (each its own RwTx):
//   1. Clear HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage
//   2. hashAllAccountsBatched — commit every accountBatchN entries
//   3. hashAllStorageBatched — commit every storageBatchN entries
//      (HashedStorage is DupSort, which inflates dirty-page cost; keep this
//       batch size modest — a few million per commit)
//   4. CalcTrieRoot + flush TrieOf* intermediate nodes (single RwTx; the
//      intermediate-node set is small relative to plain state, ~1-3 GB
//      dirty at 12.5M, well within any reasonable DirtySpace).
//
// Pass 0 for batch sizes to use defaults (2M accounts, 3M storage).
func BootstrapHPHBatched(ctx context.Context, db kv.RwDB, accountBatchN, storageBatchN int) (types.Hash, error) {
	if accountBatchN <= 0 {
		accountBatchN = 2_000_000
	}
	if storageBatchN <= 0 {
		storageBatchN = 3_000_000
	}

	// Phase 1: clear hashed/trie tables (fast, own tx).
	{
		log.Info("BootstrapHPH: clearing HashedAccounts/HashedStorage/TrieOf*")
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return types.Hash{}, err
		}
		for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage, kv.TrieOfAccounts, kv.TrieOfStorage} {
			if err := tx.ClearBucket(tbl); err != nil {
				tx.Rollback()
				return types.Hash{}, fmt.Errorf("clear %s: %w", tbl, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return types.Hash{}, fmt.Errorf("commit clear: %w", err)
		}
	}

	// Phase 2: hash all accounts, batched.
	accTotal, err := hashAllAccountsBatched(ctx, db, accountBatchN)
	if err != nil {
		return types.Hash{}, fmt.Errorf("hashAllAccountsBatched: %w", err)
	}

	// Phase 3: hash all storage, batched.
	stoTotal, err := hashAllStorageBatched(ctx, db, storageBatchN)
	if err != nil {
		return types.Hash{}, fmt.Errorf("hashAllStorageBatched: %w", err)
	}
	log.Info("BootstrapHPH: hashing complete", "accounts", accTotal, "storage", stoTotal)

	// Phase 4: CalcTrieRoot + flush intermediate nodes (single RwTx).
	log.Info("BootstrapHPH: computing trie root via FlatDBTrieLoader")
	tx, err := db.BeginRw(ctx)
	if err != nil {
		return types.Hash{}, err
	}
	defer tx.Rollback()

	type kvPair struct{ k, v []byte }
	var accTrie []kvPair
	var storTrie []kvPair

	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 {
			return nil
		}
		k := append([]byte{}, keyHex...)
		if hasState == 0 {
			accTrie = append(accTrie, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accTrie = append(accTrie, kvPair{k, append([]byte{}, v...)})
		return nil
	}
	storCollector := func(accWithInc []byte, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			storTrie = append(storTrie, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		storTrie = append(storTrie, kvPair{k, append([]byte{}, v...)})
		return nil
	}

	loader := trie.NewFlatDBTrieLoader("bootstrap-hph", trie.NewRetainList(0), accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	log.Info("BootstrapHPH: flushing intermediate trie nodes",
		"accTrie", len(accTrie), "storTrie", len(storTrie))
	for _, e := range accTrie {
		if e.v == nil {
			_ = tx.Delete("TrieAccount", e.k)
		} else {
			if err := tx.Put("TrieAccount", e.k, e.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	for _, e := range storTrie {
		if e.v == nil {
			_ = tx.Delete("TrieStorage", e.k)
		} else {
			if err := tx.Put("TrieStorage", e.k, e.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return types.Hash{}, fmt.Errorf("commit trie flush: %w", err)
	}
	return root, nil
}

// hashAllAccountsBatched scans the Account table and writes keccak-hashed
// entries to HashedAccounts, committing every batchN puts so the MDBX
// dirty-page pool never overflows. Returns the total number of hashed
// entries written.
func hashAllAccountsBatched(ctx context.Context, db kv.RwDB, batchN int) (int, error) {
	emptyCodeHash := crypto.Keccak256Hash(nil)
	var lastKey []byte
	total := 0
	t0 := time.Now()
	for {
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return total, err
		}
		cursor, err := tx.Cursor("Account")
		if err != nil {
			tx.Rollback()
			return total, err
		}
		var k, v []byte
		if lastKey == nil {
			k, v, err = cursor.First()
		} else {
			// Seek to lastKey, advance past it.
			k, v, err = cursor.Seek(lastKey)
			if err == nil && k != nil && bytes.Equal(k, lastKey) {
				k, v, err = cursor.Next()
			}
		}
		if err != nil {
			cursor.Close()
			tx.Rollback()
			return total, err
		}

		count := 0
		for ; k != nil && count < batchN; k, v, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				tx.Rollback()
				return total, err
			}
			if len(k) != 20 {
				continue
			}
			hashedKey := crypto.Keccak256(k)
			var acc account.StateAccount
			if err := acc.DecodeForStorage(v); err != nil {
				cursor.Close()
				tx.Rollback()
				return total, err
			}
			if acc.CodeHash == (types.Hash{}) {
				acc.CodeHash = emptyCodeHash
			}
			if err := tx.Put(kv.HashedAccounts, hashedKey, acc.MarshalV2()); err != nil {
				cursor.Close()
				tx.Rollback()
				return total, err
			}
			lastKey = append(lastKey[:0], k...)
			count++
		}
		cursor.Close()
		if err := tx.Commit(); err != nil {
			return total, fmt.Errorf("hashAllAccountsBatched commit: %w", err)
		}
		total += count
		log.Info("hashAllAccountsBatched progress",
			"accounts", total, "batch_size", count, "elapsed", time.Since(t0).Truncate(time.Second))
		if count < batchN {
			break
		}
	}
	return total, nil
}

// hashAllStorageBatched scans the Storage table and writes
// keccak(addr)||inc||keccak(slot) → value to HashedStorage, committing
// every batchN puts.
func hashAllStorageBatched(ctx context.Context, db kv.RwDB, batchN int) (int, error) {
	var lastKey []byte
	total := 0
	t0 := time.Now()
	for {
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return total, err
		}
		cursor, err := tx.Cursor("Storage")
		if err != nil {
			tx.Rollback()
			return total, err
		}
		var k, v []byte
		if lastKey == nil {
			k, v, err = cursor.First()
		} else {
			k, v, err = cursor.Seek(lastKey)
			if err == nil && k != nil && bytes.Equal(k, lastKey) {
				k, v, err = cursor.Next()
			}
		}
		if err != nil {
			cursor.Close()
			tx.Rollback()
			return total, err
		}

		count := 0
		for ; k != nil && count < batchN; k, v, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				tx.Rollback()
				return total, err
			}
			if len(k) < 52 {
				continue
			}
			// Plain: addr(20)+slot(32)=52B → Hashed: addrHash(32)+inc0(8)+slotHash(32)=72B
			var compositeKey [72]byte
			copy(compositeKey[:32], crypto.Keccak256(k[:20]))
			copy(compositeKey[40:72], crypto.Keccak256(k[20:52]))
			if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
				cursor.Close()
				tx.Rollback()
				return total, err
			}
			lastKey = append(lastKey[:0], k...)
			count++
		}
		cursor.Close()
		if err := tx.Commit(); err != nil {
			return total, fmt.Errorf("hashAllStorageBatched commit: %w", err)
		}
		total += count
		log.Info("hashAllStorageBatched progress",
			"storage", total, "batch_size", count, "elapsed", time.Since(t0).Truncate(time.Second))
		if count < batchN {
			break
		}
	}
	return total, nil
}

// IsHPHBootstrapped reports whether a datadir has TrieOfAccounts populated
// and so can be used by TrieRootComputer in incremental mode.
func IsHPHBootstrapped(tx kv.Tx) (bool, error) {
	c, err := tx.Cursor("TrieAccount")
	if err != nil {
		return false, err
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return false, err
	}
	return k != nil, nil
}

func hashAllAccounts(tx kv.RwTx) error {
	cursor, err := tx.Cursor("Account")
	if err != nil {
		return err
	}
	defer cursor.Close()

	emptyCodeHash := crypto.Keccak256Hash(nil)

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		hashedKey := crypto.Keccak256(k)

		// CalcTrieRoot's IsEmptyCodeHash() checks for keccak256(empty),
		// not zero hash. Normalize zero → emptyCodeHash so EOAs are
		// correctly recognized as "no code".
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return err
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}

		if err := tx.Put(kv.HashedAccounts, hashedKey, acc.MarshalV2()); err != nil {
			return err
		}
	}
	return nil
}

func hashAllStorage(tx kv.RwTx) error {
	cursor, err := tx.Cursor("Storage")
	if err != nil {
		return err
	}
	defer cursor.Close()

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		if len(k) < 52 {
			continue
		}
		// Plain: addr(20)+slot(32)=52B → Hashed: addrHash(32)+inc0(8)+slotHash(32)=72B
		var compositeKey [72]byte
		copy(compositeKey[:32], crypto.Keccak256(k[:20]))
		copy(compositeKey[40:72], crypto.Keccak256(k[20:52]))
		if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
			return err
		}
	}
	return nil
}

// InitHashState does a full conversion of Account/Storage tables into
// HashedAccounts/HashedStorage. This must be done once before the first
// incremental state root verification.
func InitHashState(tx kv.RwTx) error {
	// Check if HashedAccounts already has data.
	c, err := tx.Cursor(kv.HashedAccounts)
	if err != nil {
		return err
	}
	k, _, err := c.First()
	c.Close()
	if err != nil {
		return err
	}
	if k != nil {
		// Already populated.
		return nil
	}

	log.Info("Initializing HashedAccounts/HashedStorage from PlainState (one-time)...")

	// Hash all accounts.
	accCursor, err := tx.Cursor("Account")
	if err != nil {
		return fmt.Errorf("open Account: %w", err)
	}
	defer accCursor.Close()

	emptyCodeHash := crypto.Keccak256Hash(nil)
	accCount := 0
	for k, v, err := accCursor.First(); k != nil; k, v, err = accCursor.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		hashedKey := crypto.Keccak256(k)

		// Normalize zero CodeHash → emptyCodeHash for CalcTrieRoot.
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return fmt.Errorf("decode account: %w", err)
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}
		if err := tx.Put(kv.HashedAccounts, hashedKey, acc.MarshalV2()); err != nil {
			return fmt.Errorf("put HashedAccounts: %w", err)
		}
		accCount++
	}

	// Hash all storage.
	stoCursor, err := tx.Cursor("Storage")
	if err != nil {
		return fmt.Errorf("open Storage: %w", err)
	}
	defer stoCursor.Close()

	stoCount := 0
	for k, v, err := stoCursor.First(); k != nil; k, v, err = stoCursor.Next() {
		if err != nil {
			return err
		}
		if len(k) < 52 {
			continue
		}
		var compositeKey [72]byte
		copy(compositeKey[:32], crypto.Keccak256(k[:20]))
		copy(compositeKey[40:72], crypto.Keccak256(k[20:52]))
		if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
			return fmt.Errorf("put HashedStorage: %w", err)
		}
		stoCount++
	}

	log.Info("HashState initialization complete", "accounts", accCount, "storage", stoCount)
	return nil
}
