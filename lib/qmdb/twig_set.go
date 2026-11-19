// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Changed-twig set (r96 stage 1, N42_QMDB_FLUSH_TWIG_SET). FlushTo's meta
// loop and EvictTwigsThrough each used to walk EVERY twig in the forest per
// block (36k-65k on the bench chain, growing with history) to find the few
// dozen that have something to do. Both apply a per-twig rule that is only
// ever true for a twig in one of three states:
//
//	FlushTo:           resident (nodes != nil) || pruned || metaDirty
//	EvictTwigsThrough: resident && !pruned && !dirty, and id < through/TwigSize
//
// The evict rule implies "resident", so both rules select a subset of
//
//	C = { id : nodes != nil || pruned || metaDirty }
//
// The tree keeps a superset of C (twigSet.ids) and both walks visit only it,
// applying the SAME per-twig rule as the full scan in ascending id order:
// the rows written, their order, and the twigs evicted are the full scan's
// by construction, provided the set is a superset of C. The superset is
// kept by noting a twig at every transition INTO C:
//
//   - a twig is created resident: twigFor (append, youngest-twig rollover,
//     shard_apply's pre-grow), the tombstone import's appends;
//   - an evicted twig is rehydrated: ensureHydrated (proofs, undo scratch,
//     writes to a sealed twig, verify_persisted);
//   - metaDirty becomes true: setTwigBit (every append and deactivation,
//     including the dead-row path), ApplyUndo's truncation, boundary
//     reinstall and revivals;
//   - pruned becomes true: markPruned (Compact), the tombstone import.
//
// Paths that replace twigs wholesale do not note: they invalidate the set
// and the next walk rebuilds it with one full scan: resetForLoad (LoadFrom,
// LoadFromTrustedIndex, the reload after a failed write), LoadIncremental.
// A new tree (New, FromSnapshotLog, ImportPortableSnapshot) starts
// invalid. With the switch off the set is not kept at all (noteTwig is a
// no-op on an invalid set).
//
// Members leave the set only when they fail the C rule at a compaction
// (start of each set-driven walk), so a twig that leaves C (evicted and
// meta-clean, or truncated away by ApplyUndo) re-enters only through one of
// the noted transitions above. Things that do NOT change C membership and
// therefore need no note: a twig becoming full (it stays resident until
// evicted), the staged window and AbortFlush (they change cursors and the
// dead-row list, not twig state; AbortFlush does not restore metaDirty,
// exactly as with the full scan), the evict-lag ring and retainedEntries
// (they change the `through` the caller passes, which both walks apply
// identically), foldTouched (runs before either walk, writes leaves of
// resident twigs only), the dead-row reclaim itself (rows, not twig state).
//
// Locking: the set lives in the Tree and is mutated only where the twig
// fields it mirrors are mutated, so it is protected by whatever serialises
// the tree's mutations. In the node that is QMDBRootComputer.readers held
// exclusively (lockWriter: ComputeRoot, FlushTo/StartFlushTo, EvictFlushed,
// CommitFlushed, ApplyUndo, LoadFrom); readers under RLock (Lookup/GetVia)
// never touch twigs. The parallel apply workers (shard_apply,
// apply_parallel) write leaf hashes into pre-grown resident twigs only; the
// noted transitions all run on the owner goroutine.
//
// N42_QMDB_FLUSH_TWIG_SET=check runs both walks every time and compares the
// selected twig ids; a difference panics in a test binary and otherwise logs
// an error, uses the full scan's selection and rebuilds the set.

package qmdb

import (
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/log"
)

// flushTwigSetMode is N42_QMDB_FLUSH_TWIG_SET: TwigWalkFull (unset/off),
// TwigWalkSet ("1", "true", "on", "yes") or TwigWalkCheck ("check").
var flushTwigSetMode atomic.Uint32

func init() { flushTwigSetMode.Store(uint32(parseFlushTwigSet(os.Getenv("N42_QMDB_FLUSH_TWIG_SET")))) }

func parseFlushTwigSet(v string) uint8 {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return TwigWalkSet
	case "check", "CHECK":
		return TwigWalkCheck
	}
	return TwigWalkFull
}

// SetFlushTwigSetForTest switches N42_QMDB_FLUSH_TWIG_SET ("", "1" or
// "check") and returns the restore.
func SetFlushTwigSetForTest(v string) func() {
	prev := flushTwigSetMode.Load()
	flushTwigSetMode.Store(uint32(parseFlushTwigSet(v)))
	return func() { flushTwigSetMode.Store(prev) }
}

// FlushTwigSetMode reports the active walk kind (TwigWalk*).
func FlushTwigSetMode() uint8 { return uint8(flushTwigSetMode.Load()) }

// twigSetMismatches counts check-mode differences (since start).
var twigSetMismatches atomic.Uint64

// TwigSetMismatches reports how many check-mode differences were found.
func TwigSetMismatches() uint64 { return twigSetMismatches.Load() }

// twigSet is a superset of the twigs FlushTo or EvictTwigsThrough can act
// on (see the file comment). ids may hold duplicates and ids that no longer
// exist; compaction removes both. While on is true, every twig of the forest
// whose inSet flag is set has its id in ids (noteTwig relies on it), and
// every twig in C has inSet set.
type twigSet struct {
	on  bool
	ids []int
}

// inTwigSetRule is the membership rule C: the union of FlushTo's and
// EvictTwigsThrough's per-twig rules.
func inTwigSetRule(tw *twig) bool { return flushWritesTwig(tw) }

// noteTwig records that twig id may have entered C.
func (t *Tree) noteTwig(tw *twig, id int) {
	if t.tset.on && !tw.inSet {
		tw.inSet = true
		t.tset.ids = append(t.tset.ids, id)
	}
}

// invalidateTwigSet drops the set; the next walk rebuilds it (or, with the
// switch off, runs without it).
func (t *Tree) invalidateTwigSet() {
	t.tset.on = false
	t.tset.ids = t.tset.ids[:0]
}

// rebuildTwigSet rebuilds the set with one full scan.
func (t *Tree) rebuildTwigSet() {
	ids := t.tset.ids[:0]
	for id, tw := range t.twigs {
		if tw == nil {
			continue
		}
		tw.inSet = inTwigSetRule(tw)
		if tw.inSet {
			ids = append(ids, id)
		}
	}
	t.tset.ids = ids
	t.tset.on = true
}

// useTwigSet reports whether this walk is set-driven (or checked), keeping
// the set's validity in step with the switch. rebuilt is true when the set
// had to be rebuilt by a full scan first.
func (t *Tree) useTwigSet(mode uint8) (use, rebuilt bool) {
	if mode == TwigWalkFull {
		if t.tset.on || len(t.tset.ids) > 0 {
			t.tset.on = false
			t.tset.ids = nil
		}
		return false, false
	}
	if !t.tset.on {
		t.rebuildTwigSet()
		rebuilt = true
	}
	return true, rebuilt
}

// compactTwigSet sorts the set ascending and drops duplicates, ids beyond
// the forest and twigs that left C. It returns how many entries it looked
// at.
func (t *Tree) compactTwigSet() int {
	ids := t.tset.ids
	n := len(ids)
	slices.Sort(ids)
	out := ids[:0]
	last := -1
	for _, id := range ids {
		if id == last {
			continue
		}
		last = id
		if id >= len(t.twigs) {
			continue
		}
		tw := t.twigs[id]
		if tw == nil {
			continue
		}
		if !inTwigSetRule(tw) {
			tw.inSet = false
			continue
		}
		tw.inSet = true
		out = append(out, id)
	}
	t.tset.ids = out
	return n
}

// twigSetMismatch reports a check-mode difference: a panic in a test
// binary, an error log in a node. The caller then uses the full scan and
// rebuilds the set.
func twigSetMismatch(walk string, full, set []int) {
	twigSetMismatches.Add(1)
	msg := fmt.Sprintf("qmdb: changed-twig set differs from the full scan in %s: full %d ids %v, set %d ids %v",
		walk, len(full), clipIDs(full), len(set), clipIDs(set))
	if testing.Testing() {
		panic(msg)
	}
	log.Error(msg + " -- using the full scan and rebuilding the set")
}

func clipIDs(ids []int) []int {
	if len(ids) > 16 {
		return ids[:16]
	}
	return ids
}
