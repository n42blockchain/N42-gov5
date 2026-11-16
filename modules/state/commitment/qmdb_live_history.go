// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Live-node QMDB full-history journal (opt-in, --state.qmdb-history).
//
// The block write journals each block from its undo record in the block's own
// tx (qmdb.RecordBlockHistory); every unwind of a persisted block removes its
// journal again in the unwind's tx (qmdb.RevertBlockHistory). The tables are
// the ones the offline replay engine fills with --qmdb-history, so a database
// rebuilt by replay-v2 --qmdb-history continues seamlessly on a live node.
//
// Two DatabaseInfo markers describe the journal:
//
//	qmdbHistoryStart  BE8(block) the earliest height the journal covers
//	qmdbHistoryBroken BE8(block) the first height whose journal could not be
//	                  written or reverted exactly; heights >= it are refused

package commitment

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

var (
	qmdbHistoryStartKey  = []byte("qmdbHistoryStart")
	qmdbHistoryBrokenKey = []byte("qmdbHistoryBroken")
)

// ErrQMDBHistoryUnavailable is returned by the startup check when the
// database cannot serve a complete history journal.
var ErrQMDBHistoryUnavailable = errors.New("qmdb history unavailable")

func readBE8Marker(tx kv.Getter, key []byte) (uint64, bool, error) {
	v, err := tx.GetOne(modules.DatabaseInfo, key)
	if err != nil || len(v) == 0 {
		return 0, false, err
	}
	if len(v) != 8 {
		return 0, false, fmt.Errorf("invalid %s marker length %d", key, len(v))
	}
	return binary.BigEndian.Uint64(v), true, nil
}

// ReadQMDBHistoryStart returns the earliest height the journal covers.
func ReadQMDBHistoryStart(tx kv.Getter) (uint64, bool, error) {
	return readBE8Marker(tx, qmdbHistoryStartKey)
}

// WriteQMDBHistoryStart records the earliest height the journal covers.
func WriteQMDBHistoryStart(tx kv.Putter, block uint64) error {
	return tx.Put(modules.DatabaseInfo, qmdbHistoryStartKey, be8h(block))
}

// ReadQMDBHistoryBroken returns the first height whose journal is unreliable.
func ReadQMDBHistoryBroken(tx kv.Getter) (uint64, bool, error) {
	return readBE8Marker(tx, qmdbHistoryBrokenKey)
}

// MarkQMDBHistoryBroken records that heights >= block are not provable from
// the journal. An earlier existing mark is kept.
func MarkQMDBHistoryBroken(tx kv.RwTx, block uint64) error {
	if cur, ok, err := ReadQMDBHistoryBroken(tx); err != nil {
		return err
	} else if ok && cur <= block {
		return nil
	}
	return tx.Put(modules.DatabaseInfo, qmdbHistoryBrokenKey, be8h(block))
}

// IsQMDBHistoryJournalError reports whether err means the journal and the undo
// record disagree (as opposed to a storage failure): the caller marks the
// journal broken instead of failing the block.
func IsQMDBHistoryJournalError(err error) bool {
	return errors.Is(err, qmdb.ErrHistoryUndoIncomplete) || errors.Is(err, qmdb.ErrHistoryInconsistent)
}

// EnableLiveHistory switches the computer to the live history journal:
// dead entry rows stay on disk (historical values), and the block write calls
// RecordBlockHistory. No inline recorder is attached, so ApplyUndo and the
// incremental reload keep working.
func (r *QMDBRootComputer) EnableLiveHistory() {
	r.readers.Lock()
	defer r.readers.Unlock()
	r.liveHistory = true
	r.t.SetRetainDeadEntryRows(true)
}

// LiveHistoryEnabled reports whether EnableLiveHistory was called.
func (r *QMDBRootComputer) LiveHistoryEnabled() bool {
	r.readers.RLock()
	defer r.readers.RUnlock()
	return r.liveHistory
}

// RecordBlockHistory journals `block` (whose post-state the live tree holds)
// from its undo record, through tx. A block that changed nothing writes no
// rows: the floor lookups resolve it to the previous record.
func (r *QMDBRootComputer) RecordBlockHistory(tx kv.RwTx, block uint64, undo *qmdb.BlockUndo) error {
	r.readers.Lock()
	defer r.readers.Unlock()
	next := r.t.NextSlot()
	if qmdb.UndoIsEmpty(undo) && undo.PrevNextSlot == next {
		return nil
	}
	return qmdb.RecordBlockHistory(NewMDBXQMDBHistoryStore(tx), block, undo, next, r.t.TopBandRecord())
}

// RevertQMDBBlockHistory removes `block`'s journal using its undo record.
func RevertQMDBBlockHistory(tx kv.RwTx, block uint64, undo *qmdb.BlockUndo) error {
	return qmdb.RevertBlockHistory(NewMDBXQMDBHistoryStore(tx), block, undo)
}

// PrepareLiveHistory is the startup consistency check for --state.qmdb-history,
// run after the forest is loaded and before any block is processed. The tree
// must hold the post-state of the applied head.
//
//   - Journal present (start marker, or rows from block <= 1 written by a
//     replay-v2 --qmdb-history rebuild): it must end exactly at the applied
//     head (its cursor there equals the tree's, nothing recorded above).
//   - No journal at all: one is seeded at the applied head (qmdb.SeedHistory)
//     and heights >= that head become provable. On a new chain this is
//     genesis, i.e. complete history.
//   - Journal inconsistent with the state (a gap because the node ran
//     without the flag, records above the head, history from mid-chain
//     without a marker): refused with ErrQMDBHistoryUnavailable, unless
//     reseed is set, which discards the old journal and seeds a new one at
//     the applied head.
//
// Returns the earliest provable height.
func (r *QMDBRootComputer) PrepareLiveHistory(tx kv.RwTx, appliedNum uint64, reseed bool) (uint64, error) {
	r.readers.Lock()
	defer r.readers.Unlock()
	store := NewMDBXQMDBHistoryStore(tx)
	start, haveStart, err := ReadQMDBHistoryStart(tx)
	if err != nil {
		return 0, err
	}
	first, last, haveRows, err := store.BlockPosRange()
	if err != nil {
		return 0, err
	}
	if !haveStart && !haveRows {
		return r.seedLiveHistoryLocked(tx, appliedNum)
	}
	problem := r.journalProblemLocked(store, haveStart, start, haveRows, first, last, appliedNum)
	if problem == "" {
		if !haveStart {
			start = first
			if err := WriteQMDBHistoryStart(tx, start); err != nil {
				return 0, err
			}
		}
		return start, nil
	}
	if !reseed {
		return 0, fmt.Errorf("%w: %s. The journal cannot be completed in place: rebuild the database from empty with the flag on (or replay-v2 --tree qmdb --qmdb-history), or restart with --state.qmdb-history.reseed to discard it and keep history from block %d on",
			ErrQMDBHistoryUnavailable, problem, appliedNum)
	}
	for _, table := range []string{modules.QMDBDeathStamps, modules.QMDBBlockPos, modules.QMDBKeyVers, modules.QMDBTopBand} {
		if err := tx.ClearBucket(table); err != nil {
			return 0, fmt.Errorf("discarding the QMDB history journal: %w", err)
		}
	}
	for _, key := range [][]byte{qmdbHistoryStartKey, qmdbHistoryBrokenKey} {
		if err := tx.Delete(modules.DatabaseInfo, key); err != nil {
			return 0, err
		}
	}
	return r.seedLiveHistoryLocked(tx, appliedNum)
}

// journalProblemLocked describes why an existing journal cannot continue at
// the applied head ("" when it can).
func (r *QMDBRootComputer) journalProblemLocked(store *MDBXQMDBHistoryStore, haveStart bool, start uint64, haveRows bool, first, last, appliedNum uint64) string {
	switch {
	case !haveRows:
		return fmt.Sprintf("start marker at block %d but no journal rows", start)
	case !haveStart && first > 1:
		return fmt.Sprintf("unmarked journal starting at block %d, not at genesis", first)
	case last > appliedNum:
		return fmt.Sprintf("the journal reaches block %d above the applied head %d (blocks were unwound while the flag was off)", last, appliedNum)
	}
	e, ok, err := store.FloorBlockPos(appliedNum)
	if err != nil {
		return err.Error()
	}
	if next := r.t.NextSlot(); !ok || e != next {
		return fmt.Sprintf("the journal ends at block %d (cursor %d) but the tree at applied head %d has cursor %d: the node ran without --state.qmdb-history", last, e, appliedNum, next)
	}
	return ""
}

func (r *QMDBRootComputer) seedLiveHistoryLocked(tx kv.RwTx, appliedNum uint64) (uint64, error) {
	// Seeding reads live keys back from their entries and may hydrate twigs:
	// fault through this tx, and detach afterwards (the tx dies with the
	// caller; the next block re-points the readers).
	r.setColdLocked(tx)
	defer r.setColdLocked(nil)
	if err := qmdb.SeedHistory(NewMDBXQMDBHistoryStore(tx), r.t, appliedNum); err != nil {
		return 0, fmt.Errorf("seeding the QMDB history journal at block %d: %w", appliedNum, err)
	}
	if err := WriteQMDBHistoryStart(tx, appliedNum); err != nil {
		return 0, err
	}
	return appliedNum, nil
}

// QMDBHistoryRange reads the journal bounds for the prover: earliest
// provable height, and the first broken height (ok=false: none).
func QMDBHistoryRange(tx kv.Tx) (start uint64, haveStart bool, broken uint64, haveBroken bool, err error) {
	start, haveStart, err = ReadQMDBHistoryStart(tx)
	if err != nil {
		return
	}
	broken, haveBroken, err = ReadQMDBHistoryBroken(tx)
	return
}
