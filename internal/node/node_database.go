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
//
// Database opening (MDBX, layered, frame store, vote journal) and genesis writing helpers.

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/common/cmp"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/lthash"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func OpenDatabase(ctx context.Context, cfg *conf.Config, logger log2.Logger, name string) (kv.RwDB, error) {
	if cfg.NodeCfg.DataDir == "" {
		return memdb.New(""), nil
	}

	if logger == nil {
		logger = log2.New()
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// If layered DB is enabled, split into state + history databases.
	if cfg.LayeredDBCfg.Enable {
		return openLayeredDatabase(ctx, cfg, logger, name)
	}

	// Use hot-tier path for chaindata when storage tiering is enabled.
	baseDir := cfg.NodeCfg.DataDir
	if cfg.StorageTierCfg.Enabled && cfg.StorageTierCfg.HotPath != "" {
		baseDir = cfg.StorageTierCfg.HotPath
	}
	dbPath := filepath.Join(baseDir, name)
	log.Info("Opening database", "name", name, "path", dbPath)

	roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8)))

	chainOpts := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(dbPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(mdbxMapSizeOr(8 * datasize.TB))
	if gb := mdbxDirtyGB(); gb > 0 {
		chainOpts = chainOpts.DirtySpace(gb * uint64(datasize.GB))
	}
	chainOpts = mdbxSyncModeOr(chainOpts, logger)
	chainKv, err := chainOpts.Open(ctx)
	if err != nil {
		return nil, err
	}

	if err = chainKv.Update(ctx, func(tx kv.RwTx) error {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		return nil, err
	}
	return chainKv, nil
}

// frameStoreEnabled is N42_FRAME_DB=1: frames go to their own MDBX store
// (<datadir>/frames) instead of the chaindata Frames table
// (docs/QS_MULTI_MILLION_DESIGN.md P11). Frames already in the Frames
// table stay readable there.
func frameStoreEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("N42_FRAME_DB"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// frameLogEnabled is N42_FRAME_LOG=1 (with N42_FRAME_DB=1): frame data is
// appended to a log beside the frame store (rawdb/frame_log.go).
func frameLogEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("N42_FRAME_LOG"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// openFrameStoreDB opens the frame store when enabled (nil otherwise), with
// the chain database's map size and durability mode.
func openFrameStoreDB(ctx context.Context, cfg *conf.Config) (kv.RwDB, error) {
	if !frameStoreEnabled() || cfg.NodeCfg.DataDir == "" {
		return nil, nil
	}
	logger := log2.New()
	path := filepath.Join(cfg.NodeCfg.DataDir, "frames")
	log.Info("Opening the frame store (N42_FRAME_DB)", "path", path)
	opts := mdbx.NewMDBX(logger).WriteMergeThreshold(4 * 8192).Path(path).Label(kv.ChainDB).
		WithTableCfg(func(c kv.TableCfg) kv.TableCfg { return rawdb.FrameLogIndexTables(rawdb.FrameStoreTables(c)) }).
		MapSize(frameMapSize())
	if n := rawdb.FramePruneBatches(); n > 0 {
		log.Warn("!!! FRAME STORE PRUNING ON (BENCH ONLY) !!! N42_FRAME_DB_PRUNE_BATCHES: frames older than "+
			"the last writes are deleted; this node cannot serve or re-read older blocks", "keep", n)
	}
	// BENCH ONLY: N42_FRAME_DB_SYNC=safe-nosync drops the frame store's
	// per-commit fsync while the chain database stays durable. A crash can
	// then leave a durable block whose frames were lost, which the node must
	// fetch from its peers again. It exists to measure how much of the frame
	// write is the fsync when seven nodes share one disk.
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("N42_FRAME_DB_SYNC"))); v == "safe-nosync" {
		log.Warn("!!! FRAME STORE DURABILITY REDUCED (BENCH ONLY) !!! N42_FRAME_DB_SYNC=safe-nosync: " +
			"a crash can lose frames of blocks that are already durable")
		return opts.SafeNoSync().Open(ctx)
	} else if v != "" && v != "durable" {
		log.Warn("ignoring unknown N42_FRAME_DB_SYNC value", "value", v)
	}
	return mdbxSyncModeOr(opts, logger).Open(ctx)
}

// frameMapSize is the frame store's map size: N42_FRAME_DB_MAPSIZE_GB when
// set (a pruned bench store on a small disk must fail with MAP_FULL rather
// than fill it), else the chain database's.
func frameMapSize() datasize.ByteSize {
	if gb, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("N42_FRAME_DB_MAPSIZE_GB")), 10, 64); err == nil && gb > 0 {
		return datasize.ByteSize(gb) * datasize.GB
	}
	return mdbxMapSizeOr(8 * datasize.TB)
}

// openVoteJournalDB opens the separate HotStuff vote journal store
// (hotstuff.SeparateVoteJournalEnabled): one table, a few hundred bytes,
// with the chain database's durability mode.
func openVoteJournalDB(ctx context.Context, path string) (kv.RwDB, error) {
	logger := log2.New()
	opts := mdbx.NewMDBX(logger).Path(path).Label(kv.ConsensusDB).
		WithTableCfg(hotstuff.VoteJournalTables).
		MapSize(64 * datasize.MB).GrowthStep(4 * datasize.MB)
	return mdbxSyncModeOr(opts, logger).Open(ctx)
}

// mdbxSyncModeOr applies a non-default MDBX durability mode when
// N42_MDBX_SYNC is set. Unset — the default, and what every deployment gets
// unless someone opts out on purpose — leaves mdbx.Durable: every block's
// commit is fsynced, so a power cut costs nothing.
//
// The commit is the single largest cost on a loaded node (mdbx_txn_commit_ex
// was 26% of cgo time at 5.2k tx/s), and part of that is the fsync. This exists
// so that share can be measured on real data rather than argued from a
// microbenchmark, and so an operator who has decided the tradeoff is worth it
// can take it. It is deliberately an environment variable and not a config
// field: it weakens a durability guarantee and should have to be typed out.
//
//	safe-nosync — skip the per-commit fsync; the kernel owns writeback. Crash
//	              or power loss rolls the DB back to the last checkpoint,
//	              losing recent commits but not integrity. A validator that
//	              loses blocks this way re-syncs them from its peers.
func mdbxSyncModeOr(opts mdbx.MdbxOpts, logger log2.Logger) mdbx.MdbxOpts {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("N42_MDBX_SYNC"))); v {
	case "", "durable":
		return opts
	case "safe-nosync":
		logger.Warn("MDBX durability reduced by N42_MDBX_SYNC=safe-nosync — " +
			"per-commit fsync disabled; a crash or power loss can roll the chaindata " +
			"back several blocks, which the node must then re-sync from peers")
		return opts.SafeNoSync()
	default:
		logger.Warn("ignoring unknown N42_MDBX_SYNC value, staying durable", "value", v)
		return opts
	}
}

// checkPrefetchHasCache rejects --prefetch when there is no shared state cache
// for it to populate.
//
// Prefetch's entire benefit path is that its I/O workers warm the ShardedCache
// the executor subsequently reads through. evmRecord wraps the block's reader
// in a CachedStateReader only when layered.ExtractCache returns non-nil, and
// that happens solely for a *layered.LayeredDB; LayeredDBCfg.Enable defaults
// false and no CLI flag sets it. So on a normal datadir the prefetch workers
// race the executor's single MDBX cursor to fill a cache that does not exist:
// all of the halt risk, none of the benefit. A log warning is the wrong
// response to a flag that can only cost, so this fails fast the way the
// mobileverify guard does and names the config key that would make it coherent.
//
// Passing a non-nil cache does NOT make prefetch safe -- the shared-cursor race
// in internal/prefetcher.go is unfixed and its warning still fires. This guard
// only removes the case where the race buys nothing at all.
func checkPrefetchHasCache(cache *layered.ShardedCache) error {
	if cache != nil {
		return nil
	}
	return errors.New("prefetch is enabled but there is no shared state cache to populate: " +
		"its reads reach the executor only through the ShardedCache of a layered DB, and " +
		"layered_db.enable is false, so prefetch would race the executor's MDBX cursor " +
		"(see internal/prefetcher.go) for no benefit. Enable layered_db.enable, or turn prefetch off")
}

// mdbxDirtyGB returns an override in GB for the ChainDB's dirty-page limit,
// from N42_MDBX_DIRTY_GB, or 0 to leave MDBX's computed default alone.
//
// Why this exists. computeDirtySpace gives the ChainDB min(TotalMemory/42,
// 1 GB), so a 136 GB box gets 1 GB and would otherwise have had 3.24 GB. When a
// block's dirty set crosses that limit MDBX spills pages mid-transaction, and
// the cost lands in whichever write phase is running rather than in the commit:
// measured on the qs fleet, 5% of blocks ran chgset at 1021 ms against 191 ms
// for the rest while writing only 1.06x the bytes, which is ~42 ms a block
// averaged over the workload and about 7% of the write path. It is also the
// single largest source of run-to-run variance on that rig -- large enough that
// four rounds could not measure a 100 ms treatment through it.
//
// An env var rather than a config field, matching N42_MDBX_MAPSIZE_GB above, so
// a round can A/B the limit without a rebuild. Raising it costs resident memory
// in the worst case (a transaction may hold that many dirty pages before
// spilling), so it is not raised by default here: the default stays whatever
// MDBX computes until a round says what the right value is.
func mdbxDirtyGB() uint64 {
	if v := os.Getenv("N42_MDBX_DIRTY_GB"); v != "" {
		if gb, err := strconv.ParseUint(v, 10, 64); err == nil && gb > 0 {
			return gb
		}
	}
	return 0
}

// mdbxMapSizeOr returns sz, or an override from N42_MDBX_MAPSIZE_GB when set.
// Running several local nodes on Windows, each reserving an 8 TB map, exhausts
// the system page file (mdbx_env_open: "paging file is too small"); this lets a
// local test harness shrink the per-node reservation.
func mdbxMapSizeOr(sz datasize.ByteSize) datasize.ByteSize {
	if v := os.Getenv("N42_MDBX_MAPSIZE_GB"); v != "" {
		if gb, err := strconv.ParseUint(v, 10, 64); err == nil && gb > 0 {
			return datasize.ByteSize(gb) * datasize.GB
		}
	}
	return sz
}

// openLayeredDatabase creates a LayeredDB with separate state and history
// MDBX instances. The state DB is smaller and faster (holds only current
// state), while the history DB handles append-heavy changeset/index data.
func openLayeredDatabase(ctx context.Context, cfg *conf.Config, logger log2.Logger, name string) (kv.RwDB, error) {
	if err := cfg.LayeredDBCfg.Validate(); err != nil {
		return nil, fmt.Errorf("layered DB config: %w", err)
	}

	stateDBPath := cfg.LayeredDBCfg.StateDBPath
	if stateDBPath == "" {
		stateBase := cfg.NodeCfg.DataDir
		if cfg.StorageTierCfg.Enabled && cfg.StorageTierCfg.HotPath != "" {
			stateBase = cfg.StorageTierCfg.HotPath
		}
		stateDBPath = filepath.Join(stateBase, name+"-state")
	}
	historyDBPath := cfg.LayeredDBCfg.HistoryDBPath
	if historyDBPath == "" {
		histBase := cfg.NodeCfg.DataDir
		if cfg.StorageTierCfg.Enabled && cfg.StorageTierCfg.ColdPath != "" {
			histBase = cfg.StorageTierCfg.ColdPath
		}
		historyDBPath = filepath.Join(histBase, name+"-history")
	}

	log.Info("Opening layered database",
		"stateDB", stateDBPath,
		"historyDB", historyDBPath,
		"cacheShards", cfg.LayeredDBCfg.CacheShards,
		"cacheCapacity", cfg.LayeredDBCfg.CacheCapacity,
	)

	roTxsLimiter := semaphore.NewWeighted(int64(cmp.Max(32, runtime.GOMAXPROCS(-1)*8)))

	// State DB: smaller MapSize, optimized for random read/write.
	stateKv, err := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(stateDBPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(2 * datasize.TB).
		Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("open state DB: %w", err)
	}

	// History DB: larger MapSize, optimized for sequential append.
	historyKv, err := mdbx.NewMDBX(logger).
		WriteMergeThreshold(4 * 8192).
		Path(historyDBPath).Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).RoTxsLimiter(roTxsLimiter).
		MapSize(8 * datasize.TB).
		Open(ctx)
	if err != nil {
		stateKv.Close()
		return nil, fmt.Errorf("open history DB: %w", err)
	}

	db := layered.NewLayeredDB(stateKv, historyKv, &cfg.LayeredDBCfg)

	if err = db.Update(ctx, func(tx kv.RwTx) error {
		return params.SetN42Version(tx, params.VersionKeyCreated)
	}); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func WriteGenesisBlock(db kv.RwTx, genesis *conf.Genesis) (*block.Block, error) {
	if genesis == nil {
		return nil, internal.ErrGenesisNoConfig
	}

	g := &internal.GenesisBlock{
		Hash:          "",
		GenesisConfig: genesis,
	}
	log.Info("Initializing genesis block...")
	genBlock, _, err := g.Write(db)
	if err != nil {
		return nil, err
	}
	return genBlock, nil
}

// e_initLtHash reads or creates the LtHash lattice state digest.
func e_initLtHash(ctx context.Context, db kv.RwDB) *lthash.Digest {
	var ltDigest *lthash.Digest
	if rtx, err := db.BeginRo(ctx); err == nil {
		ltDigest, _ = lthash.ReadLtHashDigest(rtx, modules.LtHashDigest)
		rtx.Rollback()
	}
	if ltDigest == nil {
		ltDigest = lthash.New()
	}
	return ltDigest
}

func WriteChainConfig(db kv.RwTx, genesisHash types.Hash, genesis *conf.Genesis) error {
	if err := rawdb.WriteChainConfig(db, genesisHash, genesis.Config); err != nil {
		log.Error("failed to write chain config", "err", err)
		return err
	}
	return nil
}

func SplitTagsFlag(tagsFlag string) map[string]string {
	tags := strings.Split(tagsFlag, ",")
	tagsMap := map[string]string{}

	for _, t := range tags {
		if t != "" {
			kv := strings.Split(t, "=")

			if len(kv) == 2 {
				tagsMap[kv[0]] = kv[1]
			}
		}
	}

	return tagsMap
}

// historyBackfillInterval is how often the deferred history backfiller folds
// the changesets behind the head into the index: N42_HISTORY_INDEX_INTERVAL
// (a Go duration), default 2 s. Each fold rewrites the index chunk of every
// account touched since the last one, so a full block's ~23k hot accounts
// cost ~25 MB of payload and ~106 MB of dirty pages under the single MDBX
// writer per fold; at one fold per block that writer was busy enough late
// in a leg that block writes waited up to 2.4 s to begin (round 35zzi).
// Folding every N seconds rewrites each chunk once for all the blocks in
// between.
func historyBackfillInterval() time.Duration {
	if v := os.Getenv("N42_HISTORY_INDEX_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		log.Warn("N42_HISTORY_INDEX_INTERVAL ignored (not a positive duration)", "value", v)
	}
	return 2 * time.Second
}
