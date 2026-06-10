// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// EngineV2, the main driver of replay-v2. Streams blocks from the
// source database, re-executes them through internal/vm, updates JMT
// or BMT commitments, optionally records a leaf journal and writes
// results back via rawdb, layered kv and mdbx with LtHash verification.

package replay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/bmt"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/lthash"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Precomputed hashes for empty blocks.
var (
	emptyReceiptHash   = hash.DeriveSha(block.Receipts(nil))
	emptyTxHash        = hash.DeriveSha(transaction.Transactions(nil))
	emptyCodeHashValue = crypto.Keccak256Hash(nil)
	emptyUncleHash     = hash.EmptyUncleHash // rlpHash([]*Header(nil)) = 0x1dcc4de8...
	emptyRewardsHash   = hash.DeriveSha(block.Rewards(nil))
	emptyRequestsHash  = hash.EmptyRootHash // keccak256(RLP([])) = 0x56e81f17...
)

// ptrU64 returns a fresh *uint64 pointer. Avoids shared mutable state across headers.
func ptrU64(v uint64) *uint64 { return &v }

// copyHash returns a fresh *types.Hash pointer.
func copyHash(h types.Hash) *types.Hash { c := h; return &c }

// newDefaultExtra returns a fresh 32-byte zero slice for Header.Extra.
func newDefaultExtra() []byte { return make([]byte, 32) }

// EngineV2 is the replay-v2 engine that reads old chain blocks, filters
// transactions, fills timeline gaps, builds new headers with JMT+LtHash
// roots, and writes to a new database.
type EngineV2 struct {
	cfg           ConfigV2
	srcDB         kv.RwDB
	dstDB         kv.RwDB
	stats         *Stats
	log           log2.Logger
	replayCache   *layered.ShardedCache        // Erigon-style cross-batch state cache
	bmtTree       *bmt.Tree                    // retained after replay for final flush
	qmdbRC        *commitment.QMDBRootComputer // QMDB-twig computer (in-memory, persists across batches)
	mptRC         state.RootComputer           // MPT root computer (persists across batches)
	trieRC        *commitment.TrieRootComputer // CalcTrieRoot incremental (persists across batches)
	witnessReader *WitnessStateReader          // records per-block state access
	leafJournal   *LeafJournal                 // per-block leaf changes for tree building
	resealer      *BLSResealer                 // BLS mobile-voter re-seal (nil = disabled)
}

// NewEngineV2 creates a replay-v2 engine. Call Run() to execute.
func NewEngineV2(cfg ConfigV2) (*EngineV2, error) {
	if cfg.SourcePath == "" {
		return nil, fmt.Errorf("replay-v2: source path required")
	}
	if cfg.TargetPath == "" {
		return nil, fmt.Errorf("replay-v2: target path required")
	}
	if cfg.ChainConfig == nil {
		cfg.ChainConfig = params.MainnetChainConfig
	}
	if cfg.SkipAddresses == nil {
		cfg.SkipAddresses = DefaultSkipAddresses
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.FromBlock == 0 {
		cfg.FromBlock = 1
	}
	if cfg.GapPeriod == 0 {
		cfg.GapPeriod = 8
	}
	if cfg.GapTolerance == 0 {
		cfg.GapTolerance = 15
	}

	logger := log2.New("module", "replay-v2")

	// Write structured logs to file.
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			logger.SetHandler(log2.StreamHandler(f, log2.LogfmtFormat()))
		}
	} else {
		logger.SetHandler(log2.StderrHandler)
	}

	e := &EngineV2{
		cfg:   cfg,
		stats: NewStats(),
		log:   logger,
	}

	// Open leaf journal if configured.
	if cfg.LeafJournal != "" {
		if dir := filepath.Dir(cfg.LeafJournal); dir != "" {
			os.MkdirAll(dir, 0755)
		}
		lj, err := NewLeafJournal(cfg.LeafJournal)
		if err != nil {
			return nil, fmt.Errorf("open leaf journal: %w", err)
		}
		e.leafJournal = lj
		logger.Info("leaf journal enabled", "path", cfg.LeafJournal)
	}

	// Build the BLS mobile-voter re-sealer if enabled (derives the key pool).
	if cfg.BLSReseal {
		t0 := time.Now()
		rs, err := NewBLSResealer(BLSResealConfig{
			Seed:          cfg.BLSSeed,
			PoolSize:      cfg.BLSPoolSize,
			CommitteeSize: cfg.BLSCommitteeSize,
			RampBlocks:    cfg.BLSRampBlocks,
		})
		if err != nil {
			return nil, fmt.Errorf("init BLS resealer: %w", err)
		}
		e.resealer = rs
		logger.Info("BLS re-seal enabled",
			"pool", cfg.BLSPoolSize, "committee", cfg.BLSCommitteeSize,
			"rampBlocks", cfg.BLSRampBlocks, "deriveTime", time.Since(t0))
	}

	return e, nil
}

// Stats returns the current replay statistics.
func (e *EngineV2) Stats() *Stats { return e.stats }

// Close releases database resources. Call after Run() and RunPostExport().
func (e *EngineV2) Close() {
	if e.srcDB != nil {
		e.srcDB.Close()
	}
	if e.dstDB != nil {
		e.dstDB.Close()
	}
}

// Run executes the replay-v2 pipeline. It opens both databases, reads blocks
// from source, filters transactions, fills gaps, computes JMT+LtHash roots,
// and writes the new chain to the target database.
func (e *EngineV2) Run(ctx context.Context) (*Stats, error) {
	var err error

	// Initialize N42 table schema and register all tables (including JMTNode,
	// JMTRoot, JMTVersionRoots, LtHashDigest) into ChaindataTablesCfg so the
	// target DB creates them on first open.
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	// Open source DB with Accede mode — only opens tables that already exist,
	// silently skipping missing ones. Critical for old databases that lack
	// newer tables.
	e.srcDB, err = mdbx.NewMDBX(e.log).
		Path(e.cfg.SourcePath + "/chaindata").
		MapSize(2 * datasize.TB).
		Accede().
		Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("replay-v2: open source DB: %w", err)
	}
	// Source DB closed in Close(). Do NOT defer here — RunPostExport needs dstDB.

	// Open/create target DB with full table schema.
	e.dstDB, err = mdbx.NewMDBX(e.log).
		Path(e.cfg.TargetPath + "/chaindata").
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		MapSize(2 * datasize.TB).
		Open(ctx)
	if err != nil {
		e.srcDB.Close()
		return nil, fmt.Errorf("replay-v2: open target DB: %w", err)
	}

	// Detect end block from source if not specified.
	if e.cfg.ToBlock == 0 {
		if err := e.srcDB.View(ctx, func(tx kv.Tx) error {
			e.cfg.ToBlock = FindLatestBlock(tx)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	e.stats.FromBlock = e.cfg.FromBlock
	e.stats.ToBlock = e.cfg.ToBlock
	e.log.Info("replay-v2 starting", "from", e.cfg.FromBlock, "to", e.cfg.ToBlock)

	// Check for resume point in target DB. Use a dedicated key to track the
	// SOURCE chain height (not the new chain height, which includes gap-fill
	// blocks and may exceed the source block count).
	resumeBlock := uint64(0)
	if err := e.dstDB.View(ctx, func(tx kv.Tx) error {
		resumeTable := jmtstore.JMTRootTable
		if e.cfg.TreeType == "bmt" {
			resumeTable = bmtstore.BMTRootTable
		}
		data, err := tx.GetOne(resumeTable, []byte("replay_src_height"))
		if err != nil {
			return err
		}
		if data != nil && len(data) >= 8 {
			resumeBlock = binary.BigEndian.Uint64(data)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("replay-v2: read resume point: %w", err)
	}

	startBlock := e.cfg.FromBlock
	if resumeBlock > 0 && resumeBlock >= startBlock {
		startBlock = resumeBlock + 1
		e.log.Info("resuming from checkpoint", "lastSourceBlock", resumeBlock, "startBlock", startBlock)
	}

	if startBlock > e.cfg.ToBlock {
		e.log.Info("already complete", "lastSourceBlock", resumeBlock)
		return e.stats, nil
	}

	// Process in batches.
	for batchStart := startBlock; batchStart <= e.cfg.ToBlock; batchStart += uint64(e.cfg.BatchSize) {
		select {
		case <-ctx.Done():
			return e.stats, ctx.Err()
		default:
		}

		batchEnd := batchStart + uint64(e.cfg.BatchSize) - 1
		if batchEnd > e.cfg.ToBlock {
			batchEnd = e.cfg.ToBlock
		}

		if err := e.processBatchV2(ctx, batchStart, batchEnd); err != nil {
			return e.stats, fmt.Errorf("replay-v2: batch %d-%d: %w", batchStart, batchEnd, err)
		}

		if e.cfg.ProgressFn != nil {
			e.stats.CurrentBlock = batchEnd
			e.cfg.ProgressFn(batchEnd, e.cfg.ToBlock, e.stats.BlocksPerSecond())
		}
	}

	// Final flush: write current BMT tree nodes to MDBX (only the latest
	// state — ~35K nodes after prune). This is the complete tree that the
	// node loads at startup for consensus, block production, and proofs.
	if e.bmtTree != nil && e.bmtTree.DirtyLen() > 0 {
		flushStart := time.Now()
		nodeCount := e.bmtTree.DirtyLen()
		if err := e.dstDB.Update(ctx, func(tx kv.RwTx) error {
			bmtNodeStore := bmtstore.NewMDBXStore(tx, bmtstore.BMTNodeTable)
			return e.bmtTree.FlushTo(bmtNodeStore)
		}); err != nil {
			return e.stats, fmt.Errorf("final BMT flush: %w", err)
		}
		e.log.Info("final BMT flush",
			"nodes", nodeCount,
			"time", time.Since(flushStart).Round(time.Millisecond),
			"root", e.bmtTree.Root().Hex()[:16],
		)
	}

	// Log final MPT state (checkpoint already saved in last batch).
	if rc, ok := e.mptRC.(*commitment.MPTRootComputer); ok && rc != nil {
		if err := e.dstDB.View(ctx, func(tx kv.Tx) error {
			root, _ := commitment.ReadMPTRoot(tx)
			blockNum, _, _ := commitment.ReadMPTTrieState(tx)
			e.log.Info("MPT state persisted",
				"root", fmt.Sprintf("%x", root[:8]),
				"checkpoint", blockNum,
			)
			return nil
		}); err != nil {
			e.log.Warn("read MPT checkpoint failed", "err", err)
		}
	}

	// Close leaf journal.
	if e.leafJournal != nil {
		if err := e.leafJournal.Close(); err != nil {
			e.log.Warn("leaf journal close error", "err", err)
		}
		stat, _ := os.Stat(e.cfg.LeafJournal)
		if stat != nil {
			e.log.Info("leaf journal written", "path", e.cfg.LeafJournal,
				"size", fmt.Sprintf("%.1f MB", float64(stat.Size())/(1024*1024)))
		}
	}

	e.log.Info("replay-v2 complete",
		"blocks", e.stats.BlocksProcessed.Load(),
		"txReplayed", e.stats.TxReplayed.Load(),
		"txFailed", e.stats.TxFailed.Load(),
		"receiptMatch", e.stats.ReceiptMatch.Load(),
		"receiptMismatch", e.stats.ReceiptMismatch.Load(),
		"elapsed", e.stats.Elapsed(),
	)
	return e.stats, nil
}

// processBatchV2 reads a range of source blocks, replays them with JMT+LtHash
// commitment tracking, and writes the resulting chain to the target DB.
// One write transaction covers the entire batch for efficiency.
func (e *EngineV2) processBatchV2(ctx context.Context, from, to uint64) error {
	return e.dstDB.Update(ctx, func(dstTx kv.RwTx) error {
		// JMT/BMT + LtHash are optional. When disabled (--jmt=false), replay
		// only writes PlainState + block data — much faster for Phase A of
		// two-phase replay. Phase B uses migrate-jmt to build JMT from PlainState.
		var tree *jmt.Tree
		var nodeStore *jmtstore.MDBXStore
		var jmtCommit *commitment.JMTCommitment
		var bmtTree *bmt.Tree
		var bmtNodeStore *bmtstore.MDBXStore
		var bmtCommit *commitment.BMTCommitment
		var bmtRC state.RootComputer
		var ltCommit *commitment.LtHashCommitment
		var ltRC state.RootComputer
		var qmdbRC *commitment.QMDBRootComputer
		useBMT := e.cfg.TreeType == "bmt"
		useQMDB := e.cfg.TreeType == "qmdb"
		useMPT := e.cfg.TreeType == "mpt"
		useTrie := e.cfg.TreeType == "trie"
		var mptRC state.RootComputer
		var trieRC *commitment.TrieRootComputer

		// CalcTrieRoot incremental — independent of JMT flag.
		if useTrie {
			if e.trieRC == nil {
				e.trieRC = commitment.NewTrieRootComputer()
			}
			trieRC = e.trieRC
			trieRC.SetRwTx(dstTx)
		}

		if e.cfg.EnableJMT {
			switch {
			case useTrie:
				// Already initialized above.

			case useMPT:
				// MPT path — reuse across batches to preserve branch data and trie state.
				// Do NOT reset between batches — HPH state must be continuous for
				// correct incremental root computation.
				if e.mptRC == nil {
					rc := commitment.NewPersistentMPTRootComputer()
					// Restore trie state from previous batch/run if available.
					if rtx, err := e.dstDB.BeginRo(ctx); err == nil {
						if _, trieState, err := commitment.ReadMPTTrieState(rtx); err == nil && len(trieState) > 0 {
							_ = rc.RestoreTrieState(trieState)
						}
						rtx.Rollback()
					}
					e.mptRC = rc
				}
				mptRC = e.mptRC

			case useQMDB:
				// QMDB path — append-only twig forest (Blake3, position-addressed).
				// In-memory across batches; the root is history-dependent, so resume
				// reloads the exact forest from the persisted entry log (not a rebuild
				// from the key set). kv.RwTx satisfies qmdb.Putter/Getter directly.
				if e.qmdbRC == nil {
					rc := commitment.NewQMDBRootComputer()
					// Back the live-key index with MDBX (persistent, off-heap) BEFORE
					// LoadFrom so reload trusts the on-disk index + twig roots instead
					// of rescanning the whole entry log.
					rc.UseMDBXIndex(dstTx)
					if err := rc.LoadFrom(dstTx); err != nil {
						return fmt.Errorf("QMDB reload: %w", err)
					}
					rc.SetCold(dstTx)
					rc.EvictFlushed() // free the freshly-loaded window (already on disk)
					e.qmdbRC = rc
				}
				qmdbRC = e.qmdbRC
				// Re-point the cold reader and index at THIS batch's tx (the prior
				// batch's tx is rolled back); faults/index reads use committed data.
				qmdbRC.SetCold(dstTx)
				qmdbRC.SetIndexTx(dstTx)

			case useBMT:
				// BMT path — Binary Merkle Tree (Blake3, content-addressed)
				bmtNodeStore = bmtstore.NewMDBXStore(dstTx, bmtstore.BMTNodeTable)
				bmtRoot, err := bmtstore.ReadBMTRoot(dstTx)
				if err != nil {
					return fmt.Errorf("read BMT root: %w", err)
				}
				if bmtRoot == bmt.EmptyHash {
					bmtTree = bmt.New(bmtNodeStore)
				} else {
					bmtTree = bmt.NewFromRoot(bmtNodeStore, bmtRoot)
				}
				bmtCommit = commitment.NewBMTCommitment(bmtTree)
				bmtRC = commitment.NewBMTRootComputer(bmtCommit)

			default:
				// JMT path
				nodeStore = jmtstore.NewMDBXStore(dstTx, jmtstore.JMTNodeTable)
				jmtRoot, err := jmtstore.ReadJMTRoot(dstTx)
				if err != nil {
					return fmt.Errorf("read JMT root: %w", err)
				}
				if jmtRoot == jmt.EmptyHash {
					tree = jmt.New(nodeStore)
				} else {
					tree = jmt.NewFromRoot(nodeStore, jmtRoot)
				}
				jmtCommit = commitment.NewJMTCommitment(tree)

				if e.cfg.EnableLtHash {
					ltDigest, err := lthash.ReadLtHashDigest(dstTx, "LtHashDigest")
					if err != nil {
						return fmt.Errorf("read LtHash digest: %w", err)
					}
					ltCommit = commitment.NewLtHashCommitment(ltDigest)
					jmtRC := commitment.NewJMTRootComputer(jmtCommit)
					ltRC = commitment.NewLtHashAwareRootComputer(jmtRC, ltCommit)
				}
			}
		}

		// Track the running new-chain block number.
		// With gap filling, newBlockNum diverges from source block number.
		// Read target chain head from the replay metadata key.
		newBlockNum := from
		resumeTable := jmtstore.JMTRootTable
		if useBMT {
			resumeTable = bmtstore.BMTRootTable
		}
		if useQMDB {
			resumeTable = modules.QMDBMeta
		}
		if headData, _ := dstTx.GetOne(resumeTable, []byte("replay_chain_head")); len(headData) >= 8 {
			head := binary.BigEndian.Uint64(headData)
			if head > 0 && head >= from {
				newBlockNum = head + 1
			}
		}
		// Fallback: try tree-specific version tables.
		if newBlockNum == from {
			if ver, _ := bmtstore.ReadBMTVersion(dstTx); ver > 0 && ver >= from {
				newBlockNum = ver + 1
			} else if ver, _ := jmtstore.ReadJMTVersion(dstTx); ver > 0 && ver >= from {
				newBlockNum = ver + 1
			}
		}

		// Read previous block hash and timestamp from the TARGET chain's last
		// block (newBlockNum-1), NOT from the source block number. Gap filling
		// makes the target chain longer than the source, so block numbers diverge.
		parentHash, prevTime, err := e.readParentInfo(dstTx, newBlockNum)
		if err != nil {
			return fmt.Errorf("read parent info at target block %d: %w", newBlockNum-1, err)
		}

		// Genesis initialization: write full genesis alloc (2,322 accounts)
		// plus hard-fork allocs and system contracts — same as node sync from scratch.
		treeEmpty := true
		if useBMT {
			treeEmpty = bmtTree == nil || bmtTree.Root() == bmt.EmptyHash
		} else if useQMDB {
			treeEmpty = qmdbRC == nil || qmdbRC.Tree().LiveCount() == 0
		} else {
			treeEmpty = tree == nil || tree.Root() == jmt.EmptyHash
		}
		if from <= 1 && (treeEmpty || !e.cfg.EnableJMT) {
			genesis := internal.GenesisByChainName("mainnet_v2")
			if genesis != nil {
				// Extract APoS signers from source genesis Extra → inject into genesis.Miners.
				// buildConsensusExtraData will produce the correct Extra for snapshot init.
				if err := e.srcDB.View(ctx, func(srcTx kv.Tx) error {
					srcGenesis, _ := rawdb.ReadBlockByNumber(srcTx, 0)
					if srcGenesis == nil {
						return nil
					}
					srcHdr := srcGenesis.Header().(*block.Header)
					if len(srcHdr.Extra) < 97 {
						return nil
					}
					signerBytes := srcHdr.Extra[32 : len(srcHdr.Extra)-65]
					n := len(signerBytes) / 20
					miners := make([]string, n)
					for i := 0; i < n; i++ {
						miners[i] = fmt.Sprintf("0x%x", signerBytes[i*20:(i+1)*20])
					}
					// Append the deterministic test signer for single-node mining.
					// Key: 46421e0087590765d2eba920834caa9ada08e30bb9aa0e6f54b16a3f57a5630a
					miners = append(miners, "0x05057eb3f374ecd59a1368e85f704a2211963546")
					genesis.Miners = miners
					e.log.Info("genesis signers", "count", len(miners), "miners", miners)
					return nil
				}); err != nil {
					e.log.Warn("failed to read source genesis signers", "err", err)
				}

				gb := &internal.GenesisBlock{GenesisConfig: genesis}
				if _, _, err := gb.Write(dstTx); err != nil {
					return fmt.Errorf("write genesis: %w", err)
				}
				e.log.Info("genesis block written",
					"accounts", len(genesis.Alloc),
					"extraLen", len(genesis.ExtraData))

				// Write genesis ConsensusEvidence to MDBX (block 0).
				genHash, _ := rawdb.ReadCanonicalHash(dstTx, 0)
				genCE := &rawdb.ConsensusEvidence{
					BlockHash:     genHash,
					SignerCount:   1,
					SignersPacked: []byte{0x01},
				}
				if err := rawdb.WriteConsensusEvidence(dstTx, 0, genCE); err != nil {
					return fmt.Errorf("write genesis consensus evidence: %w", err)
				}
			}
			// Also apply hard-fork allocs and system contracts on top.
			r := state.NewPlainStateReader(dstTx)
			w := state.NewPlainStateWriterNoHistory(dstTx)
			ibs := state.New(r)
			InitGenesisState(ibs)
			rules := e.cfg.ChainConfig.Rules(0)
			if err := ibs.FinalizeTx(rules, w); err != nil {
				return fmt.Errorf("genesis finalize: %w", err)
			}

			// MPT: feed all genesis accounts into HPH so the trie contains
			// the full state from block 0.
			if useMPT && mptRC != nil {
				if rc, ok := mptRC.(*commitment.MPTRootComputer); ok {
					rc.SetStateReader(commitment.NewPlainStateMPTReader(dstTx))
				}
				genesisAccounts, genesisStorage, err := e.readAllState(dstTx)
				if err != nil {
					return fmt.Errorf("read genesis state for MPT: %w", err)
				}
				if root, err := mptRC.ComputeRoot(genesisAccounts, genesisStorage); err != nil {
					return fmt.Errorf("MPT genesis root: %w", err)
				} else {
					e.log.Info("MPT genesis state loaded",
						"accounts", len(genesisAccounts),
						"storage", len(genesisStorage),
						"root", fmt.Sprintf("%x", root[:8]),
					)
				}
			}

			// Trie: populate HashedAccounts/Storage from genesis PlainState,
			// then compute initial root via full CalcTrieRoot (no dirty keys).
			if useTrie && trieRC != nil {
				if err := trieRC.InitFromPlainState(); err != nil {
					return fmt.Errorf("trie genesis init: %w", err)
				}
				// Compute root with empty dirty set — CalcTrieRoot reads HashedAccounts directly.
				emptyAccts := map[types.Address]*account.StateAccount{}
				emptyStor := map[types.Address]map[types.Hash]*uint256.Int{}
				if root, err := trieRC.ComputeRoot(emptyAccts, emptyStor); err != nil {
					return fmt.Errorf("trie genesis root: %w", err)
				} else {
					e.log.Info("Trie genesis state loaded",
						"root", fmt.Sprintf("%x", root[:8]),
					)
				}
			}

			// JMT: feed all genesis accounts/storage into the JMT tree (and the
			// LtHash digest) so the tree holds the full state from block 0. Without
			// this the JMT only ever sees per-block dirty accounts and omits every
			// untouched genesis account → state root diverges from a full rebuild.
			// Mirrors the MPT/Trie seeding above. Persisted by the per-batch
			// tree.FlushTo(nodeStore) at the end of the block loop.
			if jmtCommit != nil {
				genesisAccounts, genesisStorage, gerr := e.readAllState(dstTx)
				if gerr != nil {
					return fmt.Errorf("read genesis state for JMT: %w", gerr)
				}
				tree.SetVersion(0)
				var jmtRoot types.Hash
				if ltRC != nil {
					lr, ok := ltRC.(state.LtHashRootComputer)
					if !ok {
						return fmt.Errorf("JMT genesis: ltRC missing LtHashRootComputer")
					}
					emptyOrigAcc := map[types.Address]*account.StateAccount{}
					emptyOrigStor := map[types.Address]map[types.Hash]*uint256.Int{}
					r, _, e2 := lr.ComputeRootWithOriginals(genesisAccounts, emptyOrigAcc, genesisStorage, emptyOrigStor)
					if e2 != nil {
						return fmt.Errorf("JMT+LtHash genesis root: %w", e2)
					}
					jmtRoot = r
				} else {
					r, e2 := commitment.NewJMTRootComputer(jmtCommit).ComputeRoot(genesisAccounts, genesisStorage)
					if e2 != nil {
						return fmt.Errorf("JMT genesis root: %w", e2)
					}
					jmtRoot = r
				}
				e.log.Info("JMT genesis state loaded",
					"accounts", len(genesisAccounts),
					"storage", len(genesisStorage),
					"root", fmt.Sprintf("%x", jmtRoot[:8]),
				)
			}

			// BMT: feed all genesis accounts/storage into the BMT so it holds the
			// full state from block 0 (same genesis-seeding fix as MPT/Trie/JMT).
			// Without this the BMT omits untouched genesis accounts and the root
			// diverges from a full rebuild.
			if useBMT && bmtRC != nil {
				genesisAccounts, genesisStorage, gerr := e.readAllState(dstTx)
				if gerr != nil {
					return fmt.Errorf("read genesis state for BMT: %w", gerr)
				}
				if root, e2 := bmtRC.ComputeRoot(genesisAccounts, genesisStorage); e2 != nil {
					return fmt.Errorf("BMT genesis root: %w", e2)
				} else {
					e.log.Info("BMT genesis state loaded",
						"accounts", len(genesisAccounts),
						"storage", len(genesisStorage),
						"root", fmt.Sprintf("%x", root[:8]),
					)
				}
			}

			// QMDB: seed the twig forest with the full genesis state (same
			// genesis-seeding requirement as MPT/Trie/JMT/BMT).
			if useQMDB && qmdbRC != nil {
				genesisAccounts, genesisStorage, gerr := e.readAllState(dstTx)
				if gerr != nil {
					return fmt.Errorf("read genesis state for QMDB: %w", gerr)
				}
				if root, e2 := qmdbRC.ComputeRoot(genesisAccounts, genesisStorage); e2 != nil {
					return fmt.Errorf("QMDB genesis root: %w", e2)
				} else {
					e.log.Info("QMDB genesis state loaded",
						"accounts", len(genesisAccounts),
						"storage", len(genesisStorage),
						"root", fmt.Sprintf("%x", root[:8]),
					)
				}
			}
		}

		// Set MPT StateReader once per batch (dstTx is constant within batch).
		if useMPT && mptRC != nil {
			if rc, ok := mptRC.(*commitment.MPTRootComputer); ok {
				rc.SetStateReader(commitment.NewPlainStateMPTReader(dstTx))
			}
		}
		// Update TrieRC tx per batch.
		if useTrie && trieRC != nil {
			trieRC.SetRwTx(dstTx)
		}

		return e.srcDB.View(ctx, func(srcTx kv.Tx) error {
			for num := from; num <= to; num++ {
				srcBlk, readErr := rawdb.ReadBlockByNumber(srcTx, num)
				if readErr != nil || srcBlk == nil {
					e.stats.BlocksMissing.Add(1)
					continue
				}

				srcHeader := srcBlk.Header().(*block.Header)
				srcTime := srcHeader.Time

				// Gap filling: insert empty blocks for timeline gaps.
				if e.cfg.FillGaps && prevTime > 0 {
					gapTimes := CalcGapBlockTimes(prevTime, srcTime, e.cfg.GapPeriod, e.cfg.GapTolerance)
					for _, gapTime := range gapTimes {
						var gapTreeRoot types.Hash
						if useMPT && mptRC != nil {
							// MPT: reuse last computed root (state unchanged for gap blocks).
							r, _ := mptRC.ComputeRoot(nil, nil)
							gapTreeRoot = r
						} else if useBMT && bmtCommit != nil {
							gapTreeRoot = bmtCommit.Root()
						} else if useQMDB && qmdbRC != nil {
							gapTreeRoot = qmdbRC.Root()
						} else if jmtCommit != nil {
							gapTreeRoot = jmtCommit.Root()
						}
						gapCE := &rawdb.ConsensusEvidence{
							SignerCount:   1,
							SignersPacked: []byte{0x01},
						}
						gapParentCEHash := parentConsensusEvidenceHash(dstTx, newBlockNum)
						gapPrevRandao := derivePrevRandao(dstTx, newBlockNum, parentHash)
						gapHeader := &block.Header{
							ParentHash:       parentHash,
							UncleHash:        emptyUncleHash,
							Number:           uint256.NewInt(newBlockNum),
							Time:             gapTime,
							Root:             gapTreeRoot,
							TxHash:           emptyTxHash,
							ReceiptHash:      emptyReceiptHash,
							Difficulty:       uint256.NewInt(0),
							GasLimit:         srcHeader.GasLimit,
							BaseFee:          srcHeader.BaseFee,
							Extra:            newDefaultExtra(),
							MixDigest:        gapPrevRandao,
							WithdrawalsHash:  copyHash(emptyRewardsHash),
							BlobGasUsed:      ptrU64(0),
							ExcessBlobGas:    ptrU64(0),
							ParentBeaconRoot: copyHash(gapParentCEHash),
							RequestsHash:     copyHash(emptyRequestsHash),
						}
						gapBlk := block.NewBlock(gapHeader, nil)
						gapCE.BlockHash = gapBlk.Hash()
						if e.resealer != nil {
							gapCE = e.resealer.BuildCE(newBlockNum, gapBlk.Hash(), emptyReceiptHash)
						}
						if err := rawdb.WriteConsensusEvidence(dstTx, newBlockNum, gapCE); err != nil {
							return fmt.Errorf("write gap consensus evidence %d: %w", newBlockNum, err)
						}
						if err := e.writeNewBlock(dstTx, gapBlk.(*block.Block), newBlockNum); err != nil {
							return err
						}

						parentHash = gapBlk.Hash()
						newBlockNum++
					}
				}

				// Erigon ShardedCache: cross-batch write-through for PlainState.
				if e.replayCache == nil {
					e.replayCache = layered.NewShardedCache(256, 512*1024)
				}
				// Rebuild reader chain per-block: PlainState(dstTx) → Cache → Witness.
				// dstTx changes per batch; witnessReader.Reset() clears per-block data.
				baseReader := state.NewPlainStateReader(dstTx)
				cachedReader := state.NewCachedStateReader(baseReader, e.replayCache)
				if e.witnessReader == nil {
					e.witnessReader = NewWitnessStateReader(cachedReader)
				} else {
					e.witnessReader.inner = cachedReader
					e.witnessReader.Reset()
				}
				// State writer: PlainState + ChangeSet + cache write-through.
				csWriter := state.NewPlainStateWriter(dstTx, dstTx, newBlockNum)
				w := state.NewCachedStateWriter(csWriter, e.replayCache)
				ibs := state.New(e.witnessReader)
				if useTrie && trieRC != nil {
					ibs.SetRootComputer(trieRC)
				} else if useMPT && mptRC != nil {
					// StateReader set once per batch above.
					ibs.SetRootComputer(mptRC)
				} else if useBMT && bmtRC != nil {
					ibs.SetRootComputer(bmtRC)
				} else if useQMDB && qmdbRC != nil {
					ibs.SetRootComputer(qmdbRC)
				} else if ltRC != nil {
					ibs.SetRootComputer(ltRC)
				}

				// EIP-4788: feed the parent consensus evidence hash (the re-sealed
				// BLS QC commitment) into the beacon-roots contract before tx
				// execution, so the consensus commitment is part of EVM state.
				// Self-gates on Cancun being active in the target chain config.
				if e.resealer != nil {
					pbr := parentConsensusEvidenceHash(dstTx, newBlockNum)
					ebrHeader := &block.Header{Number: uint256.NewInt(newBlockNum), Time: srcTime}
					// Persist the beacon-roots writes through the real writer (w):
					// the engine flushes block state via FinalizeTx (journal-based),
					// and ProcessBeaconBlockRoot's internal FinalizeTx clears the
					// journal, so without this the writes reach the JMT (still-dirty
					// storage at IntermediateRoot) but never the plain Storage table.
					if err := internal.ProcessBeaconBlockRootWithWriter(&pbr, e.cfg.ChainConfig, ibs, ebrHeader, w); err != nil {
						return fmt.Errorf("eip4788 block %d: %w", newBlockNum, err)
					}
				}

				replayedTxs, receipts, usedGas := e.replayBlockTxs(ibs, w, srcBlk, srcHeader, dstTx)

				// Apply block rewards from source block's Body.Rewards.
				// This replicates what consensus.Finalize() does without needing
				// the full consensus engine or deposit contract queries.
				srcBody, _ := srcBlk.Body().(*block.Body)
				if srcBody != nil {
					for _, reward := range srcBody.Rewards {
						if reward.Amount != nil && !reward.Amount.IsZero() {
							ibs.AddBalance(reward.Address, reward.Amount)
						}
					}
				}

				// Finalize all state changes (tx + rewards) via the writer.
				rules := e.cfg.ChainConfig.Rules(num)
				if err := ibs.FinalizeTx(rules, w); err != nil {
					return fmt.Errorf("finalize block %d: %w", newBlockNum, err)
				}
				// Flush ChangeSets + History for this block.
				if err := csWriter.WriteChangeSets(); err != nil {
					return fmt.Errorf("write changesets block %d: %w", newBlockNum, err)
				}
				if err := csWriter.WriteHistory(); err != nil {
					return fmt.Errorf("write history block %d: %w", newBlockNum, err)
				}

				// Set tree version for JMT path (BMT is content-addressed, no version needed).
				if !useBMT && tree != nil {
					tree.SetVersion(newBlockNum)
				}

				// Compute state roots.
				var stateRoot types.Hash
				if e.cfg.EnableJMT || useTrie {
					stateRoot = ibs.IntermediateRoot()

					if (useMPT || useTrie) && e.cfg.VerifyMPT {
						verifyAccts, verifyStor, err := e.readAllState(dstTx)
						if err != nil {
							return fmt.Errorf("verify read state block %d: %w", newBlockNum, err)
						}
						freshRC := commitment.NewMPTRootComputer()
						freshRC.SetStateReader(commitment.NewPlainStateMPTReader(dstTx))
						verifyRoot, err := freshRC.ComputeRoot(verifyAccts, verifyStor)
						if err != nil {
							return fmt.Errorf("verify compute root block %d: %w", newBlockNum, err)
						}
						if verifyRoot != stateRoot {
							// Debug: compare dirty vs PlainState for first 3 mismatched accounts
							accts, stor := ibs.DirtyAccountData()
							var diff string
							for addr, enc := range accts {
								psAcct, ok := verifyAccts[addr]
								if !ok {
									diff += fmt.Sprintf("\n  %x: in dirty but not in PlainState", addr[:4])
									continue
								}
								var dirtyAcct account.StateAccount
								if enc != nil {
									dirtyAcct.DecodeForStorage(enc)
								}
								if dirtyAcct.Nonce != psAcct.Nonce || dirtyAcct.Balance.Cmp(&psAcct.Balance) != 0 || dirtyAcct.CodeHash != psAcct.CodeHash {
									diff += fmt.Sprintf("\n  %x: dirty(n=%d b=%s ch=%x) ps(n=%d b=%s ch=%x)",
										addr[:4], dirtyAcct.Nonce, dirtyAcct.Balance.String(), dirtyAcct.CodeHash[:4],
										psAcct.Nonce, psAcct.Balance.String(), psAcct.CodeHash[:4])
								}
							}
							_ = stor
							return fmt.Errorf("MPT ROOT MISMATCH at block %d: incremental=%x rebuild=%x accounts=%d storage=%d dirty=%d%s",
								newBlockNum, stateRoot, verifyRoot, len(verifyAccts), len(verifyStor), len(accts), diff)
						}
					}
				}

				// Write leaf changes to journal (plain keys, V2 format).
				if e.leafJournal != nil {
					accts, stor := ibs.DirtyAccountData()
					var entries []LeafEntry
					for addr, val := range accts {
						entries = append(entries, LeafEntry{
							Tag:     TagAccount,
							Address: addr,
							Value:   val,
						})
					}
					for addr, slots := range stor {
						for slot, val := range slots {
							entries = append(entries, LeafEntry{
								Tag:     TagStorage,
								Address: addr,
								Slot:    slot,
								Value:   val,
							})
						}
					}
					if err := e.leafJournal.WriteBlock(newBlockNum, entries); err != nil {
						return fmt.Errorf("write leaf journal block %d: %w", newBlockNum, err)
					}
				}

				// Compute receipt/tx roots and bloom (reuse existing hash infrastructure).
				receiptHash := hash.DeriveSha(block.Receipts(receipts))
				txHash := hash.DeriveSha(transaction.Transactions(replayedTxs))
				bloom := block.CreateBloom(receipts)

				// Compute rewards hash (withdrawalsRoot = hash of block rewards).
				var rewardsHash types.Hash
				if srcBody != nil && len(srcBody.Rewards) > 0 {
					rewardsHash = hash.DeriveSha(block.Rewards(srcBody.Rewards))
				} else {
					rewardsHash = emptyRewardsHash
				}

				// Build consensus evidence from source block's Extra (APoS seal).
				ce := buildConsensusEvidence(srcHeader)

				// parentBeaconBlockRoot = Blake3(parent ConsensusEvidence).
				parentCEHash := parentConsensusEvidenceHash(dstTx, newBlockNum)

				// prevRandao = Blake3(parent CE aggregate signature) or keccak256(parentHash).
				prevRandao := derivePrevRandao(dstTx, newBlockNum, parentHash)

				// Build header with ETH-standard fields. Extra = 32B vanity only.
				newHeader := &block.Header{
					ParentHash:       parentHash,
					UncleHash:        emptyUncleHash,
					Coinbase:         srcHeader.Coinbase,
					Root:             stateRoot,
					TxHash:           txHash,
					ReceiptHash:      receiptHash,
					Bloom:            bloom,
					Difficulty:       uint256.NewInt(0),
					Number:           uint256.NewInt(newBlockNum),
					GasLimit:         srcHeader.GasLimit,
					GasUsed:          usedGas,
					Time:             srcTime,
					Extra:            newDefaultExtra(),
					MixDigest:        prevRandao,
					BaseFee:          srcHeader.BaseFee,
					WithdrawalsHash:  copyHash(rewardsHash),
					BlobGasUsed:      ptrU64(0),
					ExcessBlobGas:    ptrU64(0),
					ParentBeaconRoot: copyHash(parentCEHash),
					RequestsHash:     copyHash(emptyRequestsHash),
				}

				var newBlk *block.Block
				if len(replayedTxs) > 0 {
					newBlk = block.NewBlock(newHeader, replayedTxs).(*block.Block)
				} else {
					newBlk = block.NewBlock(newHeader, nil).(*block.Block)
					e.stats.BlocksEmpty.Add(1)
				}

				// Write consensus evidence with actual block hash. When re-sealing,
				// replace the source-derived APoS evidence with a fresh
				// CommitteeSize-member BLS aggregate QC over this block.
				ce.BlockHash = newBlk.Hash()
				if e.resealer != nil {
					ce = e.resealer.BuildCE(newBlockNum, newBlk.Hash(), receiptHash)
				}
				if err := rawdb.WriteConsensusEvidence(dstTx, newBlockNum, ce); err != nil {
					return fmt.Errorf("write consensus evidence block %d: %w", newBlockNum, err)
				}

				if writeErr := e.writeNewBlock(dstTx, newBlk, newBlockNum); writeErr != nil {
					return writeErr
				}

				// Write receipts to target DB.
				if len(receipts) > 0 {
					if err := rawdb.WriteReceipts(dstTx, newBlockNum, receipts); err != nil {
						return fmt.Errorf("write receipts block %d: %w", newBlockNum, err)
					}
				}

				// Receipt hash verification: compare replay vs source.
				if srcHeader.ReceiptHash != (types.Hash{}) {
					if receiptHash == srcHeader.ReceiptHash {
						e.stats.ReceiptMatch.Add(1)
					} else {
						e.stats.ReceiptMismatch.Add(1)
					}
				}
				e.stats.GasUsedTotal.Add(usedGas)

				// Save per-block execution witness (input data stream for mobile SDK).
				var witnessKey [8]byte
				binary.BigEndian.PutUint64(witnessKey[:], newBlockNum)
				witnessData := e.witnessReader.Serialize()
				if witnessData == nil {
					witnessData = []byte{} // empty witness for blocks with no state access
				}
				if err := dstTx.Put(modules.BlockWitness, witnessKey[:], witnessData); err != nil {
					return fmt.Errorf("write block witness %d: %w", newBlockNum, err)
				}

				parentHash = newBlk.Hash()
				prevTime = srcTime
				newBlockNum++
				e.stats.BlocksProcessed.Add(1)

				// Periodic monitoring log every 100K blocks.
				if newBlockNum%100000 == 0 {
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					elapsed := time.Since(e.stats.StartTime)
					blkps := float64(e.stats.BlocksProcessed.Load()) / elapsed.Seconds()
					e.log.Info("replay progress",
						"block", newBlockNum,
						"pct", fmt.Sprintf("%.1f%%", float64(newBlockNum)/float64(e.cfg.ToBlock)*100),
						"blk/s", fmt.Sprintf("%.0f", blkps),
						"receiptOK", e.stats.ReceiptMatch.Load(),
						"receiptFail", e.stats.ReceiptMismatch.Load(),
						"txOK", e.stats.TxReplayed.Load(),
						"txFail", e.stats.TxFailed.Load(),
						"gasTotal", e.stats.GasUsedTotal.Load(),
						"heapMB", m.HeapAlloc/1024/1024,
						"cacheHitRate", func() string {
							if e.replayCache == nil {
								return "n/a"
							}
							h, m, _ := e.replayCache.Stats()
							if h+m == 0 {
								return "0%"
							}
							return fmt.Sprintf("%.1f%%", float64(h)/float64(h+m)*100)
						}(),
						"mptBranches", func() int {
							if rc, ok := e.mptRC.(*commitment.MPTRootComputer); ok {
								return rc.BranchCount()
							}
							return 0
						}(),
						"elapsed", elapsed.Round(time.Second),
					)
				}
			}

			// End of batch: write recovery metadata.
			lastVersion := newBlockNum - 1

			// Update head pointers — node startup reads these.
			if lastHash, err := rawdb.ReadCanonicalHash(dstTx, lastVersion); err == nil && lastHash != (types.Hash{}) {
				rawdb.WriteHeadBlockHash(dstTx, lastHash)
				rawdb.WriteHeadHeaderHash(dstTx, lastHash)
			}

			// Write target chain head for all modes (critical for gap-fill + AppendDup).
			var headBuf [8]byte
			binary.BigEndian.PutUint64(headBuf[:], lastVersion)
			if err := dstTx.Put(resumeTable, []byte("replay_chain_head"), headBuf[:]); err != nil {
				return fmt.Errorf("write replay chain head: %w", err)
			}

			if useBMT && bmtTree != nil {
				dirtyCount := bmtTree.DirtyLen()
				// Skip persisting BMT nodes to MDBX — root is computed in-memory,
				// headers already have the correct Root. Tree nodes can be rebuilt
				// later from PlainState. This eliminates the 15s/batch flush bottleneck.
				// Prune unreachable nodes to keep dirty map bounded.
				bmtTree.PruneDirty()
				e.bmtTree = bmtTree // retain for final flush
				e.log.Info("batch done",
					"blocks", fmt.Sprintf("%d-%d", from, lastVersion),
					"dirtyNodes", dirtyCount,
					"afterPrune", bmtTree.DirtyLen(),
					"bmtRoot", bmtTree.Root().Hex()[:16],
				)
				if err := bmtstore.WriteBMTRoot(dstTx, bmtTree.Root()); err != nil {
					return fmt.Errorf("write BMT root: %w", err)
				}
				if err := bmtstore.WriteBMTVersion(dstTx, lastVersion); err != nil {
					return fmt.Errorf("write BMT version: %w", err)
				}
			} else if useQMDB && qmdbRC != nil {
				// QMDB: persist the positional entry log + twig meta (sequential
				// appends) so a cross-process restart can reload the exact forest.
				// FlushTo also writes "root"/"nextSlot" into QMDBMeta.
				bytesW, ferr := qmdbRC.FlushTo(dstTx)
				if ferr != nil {
					return fmt.Errorf("QMDB flush: %w", ferr)
				}
				// Evict the just-flushed entries from RAM (recoverable from the cold
				// reader = this tx's persisted log). Bounds resident memory to the
				// unflushed window instead of O(history).
				qmdbRC.EvictFlushed()
				qRoot := qmdbRC.Root()
				var verBuf [8]byte
				binary.BigEndian.PutUint64(verBuf[:], lastVersion)
				if err := dstTx.Put(modules.QMDBMeta, []byte("version"), verBuf[:]); err != nil {
					return fmt.Errorf("write QMDB version: %w", err)
				}
				e.log.Info("batch done",
					"blocks", fmt.Sprintf("%d-%d", from, lastVersion),
					"qmdbRoot", qRoot.Hex()[:16],
					"liveKeys", qmdbRC.Tree().LiveCount(),
					"residentEntries", qmdbRC.Tree().ResidentEntries(),
					"residentTwigLeaves", qmdbRC.ResidentTwigLeaves(),
					"twigs", qmdbRC.Tree().NumTwigs(),
					"flushKB", bytesW/1024,
				)
			} else if tree != nil {
				if err := tree.FlushTo(nodeStore); err != nil {
					return fmt.Errorf("JMT flush: %w", err)
				}
				tree.SetVersion(lastVersion)
				if err := jmtstore.WriteJMTRoot(dstTx, tree.Root()); err != nil {
					return fmt.Errorf("write JMT root: %w", err)
				}
				if err := jmtstore.WriteJMTVersion(dstTx, lastVersion); err != nil {
					return fmt.Errorf("write JMT version: %w", err)
				}
			}
			// MPT: flush branches + trie state + root to MDBX per batch.
			if useMPT {
				if rc, ok := e.mptRC.(*commitment.MPTRootComputer); ok {
					// Get the latest state root from the last block header.
					lastBlockKey := modules.EncodeBlockNumber(lastVersion)
					lastHash, _ := dstTx.GetOne(modules.HeaderCanonical, lastBlockKey)
					var mptRoot types.Hash
					if len(lastHash) >= 32 {
						var hash types.Hash
						copy(hash[:], lastHash)
						if hdr := rawdb.ReadHeader(dstTx, hash, lastVersion); hdr != nil {
							mptRoot = hdr.StateRoot()
						}
					}
					if err := rc.SaveCheckpoint(dstTx, lastVersion, mptRoot); err != nil {
						return fmt.Errorf("MPT checkpoint: %w", err)
					}
					e.log.Info("MPT checkpoint saved",
						"block", lastVersion,
						"root", fmt.Sprintf("%x", mptRoot[:8]),
						"branches", rc.BranchCount(),
					)
				}
			}

			if ltCommit != nil {
				if err := lthash.WriteLtHashDigest(dstTx, "LtHashDigest", ltCommit.Digest()); err != nil {
					return fmt.Errorf("write LtHash digest: %w", err)
				}
			}

			// Record the source chain height for resume (distinct from new chain
			// height which includes gap-fill blocks).
			resumeTable := jmtstore.JMTRootTable
			if useBMT {
				resumeTable = bmtstore.BMTRootTable
			}
			if useQMDB {
				resumeTable = modules.QMDBMeta
			}
			if useMPT {
				resumeTable = modules.MPTRoot
			}
			var srcBuf [8]byte
			binary.BigEndian.PutUint64(srcBuf[:], to)
			if err := dstTx.Put(resumeTable, []byte("replay_src_height"), srcBuf[:]); err != nil {
				return fmt.Errorf("write replay src height: %w", err)
			}

			e.log.Info("batch committed",
				"srcFrom", from, "srcTo", to,
				"newChainHead", lastVersion,
				"txOK", e.stats.TxReplayed.Load(),
				"txFail", e.stats.TxFailed.Load(),
				"receiptOK", e.stats.ReceiptMatch.Load(),
				"receiptFail", e.stats.ReceiptMismatch.Load(),
			)
			// Write stats to file every batch.
			if e.cfg.StatsFile != "" {
				e.stats.CurrentBlock = to
				if data, err := json.MarshalIndent(e.stats, "", "  "); err == nil {
					os.WriteFile(e.cfg.StatsFile, data, 0644)
				}
			}
			return nil
		})
	})
}

// replayBlockTxs executes the source block's transactions through the full EVM.
// Uses the same ApplyTransaction path as sync and mining — no wheel reinvention.
// Returns the replayed transactions, receipts, and total gas used.
func (e *EngineV2) replayBlockTxs(
	ibs *state.IntraBlockState,
	stateWriter state.StateWriter,
	srcBlk *block.Block,
	srcHeader *block.Header,
	dstTx kv.Tx,
) ([]*transaction.Transaction, block.Receipts, uint64) {
	txs := srcBlk.Transactions()
	if len(txs) == 0 {
		return nil, nil, 0
	}

	gp := new(common.GasPool).AddGas(srcHeader.GasLimit)
	var (
		usedGas  uint64
		replayed []*transaction.Transaction
		receipts block.Receipts
	)

	// Block hash lookup for BLOCKHASH opcode — read from target chain.
	blockHashFunc := func(n uint64) types.Hash {
		h, _ := rawdb.ReadCanonicalHash(dstTx, n)
		return h
	}

	vmCfg := vm2.Config{}

	for i, tx := range txs {
		e.stats.TxTotal.Add(1)

		ibs.Prepare(tx.Hash(), srcBlk.Hash(), i)
		receipt, _, err := internal.ApplyTransaction(
			e.cfg.ChainConfig, blockHashFunc, nil, &srcHeader.Coinbase,
			gp, ibs, stateWriter, srcHeader, tx, &usedGas, vmCfg,
		)
		if err != nil {
			e.stats.TxFailed.Add(1)
			e.stats.skipReason("evm_error")
			// Log first few errors for diagnosis.
			if e.stats.TxFailed.Load() <= 10 {
				e.log.Warn("tx apply failed",
					"block", srcHeader.Number.Uint64(),
					"txIdx", i,
					"err", err,
				)
			}
			continue
		}
		replayed = append(replayed, tx)
		receipts = append(receipts, receipt)
		e.stats.TxReplayed.Add(1)
	}
	return replayed, receipts, usedGas
}

// readParentInfo reads the parent hash and timestamp for the block before `from`.
// If from==1 or from==0, returns the zero hash and zero timestamp (genesis).
// readAllState reads all accounts and storage from the database for feeding
// into MPT. This is used after genesis initialization to ensure the HPH trie
// contains the complete state (Erigon RebuildCommitmentFiles pattern).
func (e *EngineV2) readAllState(tx kv.Tx) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	error,
) {
	accounts := make(map[types.Address]*account.StateAccount)

	c, err := tx.Cursor(modules.Account)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()

	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, nil, err
		}
		if len(k) != 20 {
			continue
		}
		var acct account.StateAccount
		if err := acct.DecodeForStorage(v); err != nil {
			return nil, nil, fmt.Errorf("decode account %x: %w", k, err)
		}
		// Normalize empty CodeHash (protobuf may store zero hash).
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = emptyCodeHashValue
		}
		var addr types.Address
		copy(addr[:], k)
		cp := acct.SelfCopy()
		accounts[addr] = cp
	}

	storageMap := make(map[types.Address]map[types.Hash]*uint256.Int)

	sc, err := tx.Cursor(modules.Storage)
	if err != nil {
		return nil, nil, err
	}
	defer sc.Close()

	for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return nil, nil, err
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
		if storageMap[addr] == nil {
			storageMap[addr] = make(map[types.Hash]*uint256.Int)
		}
		storageMap[addr][slot] = val
	}

	return accounts, storageMap, nil
}

func (e *EngineV2) readParentInfo(dstTx kv.Tx, from uint64) (types.Hash, uint64, error) {
	if from <= 1 {
		return types.Hash{}, 0, nil
	}

	parentNum := from - 1
	h, err := rawdb.ReadCanonicalHash(dstTx, parentNum)
	if err != nil {
		return types.Hash{}, 0, err
	}
	if h == (types.Hash{}) {
		// No parent in target — this can happen if the target is empty.
		return types.Hash{}, 0, nil
	}

	parentBlk, err := rawdb.ReadBlockByNumber(dstTx, parentNum)
	if err != nil || parentBlk == nil {
		return h, 0, nil
	}
	parentHeader := parentBlk.Header().(*block.Header)
	return h, parentHeader.Time, nil
}

// buildConsensusEvidence extracts APoS data from source header Extra
// and builds a ConsensusEvidence for the MDBX table.
// Source Extra format: [32B vanity][N*20B signers][65B seal]
func buildConsensusEvidence(srcHeader *block.Header) *rawdb.ConsensusEvidence {
	ce := &rawdb.ConsensusEvidence{
		SignerCount:   1,
		SignersPacked: []byte{0x01},
	}
	extra := srcHeader.Extra
	if len(extra) < 97 {
		return ce
	}
	seal := extra[len(extra)-65:]
	copy(ce.AggregateSignature[:65], seal)
	signerBytes := extra[32 : len(extra)-65]
	n := len(signerBytes) / 20
	if n > 0 {
		ce.SignerCount = uint16(n)
		packedLen := (n + 7) / 8
		ce.SignersPacked = make([]byte, packedLen)
		for i := 0; i < n; i++ {
			ce.SignersPacked[i/8] |= 1 << (uint(i) % 8)
		}
	}
	return ce
}

// parentConsensusEvidenceHash returns Blake3(parent ConsensusEvidence).
func parentConsensusEvidenceHash(tx kv.Tx, blockNum uint64) types.Hash {
	if blockNum <= 1 {
		return types.Hash{}
	}
	parentCE, err := rawdb.ReadConsensusEvidence(tx, blockNum-1)
	if err != nil || parentCE == nil {
		return types.Hash{}
	}
	return types.Hash(blake3.Sum256(parentCE.Marshal()))
}

// derivePrevRandao computes prevRandao (MixDigest) for a block.
// If the parent block has a ConsensusEvidence with a real aggregate signature,
// use Blake3(aggregateSignature). Otherwise fall back to keccak256(parentHash).
func derivePrevRandao(tx kv.Tx, blockNum uint64, parentHash types.Hash) types.Hash {
	if blockNum <= 1 {
		return crypto.Keccak256Hash(parentHash[:])
	}
	parentCE, err := rawdb.ReadConsensusEvidence(tx, blockNum-1)
	if err != nil || parentCE == nil {
		return crypto.Keccak256Hash(parentHash[:])
	}
	// Check if aggregate signature is non-zero (real QC data).
	isZero := true
	for _, b := range parentCE.AggregateSignature {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return crypto.Keccak256Hash(parentHash[:])
	}
	return types.Hash(blake3.Sum256(parentCE.AggregateSignature[:]))
}

// writeNewBlock writes a newly constructed block to the target database.
func (e *EngineV2) writeNewBlock(dstTx kv.RwTx, blk *block.Block, num uint64) error {
	hash := blk.Hash()
	if err := rawdb.WriteBlock(dstTx, blk); err != nil {
		return fmt.Errorf("write block %d: %w", num, err)
	}
	if err := rawdb.WriteCanonicalHash(dstTx, hash, num); err != nil {
		return fmt.Errorf("write canonical hash %d: %w", num, err)
	}
	// TD = 0 for post-merge (PoS). N42 is PoS from genesis.
	if err := rawdb.WriteTd(dstTx, hash, num, uint256.NewInt(0)); err != nil {
		return fmt.Errorf("write td %d: %w", num, err)
	}
	return nil
}
