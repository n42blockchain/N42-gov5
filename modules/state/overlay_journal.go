// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// overlay_journal.go — in-memory reverse-diff capture for snapshot-direct
// (minimal/full) nodes, enabling shallow-reorg unwind WITHOUT on-disk
// changesets. This is the geth "128 in-memory diff layers" model applied to
// the warm-overlay tier:
//
//   - The immutable H0 snapshot is never mutated, so the ONLY per-block state
//     that changes is the warm MDBX Account/Storage/Code tables.
//   - As a block executes, every warm-table Put/Delete is intercepted and the
//     PRE-image of the touched key (its warm-table entry before the write) is
//     recorded ONCE (first-wins) into that block's BlockRevDiff.
//   - Applying a BlockRevDiff back to the warm tables restores them to their
//     exact pre-block state — i.e. unwinds the block. Cold (snapshot) is
//     immutable, so restoring the warm tier restores the full overlay state.
//
// The node keeps the last N block diffs in a ring buffer (see reorgJournal);
// a tip reorg (parent mismatch, always depth-1 post-merge, never below
// finality) unwinds the orphaned blocks by replaying their diffs in reverse.

package state

import (
	"github.com/n42blockchain/N42/lib/kv"
)

// revEntry is one warm-table key's pre-image. present=false means the key was
// ABSENT before the block wrote it (restore = delete). present=true means it
// held old (possibly an empty-value tombstone — restore = Put(old)).
type revEntry struct {
	table   string
	key     []byte
	old     []byte
	present bool
}

// BlockRevDiff accumulates one block's warm-table pre-images (first-wins per
// key). Restore(tx) reverts the warm tables to their pre-block state.
type BlockRevDiff struct {
	entries []revEntry
	seen    map[string]struct{}
}

// NewBlockRevDiff returns an empty diff ready to capture a block.
func NewBlockRevDiff() *BlockRevDiff {
	return &BlockRevDiff{seen: make(map[string]struct{})}
}

// Len reports how many distinct keys the block touched (diagnostics).
func (d *BlockRevDiff) Len() int { return len(d.entries) }

// record captures the pre-image of (table,key) exactly once per block. The
// FIRST capture wins so the stored old value is the true pre-block value even
// if the key is written multiple times within the block.
func (d *BlockRevDiff) record(table string, key, old []byte, present bool) {
	id := table + string(key)
	if _, ok := d.seen[id]; ok {
		return
	}
	d.seen[id] = struct{}{}
	e := revEntry{table: table, key: append([]byte(nil), key...), present: present}
	if present {
		e.old = append([]byte(nil), old...)
	}
	d.entries = append(d.entries, e)
}

// Restore reverts the warm tables to their pre-block state by replaying the
// captured pre-images. Order within a single block's diff does not matter (each
// key appears once). The caller replays diffs newest-block-first when unwinding
// multiple blocks.
func (d *BlockRevDiff) Restore(tx kv.RwTx) error {
	for _, e := range d.entries {
		if e.present {
			if err := tx.Put(e.table, e.key, e.old); err != nil {
				return err
			}
		} else {
			if err := tx.Delete(e.table, e.key); err != nil {
				return err
			}
		}
	}
	return nil
}

// capturingPutDel wraps an RwTx so that every warm-table Put/Delete records its
// pre-image into diff before delegating. All other RwTx methods (Cursor,
// GetOne, …) pass through unchanged, so it transparently satisfies the putDel +
// cursorProvider surfaces OverlayStateWriter/PlainStateWriter require.
type capturingPutDel struct {
	kv.RwTx
	diff *BlockRevDiff
}

// NewCapturingTx wraps tx so warm writes are journaled into diff. Pass the
// result as the OverlayStateWriter's db.
func NewCapturingTx(tx kv.RwTx, diff *BlockRevDiff) kv.RwTx {
	return &capturingPutDel{RwTx: tx, diff: diff}
}

func (c *capturingPutDel) Put(table string, k, v []byte) error {
	if err := c.capture(table, k); err != nil {
		return err
	}
	return c.RwTx.Put(table, k, v)
}

func (c *capturingPutDel) Delete(table string, k []byte) error {
	if err := c.capture(table, k); err != nil {
		return err
	}
	return c.RwTx.Delete(table, k)
}

// capture reads the current warm entry for (table,key) and records it as the
// pre-image. Uses a cursor SeekExact — matching WarmOverlayReader — so the
// Storage table's DupSort AutoConv (52B composite -> 20B dup-key) is handled.
func (c *capturingPutDel) capture(table string, k []byte) error {
	if _, ok := c.diff.seen[table+string(k)]; ok {
		return nil // already captured this block; skip the read
	}
	cur, err := c.RwTx.Cursor(table)
	if err != nil {
		return err
	}
	kk, vv, err := cur.SeekExact(k)
	cur.Close()
	if err != nil {
		return err
	}
	if kk == nil {
		c.diff.record(table, k, nil, false)
		return nil
	}
	c.diff.record(table, k, vv, true)
	return nil
}
