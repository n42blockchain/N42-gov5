// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
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
	cfg   ConfigV2
	srcDB kv.RwDB
	dstDB kv.RwDB
	stats *Stats
	log   log2.Logger
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

	return &EngineV2{
		cfg:   cfg,
		stats: NewStats(),
		log:   log2.New("module", "replay-v2"),
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
		data, err := tx.GetOne(jmtstore.JMTRootTable, []byte("replay_src_height"))
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

	e.log.Info("replay-v2 complete",
		"blocks", e.stats.BlocksProcessed.Load(),
		"txReplayed", e.stats.TxReplayed.Load(),
		"txSkipped", e.stats.TxSkipped.Load(),
		"elapsed", e.stats.Elapsed(),
	)
	return e.stats, nil
}

// processBatchV2 reads a range of source blocks, replays them with JMT+LtHash
// commitment tracking, and writes the resulting chain to the target DB.
// One write transaction covers the entire batch for efficiency.
func (e *EngineV2) processBatchV2(ctx context.Context, from, to uint64) error {
	return e.dstDB.Update(ctx, func(dstTx kv.RwTx) error {
		// Initialize JMT tree backed by the target DB.
		nodeStore := jmtstore.NewMDBXStore(dstTx, jmtstore.JMTNodeTable)
		jmtRoot, err := jmtstore.ReadJMTRoot(dstTx)
		if err != nil {
			return fmt.Errorf("read JMT root: %w", err)
		}

		var tree *jmt.Tree
		if jmtRoot == jmt.EmptyHash {
			tree = jmt.New(nodeStore)
		} else {
			tree = jmt.NewFromRoot(nodeStore, jmtRoot)
		}

		jmtCommit := commitment.NewJMTCommitment(tree)

		// Initialize LtHash digest (resume from persisted or start fresh).
		ltDigest, err := lthash.ReadLtHashDigest(dstTx, "LtHashDigest")
		if err != nil {
			return fmt.Errorf("read LtHash digest: %w", err)
		}
		ltCommit := commitment.NewLtHashCommitment(ltDigest)

		// Set up the root computer so IntermediateRoot() flows through JMT+LtHash.
		jmtRC := commitment.NewJMTRootComputer(jmtCommit)
		ltRC := commitment.NewLtHashAwareRootComputer(jmtRC, ltCommit)

		// Track the running new-chain block number. For resume, this equals
		// the JMT version + 1. For fresh start, it starts at from.
		newBlockNum := from
		ver, _ := jmtstore.ReadJMTVersion(dstTx)
		if ver > 0 && ver >= from {
			newBlockNum = ver + 1
		}

		// Read previous block hash and timestamp from the TARGET chain's last
		// block (newBlockNum-1), NOT from the source block number. Gap filling
		// makes the target chain longer than the source, so block numbers diverge.
		parentHash, prevTime, err := e.readParentInfo(dstTx, newBlockNum)
		if err != nil {
			return fmt.Errorf("read parent info at target block %d: %w", newBlockNum-1, err)
		}

		// Genesis initialization: if starting from block 0 or 1 with an empty tree.
		if from <= 1 && jmtRoot == jmt.EmptyHash {
			r := state.NewPlainStateReader(dstTx)
			w := state.NewPlainStateWriterNoHistory(dstTx)
			ibs := state.New(r)

			InitGenesisState(ibs)
			rules := e.cfg.ChainConfig.Rules(0)
			if err := ibs.FinalizeTx(rules, w); err != nil {
				return fmt.Errorf("genesis finalize: %w", err)
			}
			e.log.Info("genesis state initialized",
				"hardforkAllocs", len(DefaultHardForkAllocs()),
				"systemContracts", len(DefaultSystemContracts()))
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
						gapJmtRoot := jmtCommit.Root()
						gapLtRoot := ltCommit.Root()
						gapHeader := &block.Header{
							ParentHash: parentHash,
							Number:     uint256.NewInt(newBlockNum),
							Time:       gapTime,
							Root:       gapJmtRoot,
							GasLimit:   30_000_000,
							BaseFee:    uint256.NewInt(params.InitialBaseFee),
							LtHashRoot: gapLtRoot,
						}
						gapBlk := block.NewBlock(gapHeader, nil)
						if writeErr := e.writeNewBlock(dstTx, gapBlk.(*block.Block), newBlockNum); err != nil {
							return writeErr
						}

						parentHash = gapBlk.Hash()
						newBlockNum++
					}
				}

				// Replay the source block's transactions.
				r := state.NewPlainStateReader(dstTx)
				w := state.NewPlainStateWriterNoHistory(dstTx)
				ibs := state.New(r)
				ibs.SetRootComputer(ltRC)

				replayedTxs := e.replayBlockTxs(ibs, srcBlk, num)

				// Finalize state changes via the writer.
				rules := e.cfg.ChainConfig.Rules(num)
				if err := ibs.FinalizeTx(rules, w); err != nil {
					return fmt.Errorf("finalize block %d: %w", num, err)
				}

				// Compute JMT + LtHash roots via IntermediateRoot, which
				// flows through the LtHashAwareRootComputer.
				jmtStateRoot := ibs.IntermediateRoot()
				ltStateRoot := ibs.LtHashRoot()

				// Build the new block header.
				newHeader := &block.Header{
					ParentHash: parentHash,
					Number:     uint256.NewInt(newBlockNum),
					Time:       srcTime,
					Root:       jmtStateRoot,
					GasLimit:   30_000_000,
					BaseFee:    uint256.NewInt(params.InitialBaseFee),
					LtHashRoot: ltStateRoot,
				}

				var newBlk *block.Block
				if len(replayedTxs) > 0 {
					newBlk = block.NewBlock(newHeader, replayedTxs).(*block.Block)
				} else {
					newBlk = block.NewBlock(newHeader, nil).(*block.Block)
					e.stats.BlocksEmpty.Add(1)
				}

				if writeErr := e.writeNewBlock(dstTx, newBlk, newBlockNum); err != nil {
					return writeErr
				}

				parentHash = newBlk.Hash()
				prevTime = srcTime
				newBlockNum++
				e.stats.BlocksProcessed.Add(1)
			}

			// End of batch: flush JMT nodes and persist checkpoints.
			if err := jmtCommit.Flush(); err != nil {
				return fmt.Errorf("JMT flush: %w", err)
			}

			lastVersion := newBlockNum - 1
			tree.SetVersion(lastVersion)

			if err := jmtstore.WriteJMTRoot(dstTx, tree.Root()); err != nil {
				return fmt.Errorf("write JMT root: %w", err)
			}
			if err := jmtstore.WriteJMTVersion(dstTx, lastVersion); err != nil {
				return fmt.Errorf("write JMT version: %w", err)
			}
			// Record per-block version→root mapping for the batch endpoint.
			jRoot := jmtCommit.Root()
			if err := jmtstore.WriteJMTVersionRoot(dstTx, lastVersion, jmt.Hash(jRoot)); err != nil {
				return fmt.Errorf("write JMT version root: %w", err)
			}

			// Persist LtHash digest.
			if err := lthash.WriteLtHashDigest(dstTx, "LtHashDigest", ltCommit.Digest()); err != nil {
				return fmt.Errorf("write LtHash digest: %w", err)
			}

			// Record the source chain height for resume (distinct from new chain
			// height which includes gap-fill blocks).
			var srcBuf [8]byte
			binary.BigEndian.PutUint64(srcBuf[:], to)
			if err := dstTx.Put(jmtstore.JMTRootTable, []byte("replay_src_height"), srcBuf[:]); err != nil {
				return fmt.Errorf("write replay src height: %w", err)
			}

			e.log.Info("batch committed",
				"srcFrom", from, "srcTo", to,
				"newChainHead", lastVersion,
			)
			return nil
		})
	})
}

// replayBlockTxs filters and replays the transactions from a source block
// using lossy replay (simple value transfers + contract deployments).
// Returns the list of replayed transactions.
func (e *EngineV2) replayBlockTxs(ibs *state.IntraBlockState, srcBlk *block.Block, num uint64) []*transaction.Transaction {
	txs := srcBlk.Transactions()
	if len(txs) == 0 {
		return nil
	}

	blockNum := new(big.Int).SetUint64(num)
	var replayed []*transaction.Transaction

	for _, tx := range txs {
		e.stats.TxTotal.Add(1)

		// Filter: deposit contract.
		if to := tx.To(); to != nil && e.cfg.SkipAddresses[*to] {
			e.stats.TxSkipped.Add(1)
			e.stats.skipReason("deposit_contract")
			continue
		}

		// Filter: empty contract creation.
		if tx.To() == nil && len(tx.Data()) == 0 {
			e.stats.TxSkipped.Add(1)
			e.stats.skipReason("empty_create")
			continue
		}

		// Recover sender. Try old chain ID (94) first since source data uses that.
		signer := transaction.MakeSigner(&params.ChainConfig{ChainID: big.NewInt(94)}, blockNum)
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			// Fallback: try new chain config signer.
			newSigner := transaction.MakeSigner(e.cfg.ChainConfig, blockNum)
			from, err = transaction.Sender(newSigner, tx)
			if err != nil {
				e.stats.TxSkipped.Add(1)
				e.stats.skipReason("sender_unknown")
				continue
			}
		}

		// Execute the transaction via simple transfer (lossy: no full EVM).
		value := tx.Value()
		if value != nil && !value.IsZero() {
			senderBal := ibs.GetBalance(from)
			if senderBal.Cmp(value) >= 0 {
				ibs.SubBalance(from, value)
				if tx.To() != nil {
					ibs.AddBalance(*tx.To(), value)
				}
			}
		}

		// Track nonce.
		ibs.SetNonce(from, ibs.GetNonce(from)+1)

		// Contract deployment: store code.
		if tx.To() == nil && len(tx.Data()) > 0 {
			contractAddr := crypto.CreateAddress(from, tx.Nonce())
			ibs.CreateAccount(contractAddr, true)
			ibs.SetCode(contractAddr, tx.Data())
		}

		replayed = append(replayed, tx)
		e.stats.TxReplayed.Add(1)
	}
	return replayed
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
