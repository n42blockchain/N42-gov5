// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// lastfull.go — the per-path FULL/DIFF bookkeeping the builder uses to decide
// each node record's form (a build-side state machine, not part of the on-disk
// contract).
//
// A FULL node record is written when no prior record exists (or it was a
// tombstone) or every fullEvery-th epoch, bounding the reader's DIFF walk-back.
// The account trie's path space is dense and stays a plain map; the storage
// trie's is sparse and unbounded, so it lives in a capped cache whose misses
// are answered by reading the already-written records back (readBackLastFull).

package main

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
)

// nodeRecState tracks the last node record written for a path.
type nodeRecState struct {
	lastFullEpoch uint32
	exists        bool // false = no record yet or last was a tombstone
}

// lastFullCache is a capped nodeRecState map whose misses are answered from
// the already-written node records (floor scan + ≤fullEvery walk-back to the
// last FULL). Entries written during the CURRENT batch are never evicted —
// their rows may still sit uncommitted in the sorted write buffer, where the
// read-back cannot see them.
type lastFullCache struct {
	m       map[string]lfEntry
	cap     int
	seq     uint32
	curN    int    // entries carrying the current seq (pinned, not evictable)
	missRB  uint64 // read-backs performed (stats)
	resumed bool   // resumed build: read-back results are degraded (see lfEntry)
}

type lfEntry struct {
	nodeRecState
	seq uint32
	// degraded marks a read-back result in a RESUMED build: the in-RAM epoch
	// dirty bitmaps were lost at restart, so a DIFF written for this path's
	// first post-restart flush could under-report changed children. The first
	// flush is forced FULL instead (complete state, no bitmap reliance).
	degraded bool
}

func newLastFullCache(capEntries int) *lastFullCache {
	return &lastFullCache{m: make(map[string]lfEntry, 1<<16), cap: capEntries}
}

func (c *lastFullCache) newBatch() { c.seq++; c.curN = 0 }

func (c *lastFullCache) put(pk string, st nodeRecState) {
	c.putEntry(pk, lfEntry{nodeRecState: st, seq: c.seq})
}

func (c *lastFullCache) putEntry(pk string, ne lfEntry) {
	st := ne.nodeRecState
	if e, ok := c.m[pk]; ok {
		if e.seq != c.seq {
			c.curN++
		}
		c.m[pk] = lfEntry{nodeRecState: st, seq: c.seq, degraded: ne.degraded}
		return
	}
	// Evict only when there ARE unpinned entries — a heavy batch can pin more
	// than the whole cap, and scanning an all-pinned map on every insert is a
	// quadratic stall (the 6M A/B froze exactly there). When everything is
	// pinned the map simply grows past cap until newBatch unpins it.
	if evictable := len(c.m) - c.curN; len(c.m) >= c.cap && evictable > 0 {
		drop := c.cap / 8
		if drop > evictable {
			drop = evictable
		}
		for k, e := range c.m {
			if e.seq == c.seq {
				continue
			}
			delete(c.m, k)
			if drop--; drop <= 0 {
				break
			}
		}
	}
	c.m[pk] = lfEntry{nodeRecState: st, seq: c.seq, degraded: ne.degraded}
	c.curN++
}

// get returns the path's last-record state (and whether it is degraded — a
// resumed build's first post-restart flush must be FULL), reading it back
// from the node table on a cache miss. `epoch` is the epoch about to be
// written — the floor scan looks strictly below it.
func (c *lastFullCache) get(tx kv.Tx, table string, pk string, epoch uint64) (nodeRecState, bool, error) {
	if e, ok := c.m[pk]; ok {
		return e.nodeRecState, e.degraded, nil
	}
	c.missRB++
	st, err := readBackLastFull(tx, table, []byte(pk), epoch)
	if err != nil {
		return nodeRecState{}, false, err
	}
	degraded := c.resumed && st.exists
	c.putEntry(pk, lfEntry{nodeRecState: st, seq: c.seq, degraded: degraded})
	return st, degraded, nil
}

// readBackLastFull reconstructs nodeRecState from the committed records of
// one path: floor record below `epoch`, then ≤fullEvery steps back to the
// last FULL when the floor is a DIFF.
func readBackLastFull(tx kv.Tx, table string, path []byte, epoch uint64) (nodeRecState, error) {
	prefix := make([]byte, 0, 1+len(path))
	prefix = append(prefix, byte(len(path)))
	prefix = append(prefix, path...)
	cur, err := tx.Cursor(table)
	if err != nil {
		return nodeRecState{}, err
	}
	defer cur.Close()
	seek := make([]byte, 0, len(prefix)+4)
	seek = append(seek, prefix...)
	seek = binary.BigEndian.AppendUint32(seek, uint32(epoch))
	k, v, err := cur.Seek(seek)
	if err != nil {
		return nodeRecState{}, err
	}
	if k == nil {
		k, v, err = cur.Last()
	} else {
		k, v, err = cur.Prev()
	}
	for {
		if err != nil {
			return nodeRecState{}, err
		}
		if k == nil || !bytesEqualPrefix(k, prefix) || len(k) != len(prefix)+4 {
			return nodeRecState{}, nil // no record below epoch: never existed
		}
		if len(v) == 0 {
			return nodeRecState{}, nil // floor is a tombstone
		}
		if v[0] == nodeRecFull {
			return nodeRecState{lastFullEpoch: binary.BigEndian.Uint32(k[len(prefix):]), exists: true}, nil
		}
		// DIFF: keep walking back to its FULL anchor (bounded by fullEvery).
		// `exists` is true (the floor record is live); only lastFullEpoch is
		// still unknown.
		pk, pv, perr := cur.Prev()
		if perr != nil {
			return nodeRecState{}, perr
		}
		if pk == nil || !bytesEqualPrefix(pk, prefix) || len(pk) != len(prefix)+4 || len(pv) == 0 {
			// Chain truncated (shouldn't happen for a valid DB): degrade to
			// "due for a FULL now" — safe, just a larger next record.
			return nodeRecState{lastFullEpoch: 0, exists: true}, nil
		}
		k, v, err = pk, pv, nil
	}
}
