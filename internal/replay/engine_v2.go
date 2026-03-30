// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/bmt"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/lthash"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// EngineV2 is the replay-v2 engine that reads old chain blocks, filters
// transactions, fills timeline gaps, builds new headers with JMT+LtHash
// roots, and writes to a new database.
type EngineV2 struct {
	cfg         ConfigV2
	srcDB       kv.RwDB
	dstDB       kv.RwDB
	stats       *Stats
	log         log2.Logger
	replayCache   *layered.ShardedCache // Erigon-style cross-batch state cache
	bmtTree       *bmt.Tree            // retained after replay for final flush
	witnessReader *WitnessStateReader  // records per-block state access
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

	return &EngineV2{
		cfg:   cfg,
		stats: NewStats(),
		log:   logger,
	}, nil
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
		useBMT := e.cfg.TreeType == "bmt"

		if e.cfg.EnableJMT {
			if useBMT {
				// BMT path
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

				if e.cfg.EnableLtHash {
					ltDigest, err := lthash.ReadLtHashDigest(dstTx, "LtHashDigest")
					if err != nil {
						return fmt.Errorf("read LtHash digest: %w", err)
					}
					ltCommit = commitment.NewLtHashCommitment(ltDigest)
				}
			} else {
				// JMT path (default)
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

		// Track the running new-chain block number. For resume, this equals
		// the tree version + 1. For fresh start, it starts at from.
		newBlockNum := from
		if useBMT {
			ver, _ := bmtstore.ReadBMTVersion(dstTx)
			if ver > 0 && ver >= from {
				newBlockNum = ver + 1
			}
		} else {
			ver, _ := jmtstore.ReadJMTVersion(dstTx)
			if ver > 0 && ver >= from {
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
		} else {
			treeEmpty = tree == nil || tree.Root() == jmt.EmptyHash
		}
		if from <= 1 && (treeEmpty || !e.cfg.EnableJMT) {
			genesis := internal.GenesisByChainName("mainnet_v2")
			if genesis != nil {
				gb := &internal.GenesisBlock{GenesisConfig: genesis}
				if _, _, err := gb.WriteGenesisState(dstTx); err != nil {
					return fmt.Errorf("write genesis state: %w", err)
				}
				e.log.Info("genesis state initialized from alloc",
					"accounts", len(genesis.Alloc))
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
						if useBMT && bmtCommit != nil {
							gapTreeRoot = bmtCommit.Root()
						} else if jmtCommit != nil {
							gapTreeRoot = jmtCommit.Root()
						}
						// LtHashRoot is now encoded in Extra, not a Header field.
						// if ltCommit != nil { gapLtRoot = ltCommit.Root() }
						// Gap block header matches normal empty block format exactly.
						emptyReceiptHash := hash.DeriveSha(block.Receipts(nil))
						emptyTxHash := hash.DeriveSha(transaction.Transactions(nil))
						gapHeader := &block.Header{
							ParentHash:  parentHash,
							Number:      uint256.NewInt(newBlockNum),
							Time:        gapTime,
							Root:        gapTreeRoot,
							TxHash:      emptyTxHash,
							ReceiptHash: emptyReceiptHash,
							GasLimit:    srcHeader.GasLimit,
							BaseFee:     srcHeader.BaseFee,
							Coinbase:    srcHeader.Coinbase,
						}
						gapBlk := block.NewBlock(gapHeader, nil)
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
				if useBMT && bmtRC != nil {
					ibs.SetRootComputer(bmtRC)
				} else if ltRC != nil {
					ibs.SetRootComputer(ltRC)
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

				// Compute state roots (JMT+LtHash if enabled, zero otherwise).
				var stateRoot types.Hash
				if e.cfg.EnableJMT {
					stateRoot = ibs.IntermediateRoot()
					// LtHashRoot is now encoded in Extra, not a Header field.
					// if e.cfg.EnableLtHash { _ = ibs.LtHashRoot() }
				}

				// Compute receipt/tx roots and bloom (reuse existing hash infrastructure).
				receiptHash := hash.DeriveSha(block.Receipts(receipts))
				txHash := hash.DeriveSha(transaction.Transactions(replayedTxs))
				bloom := block.CreateBloom(receipts)

				// Build the new block header with ALL fields populated.
				newHeader := &block.Header{
					ParentHash:  parentHash,
					Number:      uint256.NewInt(newBlockNum),
					Time:        srcTime,
					Root:        stateRoot,
					TxHash:      txHash,
					ReceiptHash: receiptHash,
					Bloom:       bloom,
					GasUsed:     usedGas,
					GasLimit:    srcHeader.GasLimit,
					BaseFee:     srcHeader.BaseFee,
					Coinbase:    srcHeader.Coinbase,
				}

				var newBlk *block.Block
				if len(replayedTxs) > 0 {
					newBlk = block.NewBlock(newHeader, replayedTxs).(*block.Block)
				} else {
					newBlk = block.NewBlock(newHeader, nil).(*block.Block)
					e.stats.BlocksEmpty.Add(1)
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
						if e.replayCache == nil { return "n/a" }
						h, m, _ := e.replayCache.Stats()
						if h+m == 0 { return "0%" }
						return fmt.Sprintf("%.1f%%", float64(h)/float64(h+m)*100)
					}(),
						"elapsed", elapsed.Round(time.Second),
					)
				}
			}

			// End of batch: flush tree nodes + write recovery metadata.
			lastVersion := newBlockNum - 1
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

// writeNewBlock writes a newly constructed block to the target database.
func (e *EngineV2) writeNewBlock(dstTx kv.RwTx, blk *block.Block, num uint64) error {
	hash := blk.Hash()
	if err := rawdb.WriteBlock(dstTx, blk); err != nil {
		return fmt.Errorf("write block %d: %w", num, err)
	}
	if err := rawdb.WriteCanonicalHash(dstTx, hash, num); err != nil {
		return fmt.Errorf("write canonical hash %d: %w", num, err)
	}
	if err := rawdb.WriteTd(dstTx, hash, num, uint256.NewInt(num)); err != nil {
		return fmt.Errorf("write td %d: %w", num, err)
	}
	return nil
}

