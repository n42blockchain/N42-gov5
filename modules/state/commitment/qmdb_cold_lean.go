// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"encoding/binary"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
)

// SetCold without waiting for the tree's lock (r97a I1,
// N42_IMPORT_SETCOLD_LEAN, default off).
//
// What SetCold mutates: the tree's two transaction-bound read sources, t.cold
// (the cold entry reader) and t.leafStore (the twig leaf-blob source). It
// touches no entry, twig, index, cursor or twig-set field, so the twig set's
// rule "changed only under the exclusive lock" (lib/qmdb/twig_set.go) is not
// involved.
//
// Who reads them: only code running with readers held EXCLUSIVELY -- the
// writer-side methods (ComputeRoot's apply and cold faults, FlushTo /
// StartFlushTo's goroutine: EvictTwigsThrough's cold check, FlushTo's
// leaf-store checks; ApplyUndo, the reloads, RevertBlock, ...). The
// shared-lock readers never do: Lookup/LookupLocked/LookupTimed pass their
// own transaction (or noColdReader) to GetVia, and the RLock accessors
// (NextSlot, FlushedThrough, PendingBuilds, ...) read no read source. One
// reader runs WITHOUT any lock: ValueSource() (the N42_STATE_READ_QMDB reader
// of an import that did not execute ahead), whose Tree.Get faults through
// t.cold; today it is ordered after the previous block's capture only
// because SetCold waits for that capture. The import therefore uses the lean
// path only for a block executed ahead (its readers are lock-taking lookup
// sources with their own transaction) and keeps the blocking SetCold
// otherwise (internal/blockchain.go).
//
// The new synchronisation: SetColdLean tries the lock; free, it installs the
// source at once as SetCold does. Held (the previous block's capture, a
// lookup span), it parks the source in coldNext (an atomic pointer) and
// returns. Every exclusive section adopts a parked source first, right after
// it took the lock and before its own body (lockWriter, and the four direct
// Lock sites of qmdb_live_history.go).
//
// Why every reader sees the cold layer it would see today: today SetCold is
// its own exclusive section S, ordered by the lock against every other
// exclusive section. Here S's body (two field stores) runs at the start of
// the first exclusive section X that takes the lock after the park; X then
// sees exactly the state "S, then X", one of the orders today's lock already
// allows (S and X both waited for the same holder H; a sync.RWMutex picks
// either). Sections that held or took the lock before the park (H itself,
// e.g. the capture of the previous block) see the previous source, as they
// do today when they precede S. No section ever sees a source mid-swap
// (the swap is inside an exclusive section), and the tree state a section
// sees never depends on whether the swap was parked: the swap changes no
// tree state. Hence no reader pairs a new cold layer with an old tree (or an
// old layer with a new tree) in a combination today's ordering cannot
// produce, and the eviction cursor a cold read relies on (slots below
// entriesBase are in the persisted log the transaction sees) holds exactly
// as often as it holds today: check mode verifies it at every adoption.
// A parked source whose transaction closes before any section adopted it
// (the import failed before its root) leaves a closed transaction attached,
// as today's SetCold does after the import's tx closes; the next SetCold
// replaces it before the next block reads.
//
// =check additionally verifies at every install (immediate or adopted) that
// the source's persisted cursor (qmdbMeta nextSlot) covers the tree's
// resident-window base, i.e. that every evicted slot is visible through it;
// a miss is counted and logged (a panic in a test binary).

const (
	coldLeanOff uint32 = iota
	coldLeanOn
	coldLeanCheck
)

var coldLeanMode atomic.Uint32

func init() { coldLeanMode.Store(parseColdLean(os.Getenv("N42_IMPORT_SETCOLD_LEAN"))) }

func parseColdLean(v string) uint32 {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return coldLeanOn
	case "check", "CHECK":
		return coldLeanCheck
	}
	return coldLeanOff
}

// ImportSetColdLeanOn reports whether N42_IMPORT_SETCOLD_LEAN is on (1 or
// check).
func ImportSetColdLeanOn() bool { return coldLeanMode.Load() != coldLeanOff }

// SetImportSetColdLeanForTest switches N42_IMPORT_SETCOLD_LEAN ("", "1" or
// "check") and returns the restore.
func SetImportSetColdLeanForTest(v string) func() {
	prev := coldLeanMode.Load()
	coldLeanMode.Store(parseColdLean(v))
	return func() { coldLeanMode.Store(prev) }
}

// coldLeanMisses counts check-mode installs whose source did not cover the
// resident window's base; coldLeanChecks counts the checks that could read
// the cursor.
var coldLeanMisses, coldLeanChecks atomic.Uint64

// ColdLeanCheckCounts reports the check mode's checks and misses.
func ColdLeanCheckCounts() (checks, misses uint64) {
	return coldLeanChecks.Load(), coldLeanMisses.Load()
}

// coldBinding is a parked SetCold.
type coldBinding struct {
	g  qmdb.Getter
	at time.Time
}

// SetColdLean is SetCold that does not wait for the tree's lock: a held lock
// parks g for the next exclusive section (see the file comment). It reports
// whether it parked. With N42_IMPORT_SETCOLD_LEAN off it is SetCold.
func (r *QMDBRootComputer) SetColdLean(g qmdb.Getter) bool {
	if coldLeanMode.Load() == coldLeanOff {
		r.SetCold(g)
		return false
	}
	t0 := time.Now()
	holder := r.holder.Load()
	if r.readers.TryLock() {
		r.holderSince.Store(t0.UnixNano())
		r.holder.Store(holderSetCold)
		r.adoptColdLocked(holderSetCold) // an older parked source first
		tw := time.Now()
		r.installColdLocked(g)
		r.lastSetCold.Store(&SetColdStamp{Work: time.Since(tw)})
		r.holder.Store(holderNone)
		r.readers.Unlock()
		return false
	}
	r.lastSetCold.Store(&SetColdStamp{Holder: holder, Parked: true, Work: time.Since(t0)})
	r.coldNext.Store(&coldBinding{g: g, at: t0})
	coldLeanParks.Add(1)
	return true
}

// coldLeanParks counts parked SetColdLean calls; coldLeanAdopts the
// adoptions (process lifetime).
var coldLeanParks, coldLeanAdopts atomic.Uint64

// ColdLeanCounts reports the parked calls and their adoptions.
func ColdLeanCounts() (parks, adopts uint64) { return coldLeanParks.Load(), coldLeanAdopts.Load() }

// adoptColdLocked installs a parked source. Caller holds readers
// exclusively; by is its holder code (stamp only).
func (r *QMDBRootComputer) adoptColdLocked(by uint32) {
	if r.coldNext.Load() == nil {
		return
	}
	b := r.coldNext.Swap(nil)
	if b == nil {
		return
	}
	r.installColdLocked(b.g)
	coldLeanAdopts.Add(1)
	if s := r.lastSetCold.Load(); s != nil && s.Parked && s.AdoptLag == 0 {
		c := *s
		c.AdoptLag, c.AdoptBy = time.Since(b.at), by
		r.lastSetCold.Store(&c)
	}
}

// installColdLocked is setColdLocked plus the check mode's cursor check.
func (r *QMDBRootComputer) installColdLocked(g qmdb.Getter) {
	r.setColdLocked(g)
	if g != nil && coldLeanMode.Load() == coldLeanCheck {
		r.checkColdCoversLocked(g)
	}
}

// checkColdCoversLocked verifies that g's persisted cursor covers the tree's
// resident-window base: every slot a cold read can be asked for (below the
// base) is in the log g sees.
func (r *QMDBRootComputer) checkColdCoversLocked(g qmdb.Getter) {
	raw, err := g.GetOne(qmdb.MetaTable, []byte("nextSlot"))
	if err != nil || len(raw) != 8 {
		return // nothing persisted (fresh or in-memory store): no evicted slot either
	}
	coldLeanChecks.Add(1)
	persisted, base := binary.BigEndian.Uint64(raw), r.t.EntriesBase()
	if persisted >= base {
		return
	}
	coldLeanMisses.Add(1)
	if testing.Testing() {
		panic("qmdb SetColdLean check: cold source does not cover the evicted window")
	}
	log.Error("qmdb SetColdLean check: cold source does not cover the evicted window",
		"persistedNextSlot", persisted, "entriesBase", base, "flushedThrough", r.flushedThrough)
}
