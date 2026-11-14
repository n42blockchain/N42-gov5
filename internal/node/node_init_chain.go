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

// NewNode phases: profile, databases, genesis, chain config, P2P and the
// blockchain itself.

package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/exex"
	"github.com/n42blockchain/N42/internal/exex/extensions"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/txindexer"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/common/datadir"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// initProfile resolves the execution profile and the configured genesis.
func (n *Node) initProfile(b *nodeBuild) error {
	cfg := b.cfg
	var err error
	b.profile, err = params.ResolveExecutionProfile(cfg.NodeCfg.Profile)
	if err != nil {
		return err
	}
	cfg.NodeCfg.Profile = b.profile.String()

	b.configuredGenesis, err = resolveConfiguredGenesis(cfg, b.profile)
	if err != nil {
		return err
	}
	return nil
}

// initStorageTier applies the storage tier configuration before any database opens.
func (n *Node) initStorageTier(b *nodeBuild) error {
	cfg := b.cfg
	// Apply storage tier configuration before opening any databases.
	if cfg.StorageTierCfg.Enabled {
		dirs := datadir.New(cfg.NodeCfg.DataDir)
		if err := applyStorageTier(&cfg.StorageTierCfg, &dirs); err != nil {
			return fmt.Errorf("storage tier: %w", err)
		}
	}
	return nil
}

// initChainDB opens the chain database.
func (n *Node) initChainDB(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	b.chainKv, err = OpenDatabase(ctx, cfg, nil, kv.ChainDB.String())
	if err != nil {
		return err
	}
	return nil
}

// initFrameStore opens the optional frame store and frame log. The failure
// cleanup of the frame store is deferred by NewNode itself.
func (n *Node) initFrameStore(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	b.frameKv, err = openFrameStoreDB(ctx, cfg)
	if err != nil {
		return err
	}
	if b.frameKv != nil {
		rawdb.SetFrameStore(b.frameKv)
		if frameLogEnabled() {
			// N42_FRAME_LOG=1: frame data goes to an append-only log in
			// <datadir>/framelog; the frame store keeps its index.
			keep := 0
			if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("N42_FRAME_LOG_KEEP_SEGMENTS"))); err == nil && n > 0 {
				keep = n
				log.Warn("!!! FRAME LOG RETENTION ON (BENCH ONLY) !!! N42_FRAME_LOG_KEEP_SEGMENTS: older segments are "+
					"deleted; this node cannot serve or re-read older blocks", "keepSegments", n)
			}
			dir := filepath.Join(cfg.NodeCfg.DataDir, "framelog")
			if _, err := rawdb.OpenFrameLog(dir, keep); err != nil {
				rawdb.SetFrameStore(nil)
				b.frameKv.Close()
				return fmt.Errorf("opening the frame log: %w", err)
			}
			log.Info("Frame log open (N42_FRAME_LOG)", "path", dir)
		}
	}
	return nil
}

// initStoredGenesis reads the stored genesis and chain config and lets the
// compiled chainspec win for built-in chains.
func (n *Node) initStoredGenesis(b *nodeBuild) error {
	ctx := b.ctx
	var err error
	if err := b.chainKv.View(ctx, func(tx kv.Tx) error {
		// Chains converted with --virtual-td carry a DatabaseInfo marker instead
		// of per-block all-zero TD rows; ReadTd then synthesizes TD 0 for known
		// headers (and keeps nil => ErrUnknownAncestor for unknown ones).
		rawdb.SetupVirtualTdFromDB(tx)
		b.genesisHash, err = rawdb.ReadCanonicalHash(tx, 0)
		if b.genesisHash == (types.Hash{}) && err != nil {
			return internal.ErrGenesisNoConfig
		}
		if b.genesisHash == (types.Hash{}) {
			// No genesis stored yet; caller will write it below.
			return nil
		}

		b.chainConfig, err = rawdb.ReadChainConfig(tx, b.genesisHash)
		if err != nil {
			return err
		}

		if b.genesisBlock, err = rawdb.ReadBlockByHash(tx, b.genesisHash); b.genesisBlock == nil {
			return fmt.Errorf("genesisBlock is missing err:%w", err)
		}

		return nil
	}); err != nil {
		return err
	}

	// Built-in chains: the COMPILED chainspec is authoritative (erigon model) —
	// parameter and fork-schedule upgrades ship with the binary, and every node
	// of the network runs the same build. The stored copy remains authoritative
	// only for private/unknown chains (it is the sole record of their config).
	// Preferring the stored copy for built-ins silently froze every chainspec
	// change made after a datadir was seeded — observed live: a hotstuff
	// parameter added to the spec parsed correctly but never took effect,
	// because the copy written at seeding time kept winning on every start.
	if b.chainConfig != nil && b.configuredGenesis.chainConfig != nil && !b.configuredGenesis.isPrivate {
		b.chainConfig = b.configuredGenesis.chainConfig
	}
	return nil
}

// initDataDirLock acquires the instance directory lock.
func (n *Node) initDataDirLock(b *nodeBuild) error {
	cfg := b.cfg
	// Acquire the instance directory lock.
	if err := n.openDataDir(cfg); err != nil {
		return err
	}
	b.dirLockNode.dirLock = n.dirLock // track for cleanup on error
	return nil
}

// initGenesis writes the genesis on a fresh datadir, or validates the
// datadir's network binding on an existing one.
func (n *Node) initGenesis(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	if b.genesisHash == (types.Hash{}) {
		if err := ValidateDataDirNetworkBinding(cfg, b.configuredGenesis.chainConfig, b.configuredGenesis.genesisHash); err != nil {
			return err
		}
		if b.configuredGenesis.requiresExplicitInit {
			return explicitInitRequiredError(cfg.NodeCfg.Chain, cfg.NodeCfg.DataDir)
		}

		b.genesisConfig = b.configuredGenesis.genesis
		b.chainConfig = b.configuredGenesis.chainConfig
		if !b.configuredGenesis.isPrivate {
			b.genesisHash = *b.configuredGenesis.genesisHash
		}
		if err := b.chainKv.Update(ctx, func(tx kv.RwTx) error {
			var writeErr error
			b.genesisBlock, writeErr = WriteGenesisBlock(tx, b.genesisConfig)
			if writeErr != nil {
				return writeErr
			}
			if b.configuredGenesis.isPrivate {
				b.genesisHash = b.genesisBlock.Hash()
			}
			return nil
		}); err != nil {
			return err
		}
		if err := PersistDataDirNetworkBinding(cfg, b.chainConfig, b.genesisHash); err != nil {
			return err
		}
		if b.profile.IsEthereumEL() && b.genesisConfig != nil {
			ethGenesisHash, hashErr := internal.EthereumCompatibleGenesisHash(b.genesisConfig)
			if hashErr != nil {
				return hashErr
			}
			if err := PersistEthereumGenesisHash(cfg.NodeCfg.DataDir, ethGenesisHash); err != nil {
				return err
			}
		}
	} else {
		if err := ensureDataDirNetworkBinding(cfg, b.chainConfig, b.genesisHash); err != nil {
			return err
		}
	}
	return nil
}

// initChainConfig enforces state-scheme immutability and refreshes the
// stored chain config.
func (n *Node) initChainConfig(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	// Enforce StateScheme immutability: once written to the DB, it cannot change.
	if b.chainConfig != nil && b.chainConfig.StateScheme != "" {
		wantScheme := b.configuredGenesis.chainConfig.StateScheme
		if wantScheme == "" {
			wantScheme = "legacy-keccak"
		}
		if b.chainConfig.StateScheme != wantScheme {
			return fmt.Errorf("state scheme mismatch: db has %q, config wants %q — "+
				"changing the state commitment algorithm after genesis is not allowed",
				b.chainConfig.StateScheme, wantScheme)
		}
	}

	// Enforce StateScheme immutability: once written to the DB, it cannot change.
	if b.chainConfig != nil && b.chainConfig.StateScheme != "" {
		wantScheme := b.configuredGenesis.chainConfig.StateScheme
		if wantScheme == "" {
			wantScheme = "legacy-keccak"
		}
		if b.chainConfig.StateScheme != wantScheme {
			return fmt.Errorf("state scheme mismatch: db has %q, config wants %q — "+
				"changing the state commitment algorithm after genesis is not allowed",
				b.chainConfig.StateScheme, wantScheme)
		}
	}

	// Update ChainConfig on every startup for non-private chains.
	if !b.configuredGenesis.isPrivate {
		if err := b.chainKv.Update(ctx, func(tx kv.RwTx) error {
			b.genesisHash = *b.configuredGenesis.genesisHash
			b.genesisConfig = b.configuredGenesis.genesis
			if b.genesisConfig != nil {
				return WriteChainConfig(tx, b.genesisHash, b.genesisConfig)
			}
			if err := rawdb.WriteChainConfig(tx, b.genesisHash, b.configuredGenesis.chainConfig); err != nil {
				log.Error("failed to write chain config", "err", err)
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}

	cfg.ChainCfg = b.chainConfig
	return nil
}

// initP2P resolves the P2P genesis hash and creates the P2P service.
func (n *Node) initP2P(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	// Use the hardcoded genesis hash for P2P fork digest when a known chain is
	// configured. The actual genesis block hash may differ due to Header struct
	// evolution (e.g., added blob gas fields), but the chain data is identical.
	// This ensures fork digest compatibility with peers running older versions.
	b.p2pGenesisHash = b.genesisBlock.Hash()
	if canonicalHash, ok := b.configuredGenesis.canonicalHash(); ok {
		b.p2pGenesisHash = canonicalHash
	}
	// An explicit operator override wins over both the computed and the hardcoded
	// hash. Required to sync a deployed network whose on-wire genesis differs from
	// this binary's constant (see --p2p.genesis-override).
	if ov := strings.TrimSpace(cfg.P2PCfg.GenesisHashOverride); ov != "" {
		overrideHash := types.HexToHash(ov)
		log.Warn("Overriding P2P genesis hash for fork digest",
			"override", overrideHash.Hex(),
			"configured", b.p2pGenesisHash.Hex())
		b.p2pGenesisHash = overrideHash
	}
	b.p2p, err = p2p.NewService(ctx, b.p2pGenesisHash, cfg.P2PCfg, cfg.NodeCfg)
	if err != nil {
		return err
	}
	return nil
}

// initBlockChain resolves the consensus engine and creates the blockchain.
func (n *Node) initBlockChain(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	var err error
	b.engine, err = resolveConsensusEngine(cfg, b.profile, b.chainKv)
	if err != nil {
		return err
	}

	b.bc, err = internal.NewBlockChain(ctx, b.genesisBlock, b.engine, b.chainKv, b.p2p, cfg.ChainCfg)
	if err != nil {
		return fmt.Errorf("failed to create blockchain: %w", err)
	}

	// Initialize cross-block EVM code analysis cache (AIP-107 equivalent).
	// Caches codeBitmap() results across blocks, avoiding O(n) re-analysis
	// for contracts already seen. ~2x EVM execution speedup.
	vm2.GlobalCodeAnalysisCache = vm2.NewCodeAnalysisCache(8192)
	return nil
}

// initBlockChainFeatures enables parallel EVM, prefetch, the tx-lookup indexer,
// the era store and the ancient freezer on the concrete blockchain.
func (n *Node) initBlockChainFeatures(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	// Enable parallel EVM execution, state prefetching, and ancient DB if configured.
	if realBC, ok := b.bc.(*internal.BlockChain); ok {
		if cfg.NodeCfg.ParallelEVM {
			// EXPERIMENTAL. Off by default; only reachable via the config key,
			// cmd/n42 exposes no CLI flag for it.
			//
			// This warning used to assert that internal/parallel does NOT
			// consult the SELFDESTRUCT/CREATE2 storage-wipe set, so an
			// intra-block read of a slot on an account destroyed earlier in the
			// same block returned a stale pre-destruct value. That defect was
			// fixed and the warning was not updated. The wipe shadow-read is
			// present on all three sides: ParallelStateWriter.CreateContract
			// records a FieldStorageWipe marker (IntraBlockState calls it on
			// every SELFDESTRUCT, recreate-after-destruct and fresh CREATE),
			// ParallelStateReader.ReadAccountStorage shadows stale pre-wipe
			// slots with the version comparison, parallel_processor.go applies
			// the wipes on merge, and internal/parallel/wipe_shadow_test.go
			// pins the ordering cases. docs/ethel/erigon-borrow-audit.md
			// records the same conclusion.
			//
			// That defect being closed was never an assessment of the rest of
			// the path, and on 2026-09-02 the rest of the path was measured:
			// an A-B-A on the 7-node bench fleet (sequential / Block-STM /
			// sequential, one binary, the flag the only variable) HALTED THE
			// CHAIN on the middle leg. Six of seven nodes rejected the same two
			// blocks and the measurement window produced zero blocks, while
			// both sequential legs ran clean at ~36,500 TPS with zero bad
			// blocks and agreed with each other to 0.5% on execution cost.
			//
			// The failure is not a state root mismatch and not a storage-wipe
			// case:
			//
			//   could not apply tx 0 from block 13618674: insufficient funds
			//   for gas * price + value: address 0x8AD4..a6Db have 0 want
			//   210000000000001
			//
			// ELEVEN OF THE TWELVE rejections were on tx 0 -- the block's FIRST
			// transaction -- so no earlier transaction of the same block could
			// have credited the sender, and the balance must come from the
			// parent state. The cause is one level lower: ProcessParallel hands
			// every Block-STM worker the SAME state reader, noting only that it
			// "must be safe for concurrent reads". It is not. That reader is a
			// PlainStateReader over an MdbxTx, and MdbxTx.GetOne takes a
			// per-bucket cached cursor out of an unsynchronised map and calls
			// SeekExact on it, so every worker shares one MDBX cursor. The race
			// detector confirms it -- see TestGetOneIsNotConcurrencySafe in
			// lib/kv/mdbx, and docs/QS_TPS_BENCHMARK.md for the round.
			//
			// So this is not "unaudited", it is "known to diverge under load",
			// and the fix is not in internal/parallel: a parallel executor
			// needs a reader per worker, or reads serialised.
			log.Warn("ParallelEVM ENABLED: each Block-STM worker now owns its read transaction and reads the live QMDB tree under its lock (the 2026-09-02 halt was a shared MDBX cursor, 3709ca6a); UNMEASURED under load until a round says otherwise -- do not enable on a chain whose blocks matter",
				"workers", internal.ParallelWorkers())
			realBC.SetParallelEVM(true)
		}
		if cfg.NodeCfg.Prefetch {
			// Same defect as ParallelEVM above, in a feature that is not marked
			// experimental: StatePrefetcher.Prefetch runs NumCPU/2 goroutines
			// against the SAME state reader the executor is using, and that
			// reader is a PlainStateReader over one kv.Tx whose GetOne shares a
			// cached MDBX cursor. See internal/prefetcher.go for the analysis
			// and lib/kv/mdbx TestGetOneIsNotConcurrencySafe for the proof.
			//
			// Refuse outright when there is no shared cache to warm. Prefetch's
			// whole benefit path is that its reads populate the ShardedCache
			// the executor then reads through, and evmRecord wraps the reader
			// in a CachedStateReader only when layered.ExtractCache returns
			// non-nil -- which happens solely for a *layered.LayeredDB.
			// LayeredDBCfg.Enable defaults false, so on a normal datadir the
			// workers race the executor's cursor to fill a cache that does not
			// exist. A warning is the wrong response to a flag that can only
			// cost and never pay: fail fast, the way the mobileverify guard
			// does, and say which config key would make it coherent.
			if err := checkPrefetchHasCache(layered.ExtractCache(b.chainKv)); err != nil {
				return err
			}
			log.Warn("Prefetch ENABLED — its I/O workers share one state reader with the executor, which races on a single MDBX cursor (see internal/prefetcher.go). Not safe on a chain whose blocks matter")
			realBC.SetPrefetch(true)
			// Enable predictive slot prefetching alongside standard prefetching.
			realBC.SetPrefetchPredictor(internal.NewPrefetchPredictor(64))
		}
		// Transaction-lookup index for the newest blocks. Attached only if it
		// actually comes up: a chain with no indexer keeps writing the MDBX
		// table, whereas a chain that stopped writing it and has no working
		// indexer would leave its transactions findable nowhere.
		ix := txindexer.New(ctx, realBC, b.chainKv, filepath.Join(cfg.NodeCfg.DataDir, "txindex"))
		if ix.Start() {
			realBC.SetTxIndexer(ix)
			b.nodeTxIndexer = ix
		}
		// Era store (sealed ancient history, read-only). Auto-detected at
		// <datadir>/ancient-era; produced offline by the seal pipeline —
		// the node never writes it. Sealed ranges are served through the
		// rawdb fallback; damaged optional files degrade to pruned.
		if err := n.initEraStore(b); err != nil {
			return err
		}
		n.initAncientFreezer(b, realBC)
	}
	return nil
}

// initEraStore attaches the read-only sealed-history store when one exists.
func (n *Node) initEraStore(b *nodeBuild) error {
	cfg, ctx := b.cfg, b.ctx
	if eraDir := ancientera.DefaultDir(cfg.NodeCfg.DataDir); ancientera.Exists(eraDir) {
		eraStore, health, err := ancientera.OpenStore(eraDir)
		if err != nil {
			return fmt.Errorf("open era store %s: %w", eraDir, err)
		}
		for _, d := range health.Degraded {
			log.Warn("era store: optional range degraded to pruned", "range", d)
		}
		for _, g := range health.ChainGaps {
			log.Error("era store: CRITICAL chain-class damage — restore this era from a peer", "range", g)
		}
		// Cross-check the manifest anchor written by the seal pipeline.
		if err := b.chainKv.View(ctx, func(tx kv.Tx) error {
			anchor, aerr := tx.GetOne(modules.DatabaseInfo, []byte("eraManifestBlake3"))
			if aerr != nil || len(anchor) != 32 {
				return nil // no anchor recorded — informational only
			}
			mh, merr := ancientera.ManifestHash(eraDir)
			if merr == nil && !bytes.Equal(anchor, mh[:]) {
				log.Warn("era store: manifest differs from the anchored hash (rebuilt or edited manifest)")
			}
			return nil
		}); err != nil {
			return err
		}
		rawdb.SetAncientStore(eraStore)
		b.nodeEraStore = eraStore
		log.Info("era store attached", "dir", eraDir,
			"sealedEnd", health.SealedEnd, "sealed", health.Sealed,
			"pruned", health.Pruned, "degraded", len(health.Degraded))
	}
	return nil
}

// initAncientFreezer opens the ancient DB and starts its freeze loop.
func (n *Node) initAncientFreezer(b *nodeBuild, realBC *internal.BlockChain) {
	cfg, ctx := b.cfg, b.ctx
	if cfg.NodeCfg.AncientDB {
		ancientBase := cfg.NodeCfg.DataDir
		if cfg.StorageTierCfg.Enabled && cfg.StorageTierCfg.ColdPath != "" {
			ancientBase = cfg.StorageTierCfg.ColdPath
		}
		ancientPath := filepath.Join(ancientBase, "ancient")
		threshold := cfg.NodeCfg.AncientFreezeThreshold
		f, err := freezer.New(ancientPath, threshold)
		if err != nil {
			log.Warn("Failed to open ancient DB, continuing without freezer", "err", err)
		} else {
			realBC.SetFreezer(f)
			// Start background freeze goroutine.
			f.StartFreeze(ctx, func() uint64 {
				cur := realBC.CurrentBlock()
				if cur == nil {
					return 0
				}
				return blockNumberOrZero(cur)
			}, func(start, count uint64) (*freezer.FreezeData, error) {
				var data *freezer.FreezeData
				err := b.chainKv.View(ctx, func(tx kv.Tx) error {
					var e error
					data, e = rawdb.CollectFreezeData(tx, start, count)
					return e
				})
				return data, err
			}, func(start, count uint64) error {
				return b.chainKv.Update(ctx, func(tx kv.RwTx) error {
					return rawdb.CleanupFrozenData(tx, start, count)
				})
			})
		}
	}
}

// initExEx creates and attaches the Execution Extensions manager.
func (n *Node) initExEx(b *nodeBuild) error {
	// Create and attach the Execution Extensions (ExEx) manager.
	b.exexMgr = exex.NewManager(0)
	b.exexMgr.Register(&extensions.LogExtension{})
	if realBC, ok := b.bc.(*internal.BlockChain); ok {
		realBC.SetExExManager(b.exexMgr)
	}
	return nil
}
