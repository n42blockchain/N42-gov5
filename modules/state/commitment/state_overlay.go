// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// StateOverlay — a 4-table RAM read/write layer (HashedAccounts, HashedStorage,
// TrieOfAccounts, TrieOfStorage) used ONLY by the concurrent root computer
// (--concurrent-root). The serial path keeps using the 2-table TrieOverlay in
// trie_overlay.go; this file is additive and does not touch it.
//
// Why a 4-table overlay: the parallel per-window root runs 16 nibble shards,
// each on its OWN read transaction. A worker's RoTx sees only COMMITTED state,
// so it would miss the current window's uncommitted HashedAccounts/HashedStorage
// writes AND this batch's TrieOf* node updates. Routing all four tables through
// an in-RAM overlay lets each worker read base-RoTx ⊕ overlay (the overlay btree
// is iterated via an O(1) COW snapshot, so concurrent reads are race-free), and
// the build flushes the overlay to MDBX ONCE per commit batch — preserving the
// write-batching that the original TrieOverlay was built for (no extra commits).
//
// The merged cursor reuses the plain mergedCursor from trie_overlay.go for the
// three tables the loader drives with flat Seek/Next (HashedAccounts,
// TrieOfAccounts, TrieOfStorage). HashedStorage is the only table the loader
// reads with the DupSort surface (SeekBothRange/NextDup); stateDupCursor below
// implements exactly those two on top of the flat merged stream.
package commitment

import (
	"bytes"
	"fmt"

	"github.com/tidwall/btree"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

func errUnsupportedDup(m string) error {
	return fmt.Errorf("state overlay dup cursor: %s unsupported", m)
}

// StateOverlay holds pending writes for the four state/trie tables.
type StateOverlay struct {
	hAcc *btree.BTreeG[overlayKV] // HashedAccounts: key=addrHash(32) -> account V2
	hSto *btree.BTreeG[overlayKV] // HashedStorage:  key=addrHash||slotHash(64) -> storage value
	tAcc *btree.BTreeG[overlayKV] // TrieOfAccounts
	tSto *btree.BTreeG[overlayKV] // TrieOfStorage
}

func NewStateOverlay() *StateOverlay {
	// LOCKED btree (default): the parallel root fans 16 read-only workers that each
	// take an O(1) COW Copy() snapshot of these trees concurrently — locking makes
	// those concurrent Copy() calls race-free. All MUTATIONS happen single-threaded
	// on the main goroutine (Phase 1/2 writes before fan-out, Phase 4 node flush
	// after wg.Wait), with NO overlay cursor held across them, so the locked-tree
	// "cursor-across-delete self-deadlock" that forced NoLocks on the serial
	// TrieOverlay cannot occur here.
	opts := btree.Options{}
	return &StateOverlay{
		hAcc: btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
		hSto: btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
		tAcc: btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
		tSto: btree.NewBTreeGOptions[overlayKV](overlayLess, opts),
	}
}

func (o *StateOverlay) tree(table string) *btree.BTreeG[overlayKV] {
	switch table {
	case modules.HashedAccounts:
		return o.hAcc
	case modules.HashedStorage:
		return o.hSto
	case modules.TrieOfAccounts:
		return o.tAcc
	default: // modules.TrieOfStorage
		return o.tSto
	}
}

func (o *StateOverlay) Handles(table string) bool {
	return table == modules.HashedAccounts || table == modules.HashedStorage ||
		table == modules.TrieOfAccounts || table == modules.TrieOfStorage
}

func (o *StateOverlay) Put(table string, k, v []byte) {
	o.tree(table).Set(overlayKV{k: append([]byte{}, k...), v: append([]byte{}, v...)})
}

func (o *StateOverlay) Delete(table string, k []byte) {
	o.tree(table).Set(overlayKV{k: append([]byte{}, k...)}) // v=nil → tombstone
}

func (o *StateOverlay) Get(table string, k []byte) ([]byte, bool) {
	e, ok := o.tree(table).Get(overlayKV{k: k})
	if !ok {
		return nil, false
	}
	return e.v, true
}

func (o *StateOverlay) Len() int { return o.hAcc.Len() + o.hSto.Len() + o.tAcc.Len() + o.tSto.Len() }

// FlushTo applies pending writes to tx in sorted (near-append) order and clears
// the overlay. DupSort tables (HashedStorage, TrieOfStorage) keep the
// delete-before-put discipline so a path/key holds exactly its current value(s).
func (o *StateOverlay) FlushTo(tx kv.RwTx) error {
	// Plain tables: Put/Delete.
	for _, t := range []struct {
		name string
		tr   *btree.BTreeG[overlayKV]
	}{{modules.HashedAccounts, o.hAcc}, {modules.TrieOfAccounts, o.tAcc}} {
		it := t.tr.Iter()
		for ok := it.First(); ok; ok = it.Next() {
			e := it.Item()
			if e.v == nil {
				if err := tx.Delete(t.name, e.k); err != nil {
					it.Release()
					return err
				}
			} else if err := tx.Put(t.name, e.k, e.v); err != nil {
				it.Release()
				return err
			}
		}
		it.Release()
	}
	// DupSort tables: delete-before-put (full keys; AutoDupSort splits them).
	for _, t := range []struct {
		name string
		tr   *btree.BTreeG[overlayKV]
	}{{modules.HashedStorage, o.hSto}, {modules.TrieOfStorage, o.tSto}} {
		it := t.tr.Iter()
		for ok := it.First(); ok; ok = it.Next() {
			e := it.Item()
			if err := tx.Delete(t.name, e.k); err != nil {
				it.Release()
				return err
			}
			if e.v != nil {
				if err := tx.Put(t.name, e.k, e.v); err != nil {
					it.Release()
					return err
				}
			}
		}
		it.Release()
	}
	o.hAcc.Clear()
	o.hSto.Clear()
	o.tAcc.Clear()
	o.tSto.Clear()
	return nil
}

// ---------------------------------------------------------------------------
// per-worker read tx wrapper: base RoTx ⊕ shared StateOverlay (COW-snapshot reads)

type stateOverlayTx struct {
	kv.Tx // read-only base (per-worker RoTx); the loader only exercises the read surface
	o     *StateOverlay
}

// WrapStateOverlay returns a READ view = base ⊕ overlay for the four tables.
// base is a per-worker read-only tx; overlay writes are made directly on the
// *StateOverlay by the main thread (overlay.Put/Delete), not through this wrapper.
func WrapStateOverlay(base kv.Tx, o *StateOverlay) kv.Tx {
	return &stateOverlayTx{Tx: base, o: o}
}

// stateOverlayRwTx routes Put/Delete on the four tables into the overlay (so the
// EXISTING serial write logic — writeAccountsAndStorage, wipes, Phase-4 flush —
// populates the overlay unchanged), and passes reads/other tables through. Used
// by the main thread to fill the overlay before fanning out to read-only workers.
type stateOverlayRwTx struct {
	kv.RwTx
	o *StateOverlay
}

func WrapStateOverlayRW(base kv.RwTx, o *StateOverlay) kv.RwTx {
	return &stateOverlayRwTx{RwTx: base, o: o}
}

func (t *stateOverlayRwTx) Put(table string, k, v []byte) error {
	if t.o.Handles(table) {
		t.o.Put(table, k, v)
		return nil
	}
	return t.RwTx.Put(table, k, v)
}

func (t *stateOverlayRwTx) Delete(table string, k []byte) error {
	if t.o.Handles(table) {
		t.o.Delete(table, k)
		return nil
	}
	return t.RwTx.Delete(table, k)
}

func (t *stateOverlayRwTx) GetOne(table string, k []byte) ([]byte, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v, nil
		}
	}
	return t.RwTx.GetOne(table, k)
}

func (t *stateOverlayRwTx) Has(table string, k []byte) (bool, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v != nil, nil
		}
	}
	return t.RwTx.Has(table, k)
}

func (t *stateOverlayRwTx) Cursor(table string) (kv.Cursor, error) {
	c, err := t.RwTx.Cursor(table)
	if err != nil || !t.o.Handles(table) {
		return c, err
	}
	return newMergedCursor(c, t.o.tree(table)), nil
}

func (t *stateOverlayRwTx) CursorDupSort(table string) (kv.CursorDupSort, error) {
	c, err := t.RwTx.CursorDupSort(table)
	if err != nil {
		return nil, err
	}
	if !t.o.Handles(table) {
		return c, nil
	}
	mc := newMergedCursor(c, t.o.tree(table))
	if table == modules.HashedStorage {
		return &stateDupCursor{mergedCursor: mc}, nil
	}
	return &mergedDupCursor{mergedCursor: mc}, nil
}

func (t *stateOverlayTx) GetOne(table string, k []byte) ([]byte, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v, nil
		}
	}
	return t.Tx.GetOne(table, k)
}

func (t *stateOverlayTx) Has(table string, k []byte) (bool, error) {
	if t.o.Handles(table) {
		if v, ok := t.o.Get(table, k); ok {
			return v != nil, nil
		}
	}
	return t.Tx.Has(table, k)
}

func (t *stateOverlayTx) Cursor(table string) (kv.Cursor, error) {
	c, err := t.Tx.Cursor(table)
	if err != nil || !t.o.Handles(table) {
		return c, err
	}
	return newMergedCursor(c, t.o.tree(table)), nil
}

func (t *stateOverlayTx) CursorDupSort(table string) (kv.CursorDupSort, error) {
	c, err := t.Tx.CursorDupSort(table)
	if err != nil {
		return nil, err
	}
	if !t.o.Handles(table) {
		return c, nil
	}
	mc := newMergedCursor(c, t.o.tree(table))
	if table == modules.HashedStorage {
		// Only HashedStorage is read via the DupSort surface (SeekBothRange/NextDup).
		return &stateDupCursor{mergedCursor: mc}, nil
	}
	// TrieOfStorage: loader uses flat Seek/Next only.
	return &mergedDupCursor{mergedCursor: mc}, nil
}

// ---------------------------------------------------------------------------
// stateDupCursor — DupSort surface for the HashedStorage merged view.
//
// The flattened merged stream yields full keys addrHash(32)||slotHash(32) -> v.
// The loader expects the mdbx DupSort contract: SeekBothRange(addrHash, slotPfx)
// returns the DUP VALUE of the first slot >= slotPfx under addrHash, where the
// dup value is slotHash(32)||storageValue; NextDup advances within the same
// addrHash. We rebuild that dup value from the full key + value.

type stateDupCursor struct {
	*mergedCursor
	curAddr []byte // physical 32B key (addrHash) bounding NextDup
}

func (c *stateDupCursor) dupVal(fullKey, v []byte) []byte {
	// dup value = key suffix after the physical key (slotHash) || stored value.
	return append(append([]byte{}, fullKey[len(c.curAddr):]...), v...)
}

func (c *stateDupCursor) SeekBothRange(key, valPrefix []byte) ([]byte, error) {
	c.curAddr = append(c.curAddr[:0], key...)
	seek := append(append([]byte{}, key...), valPrefix...)
	k, v, err := c.mergedCursor.Seek(seek)
	if err != nil {
		return nil, err
	}
	if k == nil || !bytes.HasPrefix(k, key) {
		return nil, nil // no dup under this account at/after the prefix
	}
	return c.dupVal(k, v), nil
}

func (c *stateDupCursor) NextDup() ([]byte, []byte, error) {
	k, v, err := c.mergedCursor.Next()
	if err != nil {
		return nil, nil, err
	}
	if k == nil || !bytes.HasPrefix(k, c.curAddr) {
		return nil, nil, nil
	}
	return c.curAddr, c.dupVal(k, v), nil
}

// Unused DupSort surface — fail loudly (the loader drives HashedStorage with
// only SeekBothRange + NextDup on the read path).
func (c *stateDupCursor) SeekBothExact(_, _ []byte) ([]byte, []byte, error) {
	return nil, nil, errUnsupportedDup("SeekBothExact")
}
func (c *stateDupCursor) FirstDup() ([]byte, error)            { return nil, errUnsupportedDup("FirstDup") }
func (c *stateDupCursor) NextNoDup() ([]byte, []byte, error)   { return nil, nil, errUnsupportedDup("NextNoDup") }
func (c *stateDupCursor) PrevDup() ([]byte, []byte, error)     { return nil, nil, errUnsupportedDup("PrevDup") }
func (c *stateDupCursor) PrevNoDup() ([]byte, []byte, error)   { return nil, nil, errUnsupportedDup("PrevNoDup") }
func (c *stateDupCursor) LastDup() ([]byte, error)             { return nil, errUnsupportedDup("LastDup") }
func (c *stateDupCursor) CountDuplicates() (uint64, error)     { return 0, errUnsupportedDup("CountDuplicates") }
