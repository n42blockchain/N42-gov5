// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"time"

	"github.com/n42blockchain/N42/lib/qmdb"
)

// Writer-side holders of the tree's reader lock (r94a stamp). Run 80 found
// the three block-start system-call reads of an ahead run taking 6-7.5 ms
// p50 on blocks whose base prefill was detached, about 1 ms otherwise: a
// wait for the reader lock. Each method that takes it exclusively labels
// itself, so a timed lookup that waited can report who held the lock and
// for how long already.
const (
	holderNone uint32 = iota
	holderEvictLag
	holderChainPending
	holderPeel
	holderRevert
	holderRewind
	holderTakeUndo
	holderApplyUndo
	holderRootFold
	holderLoad
	holderReload
	holderFlush
	holderFlushAsync
	holderCommitFlushed
	holderAbortFlushed
	holderSetCold
	holderMdbxIndex
	holderIndexTx
	holderAdoptAppends
	holderEvict
	holderRoot
)

var holderNames = [...]string{"none", "evictLag", "chainPending", "peel", "revert", "rewind", "takeUndo",
	"applyUndo", "rootFold", "load", "reload", "flush", "flushAsync", "commitFlushed", "abortFlushed",
	"setCold", "mdbxIndex", "indexTx", "adoptAppends", "evict", "root"}

// QMDBHolderName names a holder code of LookupTiming.Holder.
func QMDBHolderName(code uint32) string {
	if int(code) < len(holderNames) {
		return holderNames[code]
	}
	return "unknown"
}

// lockWriter takes readers exclusively for code and returns the release.
func (r *QMDBRootComputer) lockWriter(code uint32) func() {
	r.readers.Lock()
	r.holderSince.Store(time.Now().UnixNano())
	r.holder.Store(code)
	return func() {
		r.holder.Store(holderNone)
		r.readers.Unlock()
	}
}

// lookupWaitNote is the threshold over which a timed lookup counts as having
// waited for the lock (and records the holder).
const lookupWaitNote = 50 * time.Microsecond

// LookupTimed is Lookup accumulating into t (when set) the wait for the
// reader lock, separately from the read, and the writer holding the lock
// when a wait began.
func (r *QMDBRootComputer) LookupTimed(keyHash qmdb.Hash, cold qmdb.Getter, t *LookupTiming) (value []byte, found bool, evicted bool) {
	if t == nil {
		return r.Lookup(keyHash, cold)
	}
	t0 := time.Now()
	holder, since := r.holder.Load(), r.holderSince.Load()
	r.readers.RLock()
	t1 := time.Now()
	w := t1.Sub(t0)
	if w > lookupWaitNote {
		qmdbLookupWaitNanos.Add(w.Nanoseconds())
		t.Waits.Add(1)
		if holder != holderNone {
			t.Holder.Store(holder)
			if since > 0 {
				t.HolderAge.Store(t0.UnixNano() - since)
			}
		}
	}
	t.Wait.Add(int64(w))
	defer r.readers.RUnlock()
	var cr qmdb.ColdReader = noColdReader{}
	if cold != nil {
		cr = qmdb.ColdReaderFromGetter(cold)
	}
	return r.t.GetVia(keyHash, cr)
}
