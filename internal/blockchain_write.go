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
// BlockChain write helpers. Persists headers, bodies, receipts,
// state and canonical mappings through rawdb, promotes the chain
// head pointer under the insert lock and coordinates flushes with
// the freezer. Complements the read helpers in blockchain_reader.go.

package internal

// This file contains all block/state write methods for BlockChain.
// Read-only methods are in blockchain_reader.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	nodeMetrics "github.com/n42blockchain/N42/internal/metrics"
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/qmdb"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	verklestore "github.com/n42blockchain/N42/lib/verkle/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/lthash"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	statesnapshot "github.com/n42blockchain/N42/modules/state/snapshot"
)

// WriteBlockWithoutState persists a block without writing its state.
func (bc *BlockChain) WriteBlockWithoutState(blk block.IBlock) error {
	if bc.insertStopped() {
		return errInsertionInterrupted
	}
	concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
	if err != nil {
		return err
	}
	if _, err := requireBlockNumber(concreteBlock, "block number unavailable"); err != nil {
		return err
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteBlock(tx, concreteBlock)
	})
}

// writeBlockWithTd writes a block along with its total difficulty to the database.
// Used during sidechain insertion to ensure td is available for ReorgNeeded checks.
func (bc *BlockChain) writeBlockWithTd(blk block.IBlock, td *uint256.Int) error {
	if bc.insertStopped() {
		return errInsertionInterrupted
	}
	concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
	if err != nil {
		return err
	}
	blockNumber, err := requireBlockNumber(concreteBlock, "block number unavailable")
	if err != nil {
		return err
	}
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, concreteBlock); err != nil {
			return err
		}
		return rawdb.WriteTd(tx, blk.Hash(), blockNumber.Uint64(), td)
	}); err != nil {
		return err
	}
	bc.tdCache.Add(blk.Hash(), td)
	return nil
}

// WriteBlockWithState writes a block, its receipts, and state to the database (public API).
func (bc *BlockChain) WriteBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs interface{}, nopay map[types.Address]*uint256.Int) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()

	stateDB, ok := ibs.(*state.IntraBlockState)
	if !ok {
		return errors.New("WriteBlockWithState: ibs must be *state.IntraBlockState")
	}
	_, err := bc.writeBlockWithState(blk, receipts, stateDB, nopay)
	return err
}

// writeBlockWithState persists block, receipts, state, and reward data, then
// decides whether a reorg is needed. Returns the resulting WriteStatus.
func (bc *BlockChain) writeBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs *state.IntraBlockState, nopay map[types.Address]*uint256.Int) (status WriteStatus, retErr error) {
	concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
	if err != nil {
		return NonStatTy, err
	}
	blockNumber, err := requireBlockNumber(concreteBlock, "block number unavailable")
	if err != nil {
		return NonStatTy, err
	}
	// OpenTelemetry: trace block write operations.
	bcTracer := tracing.Tracer("blockchain")
	_, span := tracing.StartSpan(bc.ctx, bcTracer, "blockchain.writeBlockWithState")
	span.SetAttributes(
		tracing.Int64Attr("block.number", int64(blockNumber.Uint64())),
		tracing.StringAttr("block.hash", blk.Hash().String()),
		tracing.Int64Attr("block.tx_count", int64(len(blk.Body().Transactions()))),
		tracing.Int64Attr("block.receipt_count", int64(len(receipts))),
	)
	defer func() {
		if retErr != nil {
			tracing.SetSpanError(span, retErr)
		} else {
			tracing.SetSpanOK(span)
		}
		span.End()
	}()

	var externTd *uint256.Int

	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		ptd, err := rawdb.ReadTd(tx, blk.ParentHash(), uint256.NewInt(0).Sub(blockNumber, uint256.NewInt(1)).Uint64())
		if err != nil {
			return fmt.Errorf("reading parent td for block %d: %w", blockNumber.Uint64(), err)
		}
		if ptd == nil {
			return consensus.ErrUnknownAncestor
		}

		externTd = uint256.NewInt(0).Add(ptd, blk.Difficulty())
		if err := rawdb.WriteTd(tx, blk.Hash(), blockNumber.Uint64(), externTd); err != nil {
			return err
		}
		log.Trace("writeTd:", "number", blockNumber.Uint64(), "hash", blk.Hash(), "td", externTd.Uint64())

		if len(receipts) > 0 {
			if err := rawdb.AppendReceipts(tx, blockNumber.Uint64(), receipts); err != nil {
				log.Errorf("rawdb.AppendReceipts failed err= %v", err)
				return err
			}
			if err := rawdb.WriteLogIndex(tx, blockNumber.Uint64(), receipts); err != nil {
				log.Errorf("rawdb.WriteLogIndex failed err= %v", err)
				return err
			}
		}
		if err := rawdb.WriteBlock(tx, concreteBlock); err != nil {
			return err
		}

		// Persist the per-block BLS committee evidence in the same atomic batch —
		// on EVERY node, not just the producing leader. The simulated pool is
		// deterministic (same seed on all nodes), so followers regenerate the
		// byte-identical CE at import. Without this, only each block's leader
		// held its own CE; under leader rotation the NEXT leader had no parent
		// CE to derive ParentBeaconRoot from and the resealed chain's evidence
		// chain broke at the first live block (7-node smoke, blocks 1001+).
		if bc.committeePool != nil {
			if hdr, ok := concreteBlock.Header().(*block.Header); ok && hdr != nil {
				ce, cerr := bc.committeePool.BuildBlockEvidence(blockNumber.Uint64(), blk.Hash(), hdr.ReceiptHash)
				if cerr != nil {
					return fmt.Errorf("build consensus evidence block %d: %w", blockNumber.Uint64(), cerr)
				}
				if err := rawdb.WriteConsensusEvidence(tx, blockNumber.Uint64(), ce); err != nil {
					return fmt.Errorf("write consensus evidence block %d: %w", blockNumber.Uint64(), err)
				}
			}
		}

		var stateWriter state.WriterWithChangeSets = state.NewPlainStateWriter(tx, tx, blockNumber.Uint64())
		if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
			stateWriter = state.NewCachedStateWriter(stateWriter, cache)
		}

		// Wrap with DiffCollector to capture state diffs for snapshot acceleration.
		var diffCollector *statesnapshot.DiffCollector
		if bc.snapshotTree != nil {
			diffCollector = statesnapshot.NewDiffCollector(stateWriter)
			stateWriter = diffCollector
		}

		// ibs may be nil when the block was accepted via ZK proof fast path
		// (EVM re-execution skipped). In that case, skip state commit since
		// the proven state root is trusted.
		if ibs != nil {
			if err := ibs.CommitBlock(bc.chainConfig.Rules(blockNumber.Uint64()), stateWriter); err != nil {
				return err
			}
			// A prior attempt at this height (hotstuff view-change re-production,
			// a competing same-height proposal, or a reorg re-import) may have
			// already written AccountChangeSet/StorageChangeSet rows for this
			// blockNum. Those tables are AppendDup (strictly-increasing key,data);
			// re-writing the same height collides → MDBX_EKEYMISMATCH and the node
			// wedges. Clear any rows ≥ this height first — a no-op on the
			// strictly-forward happy path, and safe (same tx as the re-append).
			if err := changeset.Truncate(tx, blockNumber.Uint64()); err != nil {
				return fmt.Errorf("truncating stale changesets for block %d failed: %w", blockNumber.Uint64(), err)
			}
			if err := stateWriter.WriteChangeSets(); err != nil {
				return fmt.Errorf("writing changesets for block %d failed: %w", blockNumber.Uint64(), err)
			}
			if err := stateWriter.WriteHistory(); err != nil {
				return fmt.Errorf("writing history for block %d failed: %w", blockNumber.Uint64(), err)
			}
		}

		// Flush JMT dirty nodes into the current MDBX transaction and persist root.
		// Skip when ibs is nil (ZK fast path): no EVM was executed, so no JMT
		// updates occurred. Writing the JMT root here would incorrectly persist
		// the parent's root as the current block's root.
		if ibs != nil && bc.jmtEnabled && bc.jmtCommitment != nil {
			mdbxNodeStore := jmtstore.NewMDBXStore(tx, jmtstore.JMTNodeTable)
			if err := bc.jmtCommitment.Tree().FlushTo(mdbxNodeStore); err != nil {
				return fmt.Errorf("flushing JMT nodes for block %d failed: %w", blockNumber.Uint64(), err)
			}
			jmtRoot := jmt.Hash(bc.jmtCommitment.Root())
			if err := jmtstore.WriteJMTRoot(tx, jmtRoot); err != nil {
				return fmt.Errorf("writing JMT root for block %d failed: %w", blockNumber.Uint64(), err)
			}
			if err := jmtstore.WriteJMTVersion(tx, blockNumber.Uint64()); err != nil {
				return fmt.Errorf("writing JMT version for block %d failed: %w", blockNumber.Uint64(), err)
			}
			// Update in-memory version after all DB writes succeed within this tx.
			bc.jmtCommitment.Tree().SetVersion(blockNumber.Uint64())
		}

		// Flush BMT dirty nodes into the current MDBX transaction.
		if ibs != nil && bc.bmtEnabled && bc.bmtCommitment != nil {
			bmtNodeStore := bmtstore.NewMDBXStore(tx, bmtstore.BMTNodeTable)
			if err := bc.bmtCommitment.Tree().FlushTo(bmtNodeStore); err != nil {
				return fmt.Errorf("flushing BMT nodes for block %d failed: %w", blockNumber.Uint64(), err)
			}
			bmtRoot := bc.bmtCommitment.Root()
			if err := bmtstore.WriteBMTRoot(tx, bmtRoot); err != nil {
				return fmt.Errorf("writing BMT root for block %d failed: %w", blockNumber.Uint64(), err)
			}
		}

		// Flush Verkle dirty nodes into the current MDBX transaction.
		if ibs != nil && bc.verkleEnabled && bc.verkleCommitment != nil {
			verkleNodeStore := verklestore.NewMDBXStore(tx, verklestore.VerkleNodeTable)
			if _, _, err := bc.verkleCommitment.FlushTo(verkleNodeStore); err != nil {
				return fmt.Errorf("flushing Verkle nodes for block %d failed: %w", blockNumber.Uint64(), err)
			}
			if err := verklestore.WriteVerkleVersion(tx, blockNumber.Uint64()); err != nil {
				return fmt.Errorf("writing Verkle version for block %d failed: %w", blockNumber.Uint64(), err)
			}
		}

		// Flush MPT branches + trie state checkpoint for crash recovery.
		if ibs != nil && bc.mptEnabled && bc.mptRootComputer != nil {
			if err := bc.mptRootComputer.SaveCheckpoint(tx, blockNumber.Uint64(), blk.StateRoot()); err != nil {
				return fmt.Errorf("MPT checkpoint for block %d failed: %w", blockNumber.Uint64(), err)
			}
		}

		// Flush QMDB positional entry log + twig meta into the current MDBX tx,
		// evict the just-flushed window from RAM, and record the per-block undo
		// for the recent-blocks eth_getProof window. The world root is carried by
		// header.Root (ComputeRoot returned it), so no separate root row is needed.
		if ibs != nil && bc.qmdbEnabled && bc.qmdbRootComputer != nil {
			if _, err := bc.qmdbRootComputer.FlushTo(tx); err != nil {
				return fmt.Errorf("flushing QMDB entries for block %d failed: %w", blockNumber.Uint64(), err)
			}
			bc.qmdbRootComputer.EvictFlushed()
			undo := bc.qmdbRootComputer.TakeUndo()
			if undo == nil {
				undo = &qmdb.BlockUndo{PrevNextSlot: bc.qmdbRootComputer.Tree().NextSlot()}
			}
			if err := rawdb.WriteQMDBUndo(tx, blockNumber.Uint64(), undo); err != nil {
				return fmt.Errorf("writing QMDB undo for block %d failed: %w", blockNumber.Uint64(), err)
			}
			// Track the applied-chain head (which block's state the world state
			// reflects) — the branch-switch unwind reverts to this marker's
			// lineage until it matches an incoming block's parent.
			if err := rawdb.WriteQMDBApplied(tx, blockNumber.Uint64(), blk.Hash()); err != nil {
				return fmt.Errorf("writing QMDB applied marker for block %d failed: %w", blockNumber.Uint64(), err)
			}
			const qmdbUndoWindow = 256
			if bn := blockNumber.Uint64(); bn > qmdbUndoWindow {
				if err := rawdb.PruneQMDBUndoBelow(tx, bn-qmdbUndoWindow); err != nil {
					return fmt.Errorf("pruning QMDB undo window for block %d failed: %w", bn, err)
				}
			}
		}

		// Persist LtHash digest alongside JMT root.
		if ibs != nil && bc.ltHashEnabled && bc.ltHashCommitment != nil {
			if err := lthash.WriteLtHashDigest(tx, modules.LtHashDigest, bc.ltHashCommitment.Digest()); err != nil {
				return fmt.Errorf("writing LtHash digest for block %d failed: %w", blockNumber.Uint64(), err)
			}
		}

		// Update snapshot tree with collected diffs.
		if diffCollector != nil && bc.snapshotTree != nil {
			if err := bc.snapshotTree.Update(
				blockNumber.Uint64(),
				blk.Hash(),
				blk.ParentHash(),
				diffCollector.Accounts(),
				diffCollector.AccountDeletions(),
				diffCollector.Storage(),
			); err != nil {
				// Non-fatal: snapshot acceleration is best-effort.
				log.Warn("Failed to update snapshot tree", "block", blockNumber.Uint64(), "err", err)
			}
		}

		if nopay != nil {
			for addr, v := range nopay {
				if err := rawdb.PutAccountReward(tx, addr, v); err != nil {
					return fmt.Errorf("writing account reward for %s: %w", addr, err)
				}
			}
		}
		return nil
	}); err != nil {
		return NonStatTy, err
	}

	// Refresh the JMT backing store's RO transaction so it sees the just-committed nodes.
	if bc.jmtStoreRefresh != nil {
		bc.jmtStoreRefresh()
	}

	if externTd != nil {
		bc.tdCache.Add(blk.Hash(), externTd)
	}

	reorg, err := bc.forker.ReorgNeeded(bc.CurrentBlock().Header(), blk.Header())
	if err != nil {
		return NonStatTy, err
	}

	if reorg {
		if blk.ParentHash() != bc.CurrentBlock().Hash() {
			if err := bc.reorg(nil, bc.CurrentBlock(), blk); err != nil {
				return NonStatTy, err
			}
		}
		status = CanonStatTy
	} else {
		status = SideStatTy
	}

	// For leader-driven consensus (HotStuff), a block (miner-produced or received
	// via direct push) is stored — block + state were written above — but NOT made
	// canonical here. It becomes canonical only once HotStuff commits it
	// (BlockChain.CommitToCanonical), so every node follows the single committed
	// chain instead of racing ahead on its own locally-inserted candidate.
	leaderDriven := bc.engine != nil && !bc.engine.Type().UsesTimerDrivenSealing()
	if status == CanonStatTy && !leaderDriven {
		if err := bc.writeHeadBlock(nil, blk); err != nil {
			log.Errorf("failed to save latest blocks, err: %v", err)
			return NonStatTy, err
		}

		// Notify ExEx extensions about the committed block.
		if bc.exexManager != nil {
			bc.exexManager.Notify(&exex.ExExNotification{
				Type:     exex.NotificationCommit,
				Block:    blk,
				Receipts: receipts,
				Hash:     blk.Hash(),
				Number:   blockNumber.Uint64(),
			})
		}
	}

	if _, ok := bc.futureBlocks.Get(blk.Hash()); ok {
		bc.futureBlocks.Remove(blk.Hash())
	}
	return status, nil
}

func (bc *BlockChain) headWriteContext() context.Context {
	if bc.ctx == nil || bc.ctx.Err() != nil {
		// The main block/state write may have already committed when node shutdown
		// cancels bc.ctx. Allow the short head/canonical finalization tx to finish.
		return context.Background()
	}
	return bc.ctx
}

// writeHeadBlock updates the canonical head block and its associated indexes.
func (bc *BlockChain) writeHeadBlock(tx kv.RwTx, blk block.IBlock) error {
	concreteBlock, castErr := requireConcreteBlock(blk, "unexpected block type")
	if castErr != nil {
		return castErr
	}
	blockNumber, numberErr := requireBlockNumber(concreteBlock, "block number unavailable")
	if numberErr != nil {
		return numberErr
	}
	var err error
	var notExternalTx bool
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.headWriteContext())
		if err != nil {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	rawdb.WriteHeadBlockHash(tx, blk.Hash())
	rawdb.WriteTxLookupEntries(tx, concreteBlock)

	if err = rawdb.WriteCanonicalHash(tx, blk.Hash(), blockNumber.Uint64()); err != nil {
		return err
	}

	if notExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	bc.currentBlock.Store(concreteBlock)
	headBlockGauge.Set(blockNumber.Uint64())
	headGasUsedGauge.Set(concreteBlock.GasUsed())
	headGasLimitGauge.Set(concreteBlock.GasLimit())
	headTransactionsGauge.Set(uint64(len(concreteBlock.Transactions())))
	nodeMetrics.SyncCurrentBlock.Set(blockNumber.Uint64())

	// Periodically decay the prefetch predictor to favor recent access patterns.
	if bc.prefetchPredictor != nil && blockNumber.Uint64()%100 == 0 {
		bc.prefetchPredictor.Decay(0.9)
	}

	return nil
}

// writeKnownBlock updates the head block with a known block
// and introduces chain reorg if necessary.
func (bc *BlockChain) writeKnownBlock(tx kv.RwTx, blk block.IBlock) error {
	var notExternalTx bool
	var err error
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	current := bc.CurrentBlock()
	if blk.ParentHash() != current.Hash() {
		if err := bc.reorg(tx, current, blk); err != nil {
			return err
		}
	}
	if err = bc.writeHeadBlock(tx, blk); err != nil {
		return err
	}
	if notExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
