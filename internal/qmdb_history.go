// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Chain wiring of the opt-in QMDB full-history journal (--state.qmdb-history,
// see modules/state/commitment/qmdb_live_history.go): enabled before Start,
// checked/seeded in Start after the startup repairs, written by every block
// write and unwound by every unwind of a persisted block.

package internal

import (
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// EnableQMDBHistory requests the full-history journal. Call after
// SetQMDBRootComputer and before Start; Start validates (or seeds) the journal
// and fails when the database cannot continue it. reseed discards an
// inconsistent journal instead of failing.
func (bc *BlockChain) EnableQMDBHistory(reseed bool) {
	bc.qmdbHistoryRequested = true
	bc.qmdbHistoryReseed = reseed
	if bc.qmdbRootComputer != nil {
		bc.qmdbRootComputer.EnableLiveHistory()
	}
}

// QMDBHistoryActive reports whether blocks are being journaled.
func (bc *BlockChain) QMDBHistoryActive() bool { return bc.qmdbHistoryActive.Load() }

// prepareQMDBHistory runs the journal's startup check once the applied head is
// final (after the startup repairs/reverts).
func (bc *BlockChain) prepareQMDBHistory() error {
	if !bc.qmdbHistoryRequested {
		return nil
	}
	if !bc.qmdbEnabled || bc.qmdbRootComputer == nil {
		return fmt.Errorf("--state.qmdb-history requires the QMDB state commitment")
	}
	var start uint64
	err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		applied, err := qmdbHistoryAppliedHead(tx)
		if err != nil {
			return err
		}
		start, err = bc.qmdbRootComputer.PrepareLiveHistory(tx, applied, bc.qmdbHistoryReseed)
		return err
	})
	if err != nil {
		return fmt.Errorf("QMDB history: %w", err)
	}
	bc.qmdbHistoryActive.Store(true)
	log.Info("QMDB full-history journal active", "earliestProvable", start)
	return nil
}

// qmdbHistoryAppliedHead is the height whose post-state the loaded tree
// holds. Without the applied marker it is genesis only when the chain head is
// genesis; seeding a non-genesis tree as "block 0" would attach the head's
// state to height 0, so that is refused.
func qmdbHistoryAppliedHead(tx kv.Tx) (uint64, error) {
	applied, _, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil || ok {
		return applied, err
	}
	if n := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); n != nil && *n > 0 {
		return 0, fmt.Errorf("%w: the database has no QMDB applied-head marker but the chain head is block %d, so the height of the loaded state is unknown; run the node once without --state.qmdb-history to let it write the marker, or rebuild with replay-v2 --tree qmdb --qmdb-history",
			commitment.ErrQMDBHistoryUnavailable, *n)
	}
	return 0, nil
}

// journalQMDBBlock journals a just-written block in its write tx. A journal
// that disagrees with the undo record is marked broken from this block on (the
// prover then refuses those heights) instead of failing the block.
func (bc *BlockChain) journalQMDBBlock(tx kv.RwTx, block uint64, undo *qmdb.BlockUndo) error {
	if !bc.qmdbHistoryActive.Load() {
		return nil
	}
	err := bc.qmdbRootComputer.RecordBlockHistory(tx, block, undo)
	if err == nil {
		return nil
	}
	if !commitment.IsQMDBHistoryJournalError(err) {
		return fmt.Errorf("journaling QMDB history for block %d: %w", block, err)
	}
	log.Error("QMDB history journal broken: heights from this block on are no longer provable from history",
		"block", block, "err", err)
	return commitment.MarkQMDBHistoryBroken(tx, block)
}

// unjournalQMDBBlock removes an unwound block's journal in the unwind's tx.
// No-op unless the flag is on and the database carries a journal. `active`
// is the flag (requested), not the post-check state: the startup repairs
// (speculative revert, realign) unwind blocks BEFORE the journal check runs,
// and an unwound block must leave the journal with it.
func unjournalQMDBBlock(tx kv.RwTx, active bool, block uint64, undo *qmdb.BlockUndo) error {
	if !active {
		return nil
	}
	start, ok, err := commitment.ReadQMDBHistoryStart(tx)
	if err != nil || !ok {
		return err
	}
	if block >= start && undo == nil {
		// Without the undo record the journal can only be unwound if the
		// block left no journal record (never journaled, or changed nothing).
		journaled, err := commitment.NewMDBXQMDBHistoryStore(tx).HasBlockPos(block)
		if err != nil {
			return err
		}
		if !journaled {
			return nil
		}
	}
	if block < start || undo == nil {
		// Below a mid-chain seed (its stamps are not per-block) or no undo
		// record: the journal cannot be unwound exactly.
		log.Error("QMDB history journal broken by an unwind it cannot follow", "block", block, "start", start, "haveUndo", undo != nil)
		// Heights from the unwound block on are unreliable (from the start
		// when the unwind went below it).
		from := block
		if from < start {
			from = start
		}
		return commitment.MarkQMDBHistoryBroken(tx, from)
	}
	err = commitment.RevertQMDBBlockHistory(tx, block, undo)
	if err == nil {
		return nil
	}
	if !commitment.IsQMDBHistoryJournalError(err) {
		return fmt.Errorf("unwinding QMDB history of block %d: %w", block, err)
	}
	log.Error("QMDB history journal broken by an unwind", "block", block, "err", err)
	return commitment.MarkQMDBHistoryBroken(tx, block)
}

// readQMDBUndo loads one block's undo record (nil when absent).
func readQMDBUndo(tx kv.Getter, block uint64) (*qmdb.BlockUndo, error) {
	raw, err := tx.GetOne(modules.QMDBUndoWindow, modules.EncodeBlockNumber(block))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	return qmdb.UnmarshalBlockUndo(raw)
}
