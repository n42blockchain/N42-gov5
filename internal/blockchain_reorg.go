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
// Reorg, head reset and unwind/realign helpers.

package internal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// ReorgNeeded determines if a reorg should occur based on block numbers and difficulty.
func (bc *BlockChain) ReorgNeeded(current block.IBlock, header block.IBlock) bool {
	switch current.Number64().Cmp(header.Number64()) {
	case 1:
		return false
	case 0:
		return current.Difficulty().Cmp(header.Difficulty()) < 0
	}
	return true
}

// SetHead resets the canonical head to a specific block number.
func (bc *BlockChain) SetHead(head uint64) error {
	newHeadBlock, err := bc.GetBlockByNumber(uint256.NewInt(head))
	if err != nil {
		return err
	}
	if newHeadBlock == nil {
		return fmt.Errorf("block #%d not found", head)
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteHeadHeaderHash(tx, newHeadBlock.Hash())
	})
}

// =============================================================================
// Reorg
// =============================================================================

// reorg reconstructs the canonical chain when a fork is detected.
// Instrumented with ReorgAudit for observability.
func (bc *BlockChain) reorg(tx kv.RwTx, oldBlock, newBlock block.IBlock) error {
	audit := GetReorgAudit()
	auditEvent := audit.StartReorg(oldBlock, newBlock)

	var (
		newChain    block.Blocks
		oldChain    block.Blocks
		commonBlock block.IBlock
		deletedTxs  []types.Hash
		addedTxs    []types.Hash
	)
	defer func() {
		audit.EndReorg(auditEvent, commonBlock, oldChain, newChain, len(deletedTxs), len(addedTxs), nil)
	}()

	if oldBlock == nil {
		return errors.New("invalid old chain")
	}
	if newBlock == nil {
		return errors.New("invalid new chain")
	}
	// No-op reorg: old and new head are the same block. The common-ancestor walk
	// would match on its first iteration, leaving both chains empty and tripping
	// the "Impossible reorg" ERROR below — a false alarm that also spins when an
	// applied≠canonical node repeatedly asks to align onto its own canonical head.
	// Nothing to reorg; return quietly (at debug) instead of erroring.
	if oldBlock.Hash() == newBlock.Hash() {
		log.Debug("reorg no-op: old and new head are identical",
			"number", newBlock.Number64(), "hash", newBlock.Hash())
		return nil
	}
	oldNumber, err := requireBlockNumber(oldBlock, "old block number unavailable")
	if err != nil {
		return err
	}
	newNumber, err := requireBlockNumber(newBlock, "new block number unavailable")
	if err != nil {
		return err
	}

	// Reduce the longer chain to match the shorter one
	if oldNumber.Uint64() > newNumber.Uint64() {
		for oldBlock != nil && oldNumber.Uint64() != newNumber.Uint64() {
			oldChain = append(oldChain, oldBlock)
			for _, tx := range oldBlock.Transactions() {
				deletedTxs = append(deletedTxs, tx.Hash())
			}
			if oldNumber.IsZero() {
				break
			}
			oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldNumber.Uint64()-1)
			if oldBlock != nil {
				oldNumber, err = requireBlockNumber(oldBlock, "old block number unavailable")
				if err != nil {
					return err
				}
			}
		}
	} else {
		for newBlock != nil && newNumber.Uint64() != oldNumber.Uint64() {
			newChain = append(newChain, newBlock)
			if newNumber.IsZero() {
				break
			}
			newBlock = bc.GetBlock(newBlock.ParentHash(), newNumber.Uint64()-1)
			if newBlock != nil {
				newNumber, err = requireBlockNumber(newBlock, "new block number unavailable")
				if err != nil {
					return err
				}
			}
		}
	}
	if oldBlock == nil {
		return errors.New("invalid old chain")
	}
	if newBlock == nil {
		return errors.New("invalid new chain")
	}

	var useExternalTx bool
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
	} else {
		useExternalTx = true
	}

	// Walk back both sides to find the common ancestor
	for {
		if oldBlock.Hash() == newBlock.Hash() {
			commonBlock = oldBlock
			break
		}
		oldChain = append(oldChain, oldBlock)
		for _, t := range oldBlock.Transactions() {
			deletedTxs = append(deletedTxs, t.Hash())
		}
		newChain = append(newChain, newBlock)

		oldNumber, err = requireBlockNumber(oldBlock, "old block number unavailable")
		if err != nil {
			return err
		}
		newNumber, err = requireBlockNumber(newBlock, "new block number unavailable")
		if err != nil {
			return err
		}
		if oldNumber.IsZero() || newNumber.IsZero() {
			return errors.New("reached genesis without finding common ancestor")
		}
		oldBlock = rawdb.ReadBlock(tx, oldBlock.ParentHash(), oldNumber.Uint64()-1)
		if oldBlock == nil {
			return errors.New("invalid old chain")
		}
		newBlock = rawdb.ReadBlock(tx, newBlock.ParentHash(), newNumber.Uint64()-1)
		if newBlock == nil {
			return errors.New("invalid new chain")
		}
	}

	// Log the reorg
	if len(oldChain) > 0 && len(newChain) > 0 {
		logFn := log.Info
		msg := "Chain reorg detected"
		if len(oldChain) > 63 {
			msg = "Large chain reorg detected"
			logFn = log.Warn
		}
		logFn(msg, "number", commonBlock.Number64(), "hash", commonBlock.Hash(),
			"drop", len(oldChain), "dropfrom", oldChain[0].Hash(), "add", len(newChain), "addfrom", newChain[0].Hash())
	} else if len(newChain) > 0 {
		log.Info("Extend chain", "add", len(newChain), "number", newChain[0].Number64(), "hash", newChain[0].Hash())
	} else {
		log.Error("Impossible reorg, please file an issue", "oldnum", oldBlock.Number64(), "oldhash", oldBlock.Hash(), "oldblocks", len(oldChain), "newnum", newBlock.Number64(), "newhash", newBlock.Hash(), "newblocks", len(newChain))
	}

	// Insert new chain blocks (except head) in proper order
	for i := len(newChain) - 1; i >= 1; i-- {
		blockNumber, numberErr := requireBlockNumber(newChain[i], "new chain block number unavailable")
		if numberErr != nil {
			return numberErr
		}
		if err := bc.writeHeadBlock(tx, newChain[i]); err != nil {
			return fmt.Errorf("failed to write head block during reorg at %d: %w", blockNumber.Uint64(), err)
		}
		for _, t := range newChain[i].Transactions() {
			addedTxs = append(addedTxs, t.Hash())
		}
	}

	// Clean up stale tx indexes
	for _, t := range types.HashDifference(deletedTxs, addedTxs) {
		rawdb.DeleteTxLookupEntry(tx, t)
	}

	// Delete stale canonical hash markers
	commonNumber, err := requireBlockNumber(commonBlock, "common block number unavailable")
	if err != nil {
		return err
	}
	number := commonNumber.Uint64()
	if len(newChain) > 1 {
		newChainNumber, numberErr := requireBlockNumber(newChain[1], "new chain block number unavailable")
		if numberErr != nil {
			return numberErr
		}
		number = newChainNumber.Uint64()
	}
	for i := number + 1; ; i++ {
		hash, _ := rawdb.ReadCanonicalHash(tx, i)
		if hash == (types.Hash{}) {
			break
		}
		rawdb.TruncateCanonicalHash(tx, i, false)
	}

	if !useExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// tryZKFastPath attempts to verify the block's ZK proof. Returns true if
// the proof is valid and EVM re-execution can be skipped.
// unwindForReimport reverts the applied world state to just before block n
// when a block at height >= n has already been applied — the HotStuff
// same-height sibling switch (a view-change re-proposal arriving after a
// competing candidate was speculatively imported). Without this, the sibling
// re-executes on DIRTY state: PlainState keys the loser touched keep its
// values, and the append-order-dependent QMDB tree appends at shifted slots,
// forking its root permanently vs nodes that only ever applied the winner.
//
// Detection is the QMDB undo record itself: every applied block writes one
// (blockchain_write.go), so undo rows at heights >= n mean applied state that
// must be cleanly reverted first. Per reverted height h (newest→oldest):
// QMDB tree via RevertBlock (undo), PlainState via the block's changeset
// pre-values, and the by-number derived rows (receipts/logs truncate, CE,
// the undo row itself). History-index rows are left (inflation only, no
// correctness impact — tracked in the status doc). Forward-path cost: one
// missing-key read of undo[n].
//
// NOTE: the in-memory QMDB tree mutates inside the tx; if the tx later fails
// to commit, tree and disk disagree until restart (LoadFrom rebuilds from
// disk). The error is propagated, so the node does not continue on the
// mismatched state.
// AlignAppliedBranch makes the applied world state be the post-state of
// parentHash (at height childNum-1), reverting any locally-applied uncommitted
// sibling branch first — the precondition for building/executing a block on
// the consensus-mandated parent (HotStuff HighQC block) rather than the local
// head. No-op when already aligned; ErrUnknownAncestor when the parent itself
// was never applied and must import first.
func (bc *BlockChain) AlignAppliedBranch(childNum uint64, parentHash types.Hash) error {
	// Miner alignment races ordinary block import. Both paths can peel or
	// unwind the live QMDB tree, whose in-memory mutations are not covered by
	// the MDBX transaction. Serialize the whole operation with InsertChain and
	// WriteBlockWithState so a leader cannot consume an import's in-flight undo.
	//
	// S11: bc.lock is exactly the mutex a leader build shares with import/
	// write, so time the wait for it when diagnosing a build stall
	// (docs/QS_BLOCK_TIME_BUDGET.md 6by). Off by default -- no timing calls.
	if buildStallDiagEnabled {
		t0 := time.Now()
		bc.lock.Lock()
		bc.buildStallLockWaitNs.Add(int64(time.Since(t0)))
	} else {
		bc.lock.Lock()
	}
	defer bc.lock.Unlock()
	return bc.unwindForReimport(childNum, parentHash, true)
}

// revertUncommittedQMDBAppends peels a FAILED block's appends off the live
// QMDB tree. Execution mutates the tree (ComputeRoot appends) inside a tx that
// then rolls back on validation failure — but the tree itself is not
// transactional, and the block's undo row is only persisted by
// writeBlockWithState, so those appends could never be unwound later: every
// subsequent block would append at shifted slots and the node's roots would
// silently diverge from the cluster until a restart reload. The undo record
// captured by that very ComputeRoot is still in RAM — apply it (memory-only:
// nothing of the block ever reached disk). TakeUndo doubles as the "did
// execution reach ComputeRoot" test: it is cleared by every successful
// writeBlockWithState and by this function, so a non-nil record here always
// belongs to the failed block.
// PeelDanglingQMDBAppends implements common.IBlockChain: revert appends a
// discarded miner candidate left on the live tree (see
// revertUncommittedQMDBAppends — same mechanism, caller-facing entry).
func (bc *BlockChain) PeelDanglingQMDBAppends() {
	bc.revertUncommittedQMDBAppends(0)
}

// ensureQMDBTreeAtParent enforces the pre-execution invariant that the live
// QMDB tree's root equals the incoming block's PARENT header root — i.e. the
// world state actually is the state the block builds on. On a mismatch it
// realigns the applied marker to the height whose header root the tree DOES
// match (walking the parent lineage down through the undo window), so the
// regular catch-up/future-queue machinery replays forward from the tree's
// true position instead of executing on a wrong base. Returns nil when
// aligned (or QMDB inactive / parent unknown — later checks own those).
func (bc *BlockChain) ensureQMDBTreeAtParent(blk block.IBlock) error {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return nil
	}
	parent, err := bc.GetHeaderByHash(blk.ParentHash())
	if err != nil || parent == nil {
		return nil // unknown ancestor is handled by the normal import path
	}
	pHdr, ok := parent.(*block.Header)
	if !ok {
		return nil
	}
	treeRoot := bc.qmdbRootComputer.Root()
	// A header's Root is the state after that block before the deferred
	// execution fork and the PARENT's after it; the tree is compared with
	// each header's EXECUTED root.
	execRootOf := func(h *block.Header) (types.Hash, bool) {
		if bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(h.Time) || h.Number.Uint64() == 0 {
			return h.Root, true
		}
		var r rawdb.ExecutedResult
		var found bool
		_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
			var err error
			r, found, err = rawdb.ReadExecutedResult(tx, h.Hash())
			return err
		})
		return r.Root, found
	}
	if pr, ok := execRootOf(pHdr); ok && treeRoot == pr {
		return nil
	}
	// Out-of-order arrival, not a discontinuity: the applied state simply has
	// not reached the parent yet (block N+1 gossiped before N imported). The
	// regular future-queue/catch-up path owns this — stay silent.
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		if an, _, ok, aerr := rawdb.ReadQMDBApplied(tx); aerr == nil && ok && an < pHdr.Number.Uint64() {
			return errQMDBBehindParent
		}
		return nil
	}); err != nil {
		return err
	}
	// Walk down from the parent through the undo window looking for the
	// header whose root the tree matches, then roll the OTHER two states back
	// to it. A marker-only realign proved insufficient live: PlainState kept
	// the blocks the tree had lost, catch-up re-executed them on that phantom
	// world state, and the node built nonce-stale blocks the whole network
	// rejected. The realign is now three-state atomic — PlainState unwound
	// block-by-block from its changeset pre-values down to the tree's height,
	// per-block bookkeeping consumed, marker moved — in one transaction, so a
	// failure leaves everything exactly as it was.
	cur := pHdr
	for depth := 0; depth < 256 && cur != nil; depth++ {
		if cr, ok := execRootOf(cur); ok && treeRoot == cr {
			target, targetHash := cur.Number.Uint64(), cur.Hash()
			if werr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
				return realignAppliedToTree(tx, target, targetHash)
			}); werr != nil {
				return fmt.Errorf("tree at %d/%x but three-state realign failed: %w", target, targetHash[:8], werr)
			}
			bc.clearReadThroughCache()
			return fmt.Errorf("tree root %x matches header %d, not the incoming parent %d — PlainState and marker realigned to the tree",
				treeRoot[:8], target, pHdr.Number.Uint64())
		}
		p, perr := bc.GetHeaderByHash(cur.ParentHash)
		if perr != nil || p == nil {
			break
		}
		cur, _ = p.(*block.Header)
	}
	return fmt.Errorf("tree root %x matches no header within the undo window below parent %d/%x",
		treeRoot[:8], pHdr.Number.Uint64(), pHdr.Root[:8])
}

// qmdbRealignMaxDepth caps how many blocks a discontinuity realign may roll
// back. It mirrors (with slack) the 256-block undo/lineage window: a span
// beyond it means the changesets needed for a faithful PlainState rewind may
// already be consumed, and a partial heal is worse than staying wedged behind
// the root/nonce verification walls.
const qmdbRealignMaxDepth = 512

// realignAppliedToTree rolls PlainState and the applied marker back to
// `target` — the height whose header root the live QMDB tree ALREADY holds —
// restoring the tree/PlainState/marker three-state invariant. The tree itself
// is never touched: it is the reference the other two are aligned to.
//
// PlainState's true height is taken from the changeset head, not the marker:
// writeBlockWithState persists PlainState, its changesets and the marker in
// one transaction, so the highest changeset block number IS the last block
// PlainState absorbed — even when an earlier marker-only realign already
// moved the marker away (the half-healed shape observed live on node4).
// Everything above target is unwound via the changeset pre-values and its
// per-block bookkeeping (undo record, consensus evidence, receipts) consumed;
// the re-imported blocks rewrite all of it.
func realignAppliedToTree(tx kv.RwTx, target uint64, targetHash types.Hash) error {
	from, err := highestChangesetBlock(tx)
	if err != nil {
		return err
	}
	if from > target {
		if from-target > qmdbRealignMaxDepth {
			return fmt.Errorf("refusing to realign %d blocks (PlainState at %d, tree at %d): beyond the changeset window", from-target, from, target)
		}
		for h := from; h > target; h-- {
			if err := commitment.UnwindPlainStateBlock(tx, h); err != nil {
				return fmt.Errorf("plain-state unwind of block %d: %w", h, err)
			}
			key := modules.EncodeBlockNumber(h)
			if err := tx.Delete(modules.QMDBUndoWindow, key); err != nil {
				return err
			}
			if err := tx.Delete(modules.ConsensusEvidence, key); err != nil {
				return err
			}
		}
		if err := rawdb.TruncateReceipts(tx, target+1); err != nil {
			return fmt.Errorf("truncate receipts from %d: %w", target+1, err)
		}
	}
	return rawdb.WriteQMDBApplied(tx, target, targetHash)
}

// highestChangesetBlock returns the highest block number present in the
// account/storage changesets — PlainState's true height (see
// realignAppliedToTree). 0 when both tables are empty.
func highestChangesetBlock(tx kv.Tx) (uint64, error) {
	var last uint64
	for _, table := range []string{modules.AccountChangeSet, modules.StorageChangeSet} {
		c, err := tx.Cursor(table)
		if err != nil {
			return 0, err
		}
		k, _, err := c.Last()
		c.Close()
		if err != nil {
			return 0, err
		}
		if len(k) >= 8 {
			if n := binary.BigEndian.Uint64(k[:8]); n > last {
				last = n
			}
		}
	}
	return last, nil
}

func (bc *BlockChain) revertUncommittedQMDBAppends(blockNum uint64) {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return
	}
	// A write in flight first: its block is not this one's to peel, and a
	// failed one reloads the tree (which drops these appends too).
	if err := bc.joinLagWrite(); err != nil {
		log.Warn("peel: the write in flight failed; tree reloaded from the store", "err", err)
	}
	// Idempotent with the writeBlockWithState failure path: make sure the
	// staged flush is discarded before the peel — ApplyUndo prunes revived
	// slots out of deadFlushed and must see the re-queued reclaim list.
	bc.qmdbRootComputer.AbortFlushed()
	undo := bc.qmdbRootComputer.TakeUndo()
	if undo == nil {
		return // failed before ComputeRoot — nothing was appended
	}
	err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		// The tree's cold getter still points at the rolled-back execution
		// tx; the boundary-twig reconstruction inside ApplyUndo may fault
		// leaves through it. Re-point at a live tx for the duration.
		bc.qmdbRootComputer.SetCold(tx)
		defer bc.qmdbRootComputer.SetCold(nil)
		return bc.qmdbRootComputer.ApplyUndo(undo)
	})
	if err == nil {
		log.Debug("peeled failed block's appends off the QMDB tree", "number", blockNum)
		return
	}
	log.Warn("failed to peel failed block's QMDB appends; reloading tree from disk",
		"number", blockNum, "err", err)
	if rerr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		bc.qmdbRootComputer.SetCold(tx)
		defer bc.qmdbRootComputer.SetCold(nil)
		return bc.qmdbRootComputer.LoadFrom(tx)
	}); rerr != nil {
		log.Error("qmdb tree reload after failed block FAILED — state may diverge", "err", rerr)
	}
}

func (bc *BlockChain) unwindForReimport(n uint64, parentHash types.Hash, authorizedSwitch bool) error {
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil || n == 0 {
		return nil
	}
	if err := bc.joinLagWrite(); err != nil {
		return err
	}
	// The revert chain must start from the tree the applied marker describes.
	// A dangling candidate's appends (a leader whose sealed block lost the
	// view before this switch arrived) sit between the live tree head and the
	// marker's first undo record; reverting over them either wedges (the undo
	// no longer matches the tree head) or leaves a stale lastUndo behind that
	// the NEXT import's peel replays against the rewound tree — observed live
	// as "undo record is ahead of this tree" followed by a disk reload whose
	// root matched no header in the undo window. Peel before unwinding.
	bc.PeelDanglingQMDBAppends()
	mutated := false
	err := bc.unwindForReimportTx(n, parentHash, authorizedSwitch, &mutated)
	// The speculative computer is NOT voided here any more (359e1f89 did,
	// and ea76069e narrowed it to this path): the post-switch bad roots
	// were builds on a persisted-but-unapplied sibling, fixed by waiting
	// for the applied marker (580d2f32), and the void's full rebuild takes
	// 10-23 s at 13.9M blocks -- long enough to time out the view, whose
	// stale seal then causes the next switch (35o A1: four blocks in a
	// 400 s decay). voidMinerRootTrust stays for an operator/debug path.
	if err != nil && !mutated {
		// Pre-check rejections (finality floor, lineage mismatch → future
		// queue, unauthorized passive switch) fail BEFORE any tree mutation:
		// nothing to restore, and reloading the whole tree for each of them
		// turned routine catch-up traffic into a stream of expensive reloads
		// (observed: 34-54 "failed unwind" reloads per replay, most benign).
		return err
	}
	if err != nil {
		// A multi-block unwind that fails MIDWAY leaves the in-memory QMDB
		// tree holding reverts the rolled-back transaction discarded: block N
		// reverted in memory AND in the tx, block N-1's revert failed, the tx
		// rolled back (disk keeps N applied) — but the memory tree already
		// dropped N. The node then keeps running on the diverged tree and the
		// next FlushTo writes the divergence to disk permanently. Observed
		// live: three nodes whose tree roots matched NO nearby header after a
		// degraded-mesh period of repeated sibling switches, wedged with
		// nonce errors. Reload the tree from disk to restore the invariant —
		// the inline version of the "until restart" the old comment settled
		// for.
		// Use an Update (not a View): the MDBX write lock serializes this
		// reload against any concurrent unwind's tree mutations. And refresh
		// the tree's cold getter FIRST — LoadFrom faults some leaves through
		// the getter the tree already holds, which at this point is a
		// long-dead read transaction from the previous block's execution
		// (observed live: reload panicked on a nil mdbx txn and killed six
		// nodes).
		if rerr := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
			bc.qmdbRootComputer.SetCold(tx)
			lerr := bc.qmdbRootComputer.LoadFrom(tx)
			// The tx dies when this callback returns; detach so nothing
			// faults through it before the next block re-points the getter.
			bc.qmdbRootComputer.SetCold(nil)
			return lerr
		}); rerr != nil {
			log.Error("qmdb tree reload after failed unwind FAILED — state may diverge", "err", rerr)
		} else {
			log.Warn("qmdb tree reloaded from disk after failed unwind", "unwindErr", err)
		}
	}
	if err == nil {
		// The unwind's per-block RevertBlock flushed the rewound layout through
		// the now-committed tx and staged its dead-row reclaims; adopt them so a
		// later block's AbortFlushed can't re-queue deletes that are already
		// durable. (A mutated failure took the reload branch above, which resets
		// the staged state; a pre-mutation reject staged nothing.)
		bc.qmdbRootComputer.CommitFlushed()
	}
	return err
}

func (bc *BlockChain) unwindForReimportTx(n uint64, parentHash types.Hash, authorizedSwitch bool, mutated *bool) error {
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		appliedNum, appliedHash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			// Fresh replay data: the applied chain is the stored head.
			hn := rawdb.ReadCurrentBlockNumber(tx)
			if hn == nil {
				return nil
			}
			appliedNum = *hn
			ch, cerr := rawdb.ReadCanonicalHash(tx, appliedNum)
			if cerr != nil {
				return cerr
			}
			appliedHash = ch
		}
		if appliedNum == n-1 && appliedHash == parentHash {
			return nil // plain forward import — the incoming block extends the applied head
		}
		if appliedNum < n-1 {
			return nil // parent's state not applied yet — ValidateBody/future-queue handles it
		}
		// Committed-BLOCK floor (HotStuff-2: only the 2-chain-committed block is
		// final): a block ON the committed chain must never be reverted.
		// Finality evidence is the HotStuffCommittedHead marker — written
		// EXCLUSIVELY by CommitToCanonical, not HeadBlockHash (legacy
		// import-time writers stamped losing siblings into HeadBlockHash and
		// froze such nodes forever). Crucially the floor protects BLOCKS, not
		// HEIGHTS: reverting a losing sibling AT (or below) the committed
		// height, in order to replay the committed block itself, is exactly
		// the recovery path — a height-only check bricked it (observed live:
		// a node whose canonical/marker pointed at the committed 13016802
		// while its applied state sat on a same-height sibling rejected every
		// range with unknown-ancestor; the sibling could never be reverted).
		// The block-identity check runs against the applied lineage AFTER the
		// walk below; here we only resolve the committed height.
		var committedHead uint64
		if ch := rawdb.ReadHotStuffCommittedHead(tx); ch != (types.Hash{}) {
			if hn := rawdb.ReadHeaderNumber(tx, ch); hn != nil {
				committedHead = *hn
			}
		}
		// READ-ONLY pre-check first (no side effects on failure): walk the
		// applied lineage down to height n-1 and require it to BE the incoming
		// block's parent. If it isn't, the parent is a competing sibling this
		// node never applied — it cannot be "unwound into"; it must import
		// first (ErrUnknownAncestor → future-queue), and nothing may be
		// reverted yet (a partial revert with a rolled-back tx would desync the
		// in-memory QMDB tree from disk).
		lineage := make([]types.Hash, 0, appliedNum-(n-1)+1)
		curNum, curHash := appliedNum, appliedHash
		for {
			lineage = append(lineage, curHash)
			if curNum == n-1 {
				break
			}
			hdr := rawdb.ReadHeader(tx, curHash, curNum)
			if hdr == nil {
				return fmt.Errorf("branch switch at %d: applied header %d/%x missing", n, curNum, curHash[:8])
			}
			curNum--
			curHash = hdr.ParentHash
		}
		if curHash != parentHash {
			return fmt.Errorf("branch switch at %d: applied lineage at %d is %x, incoming parent is %x (sibling parent not applied): %w",
				n, n-1, curHash[:8], parentHash[:8], consensus.ErrUnknownAncestor)
		}

		// Committed-BLOCK floor enforcement (see the marker resolution above):
		// none of the applied blocks about to be reverted (heights
		// n..appliedNum, lineage[i] = height appliedNum-i) may sit ON the
		// committed chain — the canonical table is the committed prefix
		// (single writer + truncate), so "at/below the committed height AND
		// canonical" identifies a committed block. Reverting a same-height
		// LOSING sibling is allowed: that is the recovery path.
		for i, h := 0, appliedNum; h >= n; i, h = i+1, h-1 {
			if h > committedHead {
				continue
			}
			ch, cerr := rawdb.ReadCanonicalHash(tx, h)
			if cerr != nil {
				return cerr
			}
			if ch == lineage[i] {
				// Loud, not silent. When this fires for a CONSENSUS-authorized
				// switch (the leader's pre-build align, driven by the persisted
				// LockedQC), the node's consensus state and its applied chain
				// have forked and block production is wedged permanently — the
				// failure that stopped a whole 7-node fleet, visible only as a
				// miner runLoop error. Name the condition and the remedy here,
				// where the condition is actually detected.
				if authorizedSwitch {
					log.Error("CONSENSUS STATE FORKED FROM APPLIED CHAIN: refusing to revert a committed block. "+
						"The consensus-mandated parent is on a branch that would roll back an already-committed "+
						"block, so this node cannot build or import on it and will stop producing. This normally "+
						"means the persisted HotStuff state points at a branch this node never applied. Operator "+
						"action: stop the node and clear the persisted consensus state with "+
						"`go run ./cmd/hotstuff-reset -datadir <datadir>/chaindata -apply` (chain data is untouched).",
						"incoming", n, "incomingParent", fmt.Sprintf("%x", parentHash[:8]),
						"committedHeight", h, "committedBlock", fmt.Sprintf("%x", lineage[i][:8]),
						"appliedHead", appliedNum, "committedHead", committedHead)
				}
				return fmt.Errorf("branch switch at %d rejected: would revert committed block %d/%x: %w",
					n, h, lineage[i][:8], consensus.ErrUnknownAncestor)
			}
		}

		// Only CONSENSUS-DRIVEN imports may revert applied blocks. A passively
		// received block (gossip, direct push, future-queue retry) whose branch
		// requires a revert is a stale sibling candidate from an old view — it
		// carries no new QC and has no authority over the applied head. Letting
		// it switch tugs the network between sibling branches (observed live:
		// a node applied through the CURRENT proposal got reverted 2 deep by a
		// dead same-height candidate, destroying the vote window every round).
		// Authorized callers: the proposal-driven chain-align (fetch-on-miss),
		// the leader's pre-build AlignAppliedBranch, and startup revert — all
		// funnel through InsertChainAuthorized / AlignAppliedBranch.
		if !authorizedSwitch {
			return fmt.Errorf("branch switch at %d requires consensus authority (passive import of a sibling branch): %w",
				n, consensus.ErrUnknownAncestor)
		}

		depth := appliedNum - (n - 1)
		log.Warn("branch switch: unwinding applied blocks to the incoming block's parent",
			"incoming", n, "incomingParent", fmt.Sprintf("%x", parentHash[:8]),
			"appliedHead", appliedNum, "appliedHash", fmt.Sprintf("%x", appliedHash[:8]), "depth", depth)

		// Revert the applied chain head-first down to the parent. Each step
		// needs the applied block's undo record (the window is 256 blocks — a
		// deeper switch is outside HotStuff's view-change reach and is rejected
		// rather than mis-unwound). lineage[i] is the applied hash at height
		// appliedNum-i.
		for i := 0; appliedNum >= n; i++ {
			key := modules.EncodeBlockNumber(appliedNum)
			raw, gerr := tx.GetOne(modules.QMDBUndoWindow, key)
			if gerr != nil {
				return gerr
			}
			if len(raw) == 0 {
				return fmt.Errorf("branch switch at %d: no undo record for applied block %d (beyond the undo window)", n, appliedNum)
			}
			undo, derr := qmdb.UnmarshalBlockUndo(raw)
			if derr != nil {
				return fmt.Errorf("unwind block %d: decode undo: %w", appliedNum, derr)
			}
			if err := bc.qmdbRootComputer.RevertBlock(tx, undo); err != nil {
				// A revert that cannot reconstruct a twig's frozen leaves (stale/
				// missing leaf blob) is a recoverable LOCAL condition, not proof the
				// incoming block is invalid. Tag it so the caller future-queues the
				// block instead of marking it BAD BLOCK — and so a single rogue block
				// can no longer panic-crash the node (the QMDB layer now returns this
				// error rather than panicking). Step 0 of ApplyUndo runs the only
				// fallible reconstruction before mutating, so this first-block failure
				// leaves the in-memory tree untouched; the tx rolls back on return.
				return fmt.Errorf("unwind block %d: qmdb revert: %w: %w", appliedNum, err, errRevertUnavailable)
			}
			// The miner's speculative tree loaded this block from the store;
			// hand it the same undo so its next build peels it too (applied
			// on the build goroutine, never here -- a build may be reading
			// that tree right now).
			bc.queueMinerRewind(undo)
			// The tree is mutated from here on: a failure below this point
			// (or in a LATER iteration) leaves the in-memory tree ahead of
			// the rolled-back transaction — the caller must reload it.
			*mutated = true
			if err := commitment.UnwindPlainStateBlock(tx, appliedNum); err != nil {
				return fmt.Errorf("unwind block %d: plain state: %w", appliedNum, err)
			}
			if err := tx.Delete(modules.QMDBUndoWindow, key); err != nil {
				return err
			}
			if err := tx.Delete(modules.ConsensusEvidence, key); err != nil {
				return err
			}
			appliedNum--
			appliedHash = lineage[i+1]
			if err := rawdb.WriteQMDBApplied(tx, appliedNum, appliedHash); err != nil {
				return err
			}
		}
		// Receipts + logs for the reverted range (append-keyed; the re-executed
		// blocks re-append them).
		if err := rawdb.TruncateReceipts(tx, n); err != nil {
			return fmt.Errorf("unwind receipts from %d: %w", n, err)
		}
		// Invalidate any read-through caches that bypassed the unwind.
		if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
			cache.Clear()
		}
		return nil
	})
}
