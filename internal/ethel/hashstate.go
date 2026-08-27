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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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
	// 12 GB default (was 32) — 32 GB grew to ~64 GB allocated, OOM'd 128 GB hosts.
	stoBufSize := envBufSize("N42_ETL_BUFFER_STO_GB", 12) * datasize.GB
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
	// #1: memoize keccak(addr). PlainState "Storage" is sorted by addr(20)+slot,
	// so consecutive rows share the same 20-byte address. keccak(addr) was being
	// recomputed once per slot — for an account with N slots that's N identical
	// hashes. Cache the last addr's hash and reuse it: per-row keccaks drop from
	// 2 → ~1 (addr hash amortizes to one per account), ~2× on the storage phase.
	var prevAddr [20]byte
	var prevHashedAddr [32]byte
	var hasPrev bool
	for k, v, err := stoCur.First(); k != nil; k, v, err = stoCur.Next() {
		if err != nil {
			stoCur.Close()
			return err
		}
		if len(k) < 52 {
			continue
		}
		var compositeKey [64]byte
		if hasPrev && bytes.Equal(k[:20], prevAddr[:]) {
			copy(compositeKey[:32], prevHashedAddr[:])
		} else {
			copy(compositeKey[:32], crypto.Keccak256(k[:20]))
			copy(prevAddr[:], k[:20])
			copy(prevHashedAddr[:], compositeKey[:32])
			hasPrev = true
		}
		copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
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

// RebuildHashedStateETLParallel is the parallel variant of RebuildHashedStateETL.
// It shards the PlainState Account/Storage keyspace by the first address byte
// into `workers` ranges, scans each range CONCURRENTLY on its own RoTx, keccaks
// into a per-worker etl.Collector, then merge-loads all collectors into
// HashedAccounts / HashedStorage in globally-sorted order via etl.LoadMerged.
//
// At chain scale the single-RoTx sequential scan (MDBX page read+decompress) is
// the bottleneck — N per-worker RoTx over disjoint first-byte ranges parallelize
// the reads (and the keccak) near-linearly. The merge-load runs single-threaded
// through the passed RwTx after all workers finish. Per-account #1 memoization of
// keccak(addr) is preserved within each shard (a shard never splits an address,
// since sharding is by addr's first byte).
//
// db supplies the per-worker RoTx (reads see committed PlainState; the uncommitted
// ClearBucket on the hashed tables in tx is invisible to them — different tables).
// Falls back to the sequential path when workers<=1 or db==nil.
func RebuildHashedStateETLParallel(ctx context.Context, db kv.RoDB, tx kv.RwTx, workers int, tmpdir string, logger log2.Logger) error {
	if workers <= 1 || db == nil {
		return RebuildHashedStateETL(ctx, tx, tmpdir, logger)
	}
	if tmpdir == "" {
		base := os.Getenv("N42_ETL_TMPDIR")
		if base != "" {
			if err := os.MkdirAll(base, 0755); err != nil {
				return fmt.Errorf("create N42_ETL_TMPDIR=%s: %w", base, err)
			}
		}
		dir, err := os.MkdirTemp(base, "hashed-state-etl-par-*")
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

	emptyCodeHash := crypto.Keccak256Hash(nil)
	accBufPer := perWorkerBuf("N42_ETL_BUFFER_ACCT_GB", 4, workers, 64*datasize.MB)
	stoBufPer := perWorkerBuf("N42_ETL_BUFFER_STO_GB", 12, workers, 128*datasize.MB)
	log.Info("RebuildHashedStateETLParallel: starting",
		"workers", workers, "acctBufPerMB", uint64(accBufPer/datasize.MB),
		"stoBufPerMB", uint64(stoBufPer/datasize.MB))

	// ---- Accounts: shard by first addr byte, keccak(addr) → HashedAccounts ----
	t0 := time.Now()
	accColls := make([]*etl.Collector, workers)
	var accCounts []int64 = make([]int64, workers)
	if err := runShards(workers, func(w, lo, hi int) error {
		accColls[w] = etl.NewCollector(fmt.Sprintf("rehash-acct-%d", w),
			filepath.Join(tmpdir, fmt.Sprintf("acct-%d", w)),
			etl.NewSortableBuffer(accBufPer), logger)
		return scanPlainStateShard(ctx, db, "Account", lo, hi, func(k, v []byte) error {
			if len(k) != 20 {
				return nil
			}
			var acc account.StateAccount
			if err := acc.DecodeForStorage(v); err != nil {
				return fmt.Errorf("decode account: %w", err)
			}
			if acc.CodeHash == (types.Hash{}) {
				acc.CodeHash = emptyCodeHash
			}
			accCounts[w]++
			return accColls[w].Collect(crypto.Keccak256(k), acc.MarshalV2())
		})
	}); err != nil {
		closeColls(accColls)
		return fmt.Errorf("parallel hash accounts: %w", err)
	}
	if err := etl.LoadMerged(tx, kv.HashedAccounts, etl.IdentityLoadFunc, etl.TransformArgs{}, accColls...); err != nil {
		closeColls(accColls)
		return fmt.Errorf("load HashedAccounts: %w", err)
	}
	closeColls(accColls)
	log.Info("RebuildHashedStateETLParallel: accounts done",
		"count", sumInt64(accCounts), "elapsed", time.Since(t0).Truncate(time.Millisecond))

	// ---- Storage: shard by first addr byte, keccak(addr)+keccak(slot) ----
	tSto := time.Now()
	stoColls := make([]*etl.Collector, workers)
	var stoCounts []int64 = make([]int64, workers)
	if err := runShards(workers, func(w, lo, hi int) error {
		stoColls[w] = etl.NewCollector(fmt.Sprintf("rehash-sto-%d", w),
			filepath.Join(tmpdir, fmt.Sprintf("sto-%d", w)),
			etl.NewSortableBuffer(stoBufPer), logger)
		// #1 memoize keccak(addr) within this shard (a shard never splits an addr).
		var prevAddr [20]byte
		var prevHashedAddr [32]byte
		var hasPrev bool
		return scanPlainStateShard(ctx, db, "Storage", lo, hi, func(k, v []byte) error {
			if len(k) < 52 {
				return nil
			}
			var compositeKey [64]byte
			if hasPrev && bytes.Equal(k[:20], prevAddr[:]) {
				copy(compositeKey[:32], prevHashedAddr[:])
			} else {
				copy(compositeKey[:32], crypto.Keccak256(k[:20]))
				copy(prevAddr[:], k[:20])
				copy(prevHashedAddr[:], compositeKey[:32])
				hasPrev = true
			}
			copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
			stoCounts[w]++
			return stoColls[w].Collect(compositeKey[:], v)
		})
	}); err != nil {
		closeColls(stoColls)
		return fmt.Errorf("parallel hash storage: %w", err)
	}
	if err := etl.LoadMerged(tx, kv.HashedStorage, etl.IdentityLoadFunc, etl.TransformArgs{}, stoColls...); err != nil {
		closeColls(stoColls)
		return fmt.Errorf("load HashedStorage: %w", err)
	}
	closeColls(stoColls)
	log.Info("RebuildHashedStateETLParallel done",
		"accts", sumInt64(accCounts), "sto", sumInt64(stoCounts),
		"storageElapsed", time.Since(tSto).Truncate(time.Millisecond),
		"total", time.Since(t0).Truncate(time.Millisecond))
	return nil
}

// scanPlainStateShard walks [loByte, hiByte) of a table's first-key-byte
// range. Keep the initial Seek error separate from the loop condition: a failed
// Seek commonly returns (nil, nil, err), and putting it in a `k != nil` loop
// initializer silently turns that storage failure into an empty shard.
func scanPlainStateShard(ctx context.Context, db kv.RoDB, plainTable string, loByte, hiByte int, visit func(k, v []byte) error) error {
	if loByte < 0 || loByte >= 256 || hiByte <= loByte || hiByte > 256 {
		return fmt.Errorf("invalid %s byte shard [%d,%d)", plainTable, loByte, hiByte)
	}
	roTx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer roTx.Rollback()
	cur, err := roTx.Cursor(plainTable)
	if err != nil {
		return err
	}
	defer cur.Close()

	k, v, err := cur.Seek([]byte{byte(loByte)})
	if err != nil {
		return err
	}
	for k != nil {
		if len(k) == 0 {
			return fmt.Errorf("%s contains an empty key", plainTable)
		}
		if int(k[0]) >= hiByte {
			break
		}
		if err := visit(k, v); err != nil {
			return err
		}
		k, v, err = cur.Next()
		if err != nil {
			return err
		}
	}
	return nil
}

// processByteShards runs `workers` goroutines that pull single-byte shards
// (first-key-byte b in [0,256)) from a shared work-stealing queue, invoking
// fn(workerIdx, b) for each. Unlike runShards' fixed equal ranges, this balances
// SKEWED inputs: a mega-contract whose whole storage falls in one byte shard is
// bounded to 1/256 of the keyspace while the other workers drain the rest — so
// the straggler is one heavy byte, not a 16-byte range that happened to contain
// it. Each worker writes only to its own collector (fn closes over workerIdx),
// so there is no cross-worker contention.
func processByteShards(workers int, fn func(w, b int) error) error {
	var next int64
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				b := int(atomic.AddInt64(&next, 1)) - 1
				if b >= 256 {
					return
				}
				if err := fn(w, b); err != nil {
					errs[w] = err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// runShards splits the 0..256 first-byte space into `workers` contiguous ranges
// and runs fn(workerIdx, loByte, hiByte) concurrently, returning the first error.
func runShards(workers int, fn func(w, lo, hi int) error) error {
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := 0; w < workers; w++ {
		lo := w * 256 / workers
		hi := (w + 1) * 256 / workers
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			errs[w] = fn(w, lo, hi)
		}(w, lo, hi)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func perWorkerBuf(env string, defGB uint64, workers int, floor datasize.ByteSize) datasize.ByteSize {
	per := (envBufSize(env, defGB) * datasize.GB) / datasize.ByteSize(workers)
	if per < floor {
		per = floor
	}
	return per
}

func closeColls(colls []*etl.Collector) {
	for _, c := range colls {
		if c != nil {
			c.Close()
		}
	}
}

func sumInt64(xs []int64) int64 {
	var s int64
	for _, x := range xs {
		s += x
	}
	return s
}

// hashPlainStateToCollectors parallel-hashes PlainState Account/Storage into
// `workers` per-worker etl.Collectors (work-stealing 256 byte shards, keccak(addr)
// memoized per shard). Returns the flushed-but-open collectors (caller closes);
// closes them itself on error. Shared by the streaming root builders.
func hashPlainStateToCollectors(ctx context.Context, db kv.RoDB, workers int, tmpdir string, accBufPer, stoBufPer datasize.ByteSize, logger log2.Logger) ([]*etl.Collector, []*etl.Collector, error) {
	emptyCodeHash := crypto.Keccak256Hash(nil)
	accColls := make([]*etl.Collector, workers)
	stoColls := make([]*etl.Collector, workers)
	for w := 0; w < workers; w++ {
		accColls[w] = etl.NewCollector(fmt.Sprintf("hash-acct-%d", w),
			filepath.Join(tmpdir, fmt.Sprintf("acct-%d", w)), etl.NewSortableBuffer(accBufPer), logger)
		stoColls[w] = etl.NewCollector(fmt.Sprintf("hash-sto-%d", w),
			filepath.Join(tmpdir, fmt.Sprintf("sto-%d", w)), etl.NewSortableBuffer(stoBufPer), logger)
	}
	if err := processByteShards(workers, func(w, b int) error {
		return scanPlainStateShard(ctx, db, "Account", b, b+1, func(k, v []byte) error {
			if len(k) != 20 {
				return nil
			}
			var acc account.StateAccount
			if err := acc.DecodeForStorage(v); err != nil {
				return fmt.Errorf("decode account: %w", err)
			}
			if acc.CodeHash == (types.Hash{}) {
				acc.CodeHash = emptyCodeHash
			}
			return accColls[w].Collect(crypto.Keccak256(k), acc.MarshalV2())
		})
	}); err != nil {
		closeColls(accColls)
		closeColls(stoColls)
		return nil, nil, fmt.Errorf("parallel hash accounts: %w", err)
	}
	if err := processByteShards(workers, func(w, b int) error {
		var prevAddr [20]byte
		var prevHashedAddr [32]byte
		var hasPrev bool
		return scanPlainStateShard(ctx, db, "Storage", b, b+1, func(k, v []byte) error {
			if len(k) < 52 {
				return nil
			}
			var compositeKey [64]byte
			if hasPrev && bytes.Equal(k[:20], prevAddr[:]) {
				copy(compositeKey[:32], prevHashedAddr[:])
			} else {
				copy(compositeKey[:32], crypto.Keccak256(k[:20]))
				copy(prevAddr[:], k[:20])
				copy(prevHashedAddr[:], compositeKey[:32])
				hasPrev = true
			}
			copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
			return stoColls[w].Collect(compositeKey[:], v)
		})
	}); err != nil {
		closeColls(accColls)
		closeColls(stoColls)
		return nil, nil, fmt.Errorf("parallel hash storage: %w", err)
	}
	return accColls, stoColls, nil
}

// WARNING (2026-06-14): correct on unit fixtures but NOT production-ready — at real
// 2B-leaf scale the demux pipeline below serializes and balloons RAM to ~104 GB
// (two global-sorted demux goroutines feeding 16 lockstep consumers + ungoverned
// per-leaf copies). Needs a redesign to per-nibble PARTITIONED collectors (built
// during the hash phase) so the 16 subtrie builds are genuinely independent. Kept
// for the proven MPT mechanism (cutoff-1 subtrie + CombineNibbleSubtries); callers
// should use StreamingFullStateRoot until this is reworked.
//
// StreamingFullStateRootC2 is StreamingFullStateRoot with a PARALLEL trie build.
// After the parallel hash it demuxes the two globally-sorted hashed-leaf streams by
// top account nibble into 16 INDEPENDENT subtrie builds (CalcTrieRootStreamingCutoff
// cutoff=1) run concurrently, then folds the 16 subtrie hashes into the root via
// CombineNibbleSubtries. This attacks the serial GenStructStep trie-build floor that
// StreamingFullStateRoot still pays (~40min at 25M scale → ~subtrie-parallel). A
// storage composite key's first nibble == its account's hashed-key first nibble
// (both keccak(addr)[0]), so demuxing by first nibble keeps each account together
// with its storage. Root is byte-identical to FullStateRootVerify (pinned by tests).
// Requires the top node to be a clean 16-branch (no top extension) — true for any
// non-trivial mainnet state; for tiny/empty states use StreamingFullStateRoot.
func StreamingFullStateRootC2(ctx context.Context, db kv.RoDB, workers int, tmpdir string, logger log2.Logger) (types.Hash, error) {
	if workers < 1 {
		workers = 1
	}
	if tmpdir == "" {
		base := os.Getenv("N42_ETL_TMPDIR")
		if base != "" {
			if err := os.MkdirAll(base, 0755); err != nil {
				return types.Hash{}, fmt.Errorf("create N42_ETL_TMPDIR=%s: %w", base, err)
			}
		}
		dir, err := os.MkdirTemp(base, "stream-root-c2-*")
		if err != nil {
			return types.Hash{}, fmt.Errorf("mkdir tmp: %w", err)
		}
		defer os.RemoveAll(dir)
		tmpdir = dir
	}
	accBufPer := perWorkerBuf("N42_ETL_BUFFER_ACCT_GB", 4, workers, 64*datasize.MB)
	stoBufPer := perWorkerBuf("N42_ETL_BUFFER_STO_GB", 12, workers, 128*datasize.MB)

	t0 := time.Now()
	accColls, stoColls, err := hashPlainStateToCollectors(ctx, db, workers, tmpdir, accBufPer, stoBufPer, logger)
	if err != nil {
		return types.Hash{}, err
	}
	defer closeColls(accColls)
	defer closeColls(stoColls)
	tHash := time.Since(t0)

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type kvCopy struct{ k, v []byte }
	var accNib, stoNib [16]chan kvCopy
	for i := 0; i < 16; i++ {
		accNib[i] = make(chan kvCopy, 256)
		stoNib[i] = make(chan kvCopy, 256)
	}
	var accErr, stoErr error
	demux := func(chans *[16]chan kvCopy, errp *error, colls []*etl.Collector) {
		*errp = etl.StreamMerged(func(k, v []byte) error {
			sel := kvCopy{append([]byte(nil), k...), append([]byte(nil), v...)}
			select {
			case (*chans)[k[0]>>4] <- sel:
				return nil
			case <-sctx.Done():
				return sctx.Err()
			}
		}, colls...)
		for i := 0; i < 16; i++ {
			close((*chans)[i])
		}
	}
	go demux(&accNib, &accErr, accColls)
	go demux(&stoNib, &stoErr, stoColls)

	var subs [16][]byte
	var subErrs [16]error
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accNext := func() ([]byte, []byte, bool, error) {
				p, ok := <-accNib[i]
				if !ok {
					return nil, nil, false, nil
				}
				return p.k, p.v, true, nil
			}
			stoNext := func() ([]byte, []byte, bool, error) {
				p, ok := <-stoNib[i]
				if !ok {
					return nil, nil, false, nil
				}
				return p.k, p.v, true, nil
			}
			loader := trie.NewFlatDBTrieLoader(fmt.Sprintf("c2-nib-%x", i), trie.NewRetainList(0), nil, nil, false)
			h, e := loader.CalcTrieRootStreamingCutoff(accNext, stoNext, 1)
			if e != nil {
				subErrs[i] = e
				return
			}
			if h != trie.EmptyRoot { // empty nibble → leave subs[i] nil (no branch child)
				hb := make([]byte, 32)
				copy(hb, h[:])
				subs[i] = hb
			}
		}(i)
	}
	wg.Wait()
	cancel()
	for i := 0; i < 16; i++ {
		if subErrs[i] != nil {
			return types.Hash{}, fmt.Errorf("nibble %x subtrie: %w", i, subErrs[i])
		}
	}
	if accErr != nil && !errors.Is(accErr, context.Canceled) {
		return types.Hash{}, fmt.Errorf("account stream: %w", accErr)
	}
	if stoErr != nil && !errors.Is(stoErr, context.Canceled) {
		return types.Hash{}, fmt.Errorf("storage stream: %w", stoErr)
	}
	root, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		return types.Hash{}, fmt.Errorf("combine subtries: %w", err)
	}
	log.Info("StreamingFullStateRootC2 done", "root", root.Hex(), "workers", workers,
		"hashElapsed", tHash.Truncate(time.Millisecond), "total", time.Since(t0).Truncate(time.Millisecond))
	return root, nil
}

// StreamingFullStateRoot computes the full ETH state root WITHOUT materializing
// HashedAccounts/HashedStorage into MDBX. It parallel-hashes PlainState into
// per-worker etl.Collectors (sharded by first addr byte, #1 keccak(addr)
// memoization), then merge-streams the two sorted leaf sets directly into a
// full-rebuild FlatDBTrieLoader via CalcTrieRootStreaming — fusing the sort with
// the root build. This skips the ~50min single-threaded HashedStorage MDBX load
// that RebuildHashedStateETL(Parallel)+CalcTrieRoot pays (the hashed tables are
// transient for verification anyway). Root is byte-identical to FullStateRootVerify
// (pinned by TestStreamingFullStateRootMatchesFlatDB).
//
// db supplies per-worker RoTx over committed PlainState; reads only. workers<=1
// runs a single shard. Returns the state root; does not write to the DB.
func StreamingFullStateRoot(ctx context.Context, db kv.RoDB, workers int, tmpdir string, logger log2.Logger) (types.Hash, error) {
	if workers < 1 {
		workers = 1
	}
	if tmpdir == "" {
		base := os.Getenv("N42_ETL_TMPDIR")
		if base != "" {
			if err := os.MkdirAll(base, 0755); err != nil {
				return types.Hash{}, fmt.Errorf("create N42_ETL_TMPDIR=%s: %w", base, err)
			}
		}
		dir, err := os.MkdirTemp(base, "stream-root-etl-*")
		if err != nil {
			return types.Hash{}, fmt.Errorf("mkdir tmp: %w", err)
		}
		defer os.RemoveAll(dir)
		tmpdir = dir
	}

	accBufPer := perWorkerBuf("N42_ETL_BUFFER_ACCT_GB", 4, workers, 64*datasize.MB)
	stoBufPer := perWorkerBuf("N42_ETL_BUFFER_STO_GB", 12, workers, 128*datasize.MB)

	t0 := time.Now()
	accColls, stoColls, err := hashPlainStateToCollectors(ctx, db, workers, tmpdir, accBufPer, stoBufPer, logger)
	if err != nil {
		return types.Hash{}, err
	}
	defer closeColls(accColls)
	defer closeColls(stoColls)
	log.Info("StreamingFullStateRoot: hashed, streaming into trie builder",
		"workers", workers, "hashElapsed", time.Since(t0).Truncate(time.Millisecond))

	// Two goroutines turn the per-worker collectors into globally-sorted pull
	// streams; the merge-walk in CalcTrieRootStreaming consumes them.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type kvCopy struct{ k, v []byte }
	accCh := make(chan kvCopy, 1024)
	stoCh := make(chan kvCopy, 1024)
	var accErr, stoErr error
	var pumpWG sync.WaitGroup
	pump := func(ch chan kvCopy, errp *error, colls []*etl.Collector) {
		defer pumpWG.Done()
		*errp = etl.StreamMerged(func(k, v []byte) error {
			sel := kvCopy{append([]byte(nil), k...), append([]byte(nil), v...)}
			select {
			case ch <- sel:
				return nil
			case <-sctx.Done():
				return sctx.Err()
			}
		}, colls...)
		close(ch)
	}
	pumpWG.Add(2)
	go pump(accCh, &accErr, accColls)
	go pump(stoCh, &stoErr, stoColls)

	accNext := func() ([]byte, []byte, bool, error) {
		p, ok := <-accCh
		if !ok {
			return nil, nil, false, accErr
		}
		return p.k, p.v, true, nil
	}
	stoNext := func() ([]byte, []byte, bool, error) {
		p, ok := <-stoCh
		if !ok {
			return nil, nil, false, stoErr
		}
		return p.k, p.v, true, nil
	}

	loader := trie.NewFlatDBTrieLoader("stream-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRootStreaming(accNext, stoNext)
	cancel()
	// CalcTrieRootStreaming can finish while one producer still has buffered
	// output. Cancellation asks both pumps to stop; join them before reading
	// accErr/stoErr so the early-finish path has the same synchronization as
	// consuming a channel through close.
	pumpWG.Wait()
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRootStreaming: %w", err)
	}
	// Surface a producer error the consumer didn't observe (e.g. a provider Wait
	// failed). context.Canceled is the normal teardown signal (cancel() above when
	// a stream had unconsumed leftovers) — not a real error.
	if accErr != nil && !errors.Is(accErr, context.Canceled) {
		return types.Hash{}, fmt.Errorf("account stream: %w", accErr)
	}
	if stoErr != nil && !errors.Is(stoErr, context.Canceled) {
		return types.Hash{}, fmt.Errorf("storage stream: %w", stoErr)
	}
	log.Info("StreamingFullStateRoot done", "root", root.Hex(),
		"total", time.Since(t0).Truncate(time.Millisecond))
	return root, nil
}

// FullStateRootVerify does a complete re-hash and MPT root computation.
// It clears HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage,
// re-hashes everything from Account/Storage, then runs CalcTrieRoot.
// This is expensive but guaranteed correct for verification.
//
// Uses RebuildHashedStateETL (etl.Collector + sorted bulk-load) for the
// re-hash phase — orders of magnitude faster than the legacy random-Put
// path on 10M+ block states.
// db + workers enable the parallel re-hash (per-worker RoTx). Pass db=nil or
// workers<=1 for the sequential path (RebuildHashedStateETLParallel falls back).
func FullStateRootVerify(db kv.RoDB, tx kv.RwTx, workers int) (types.Hash, error) {
	// Clear trie tables here; HashedAccounts/HashedStorage are cleared
	// inside the re-hash.
	for _, tbl := range []string{kv.TrieOfAccounts, kv.TrieOfStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return types.Hash{}, fmt.Errorf("clear %s: %w", tbl, err)
		}
	}

	if err := RebuildHashedStateETLParallel(context.Background(), db, tx, workers, "", log2.New()); err != nil {
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
//  1. Clear HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage
//  2. hashAllAccountsBatched — commit every accountBatchN entries
//  3. hashAllStorageBatched — commit every storageBatchN entries
//     (HashedStorage is DupSort, which inflates dirty-page cost; keep this
//     batch size modest — a few million per commit)
//  4. CalcTrieRoot + flush TrieOf* intermediate nodes (single RwTx; the
//     intermediate-node set is small relative to plain state, ~1-3 GB
//     dirty at 12.5M, well within any reasonable DirtySpace).
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

// BootstrapHPHFastETL fills the four TrieRootComputer tables (HashedAccounts,
// HashedStorage, TrieOfAccounts, TrieOfStorage) from PlainState, using the
// sorted etl.Collector bulk-load for the hashing phase instead of the
// random-key tx.Put path in BootstrapHPHBatched.
//
// Why: writing HashedAccounts/HashedStorage is keyed by keccak(addr) /
// keccak(slot) — i.e. RANDOM order. The batched random-Put path degrades
// catastrophically once the table outgrows the page cache (observed at 25M
// state: 108M/386M accounts after 55 min and decelerating, ~500 MB/s random
// reads to locate B-tree insertion points → ~8 h projected). The ETL path
// sorts keys on tmpfile first and bulk-loads in sorted order, so writes are
// sequential and the working set stays cache-hot.
//
// Phases (separate RwTx each, to bound dirty-page pressure):
//  1. Clear the four tables.
//  2. RebuildHashedStateETL → HashedAccounts/HashedStorage (sorted load).
//  3. CalcTrieRoot with collectors → persist TrieOfAccounts/TrieOfStorage.
//
// tmpdir holds ETL spill files (~30 GB at 25M state); put it on the same
// fast drive as the datadir. The caller's db should be opened with a large
// DirtySpace (≥32 GB) for the sorted bulk-load tx.
func BootstrapHPHFastETL(ctx context.Context, db kv.RwDB, tmpdir string) (types.Hash, error) {
	logger := log2.New()

	// Phase 1: clear the four tables (own tx).
	{
		log.Info("BootstrapHPHFastETL: clearing HashedAccounts/HashedStorage/TrieOf*")
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

	// Phase 2: ETL sorted hashing of accounts + storage (own tx).
	{
		log.Info("BootstrapHPHFastETL: hashing PlainState → HashedAccounts/HashedStorage via sorted ETL")
		tHash := time.Now()
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return types.Hash{}, err
		}
		if err := RebuildHashedStateETL(ctx, tx, tmpdir, logger); err != nil {
			tx.Rollback()
			return types.Hash{}, fmt.Errorf("RebuildHashedStateETL: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return types.Hash{}, fmt.Errorf("commit hashed state: %w", err)
		}
		log.Info("BootstrapHPHFastETL: hashing complete", "elapsed", time.Since(tHash).Truncate(time.Second))
	}

	// Phase 3: CalcTrieRoot + persist intermediate nodes (own tx).
	log.Info("BootstrapHPHFastETL: computing trie root via FlatDBTrieLoader")
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

	loader := trie.NewFlatDBTrieLoader("bootstrap-hph-etl", trie.NewRetainList(0), accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	log.Info("BootstrapHPHFastETL: flushing intermediate trie nodes",
		"accTrie", len(accTrie), "storTrie", len(storTrie))
	for _, e := range accTrie {
		if e.v == nil {
			_ = tx.Delete(kv.TrieOfAccounts, e.k)
		} else {
			if err := tx.Put(kv.TrieOfAccounts, e.k, e.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	for _, e := range storTrie {
		if e.v == nil {
			_ = tx.Delete(kv.TrieOfStorage, e.k)
		} else {
			if err := tx.Put(kv.TrieOfStorage, e.k, e.v); err != nil {
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
			var compositeKey [64]byte
			copy(compositeKey[:32], crypto.Keccak256(k[:20]))
			copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
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
		var compositeKey [64]byte
		copy(compositeKey[:32], crypto.Keccak256(k[:20]))
		copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
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
		var compositeKey [64]byte
		copy(compositeKey[:32], crypto.Keccak256(k[:20]))
		copy(compositeKey[32:64], crypto.Keccak256(k[20:52]))
		if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
			return fmt.Errorf("put HashedStorage: %w", err)
		}
		stoCount++
	}

	log.Info("HashState initialization complete", "accounts", accCount, "storage", stoCount)
	return nil
}
